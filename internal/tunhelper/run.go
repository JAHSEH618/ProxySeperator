package tunhelper

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	_ "github.com/xjasonlyu/tun2socks/v2/dns"
	"github.com/xjasonlyu/tun2socks/v2/engine"
)

type config struct {
	Device     string
	Proxy      string
	Interface  string
	LogLevel   string
	MTU        int
	UDPTimeout time.Duration
}

func Run(args []string) error {
	cfg := config{
		LogLevel: "info",
		MTU:      1500,
	}

	flags := flag.NewFlagSet("tun-helper", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.StringVar(&cfg.Device, "device", "", "tun device")
	flags.StringVar(&cfg.Proxy, "proxy", "", "upstream proxy")
	flags.StringVar(&cfg.Interface, "interface", "", "egress interface")
	flags.StringVar(&cfg.LogLevel, "loglevel", cfg.LogLevel, "log level")
	flags.IntVar(&cfg.MTU, "mtu", cfg.MTU, "tun mtu")
	flags.DurationVar(&cfg.UDPTimeout, "udp-timeout", 30*time.Second, "udp timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if cfg.Device == "" {
		return errors.New("missing --device")
	}
	if cfg.Proxy == "" {
		return errors.New("missing --proxy")
	}

	before := snapshotInterfaces()
	engine.Insert(&engine.Key{
		MTU:        cfg.MTU,
		Proxy:      cfg.Proxy,
		Device:     cfg.Device,
		Interface:  cfg.Interface,
		LogLevel:   cfg.LogLevel,
		UDPTimeout: cfg.UDPTimeout,
	})

	engine.Start()
	defer engine.Stop()

	name, err := waitForInterface(before, cfg.Device, 5*time.Second)
	if err != nil {
		return err
	}
	fmt.Printf("TUN_READY %s\n", name)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	<-signals
	return nil
}

func snapshotInterfaces() map[string]struct{} {
	ifaces, err := net.Interfaces()
	if err != nil {
		return map[string]struct{}{}
	}
	out := make(map[string]struct{}, len(ifaces))
	for _, iface := range ifaces {
		out[iface.Name] = struct{}{}
	}
	return out
}

func waitForInterface(before map[string]struct{}, device string, timeout time.Duration) (string, error) {
	hint := deviceHint(device)
	deadline := time.Now().Add(timeout)
	for {
		after := snapshotInterfaces()
		if hint != "" {
			if _, ok := after[hint]; ok {
				return hint, nil
			}
		}
		if name := findNewInterface(before, after); name != "" {
			return name, nil
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if hint != "" {
		return hint, nil
	}
	return "", fmt.Errorf("unable to determine TUN interface for %q", device)
}

func deviceHint(device string) string {
	if !strings.Contains(device, "://") {
		return strings.TrimSpace(device)
	}
	u, err := url.Parse(device)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(u.Host)
}

func findNewInterface(before, after map[string]struct{}) string {
	candidates := make([]string, 0)
	for name := range after {
		if _, ok := before[name]; ok {
			continue
		}
		candidates = append(candidates, name)
	}
	if len(candidates) == 0 {
		return ""
	}
	sort.Slice(candidates, func(i, j int) bool {
		left := interfaceScore(candidates[i])
		right := interfaceScore(candidates[j])
		if left == right {
			return candidates[i] < candidates[j]
		}
		return left > right
	})
	return candidates[0]
}

func interfaceScore(name string) int {
	lower := strings.ToLower(name)
	switch runtime.GOOS {
	case "darwin":
		switch {
		case strings.HasPrefix(lower, "utun"):
			return 100
		case strings.Contains(lower, "tun"):
			return 50
		default:
			return 0
		}
	case "windows":
		switch {
		case strings.Contains(lower, "proxyseparator"):
			return 100
		case strings.Contains(lower, "wintun"):
			return 80
		case strings.Contains(lower, "tun"):
			return 50
		default:
			return 0
		}
	default:
		if strings.Contains(lower, "tun") {
			return 50
		}
	}
	return 0
}
