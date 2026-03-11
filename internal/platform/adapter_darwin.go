//go:build darwin

package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
	"github.com/friedhelmliu/ProxySeperator/internal/logging"
)

const (
	darwinTUNAddress = "198.18.0.1"
	darwinTUNDevice  = "tun://utun"
)

var (
	darwinNameserverPattern = regexp.MustCompile(`nameserver\[\d+\]\s*:\s*(\S+)`)
	darwinSplitRoutes       = []string{"0.0.0.0/1", "128.0.0.0/1"}
)

type darwinController struct {
	logger *logging.Logger

	mu           sync.Mutex
	tun          *tunHelperProcess
	tunInterface string
	dnsSnapshot  map[string][]string
}

func NewController(logger *logging.Logger) Controller {
	return &darwinController{logger: logger}
}

func (c *darwinController) ApplySystemProxy(ctx context.Context, cfg SystemProxyConfig) error {
	services, err := c.listNetworkServices(ctx)
	if err != nil {
		return err
	}
	hostHTTP, portHTTP, err := splitHostPort(cfg.HTTPAddress)
	if err != nil {
		return api.WrapError(api.ErrCodeSystemProxyFailed, "无效的 HTTP 代理地址", err)
	}
	hostSOCKS, portSOCKS, err := splitHostPort(cfg.SOCKSAddress)
	if err != nil {
		return api.WrapError(api.ErrCodeSystemProxyFailed, "无效的 SOCKS 代理地址", err)
	}
	for _, service := range services {
		if err := run(ctx, "networksetup", "-setwebproxy", service, hostHTTP, portHTTP); err != nil {
			return api.WrapError(api.ErrCodeSystemProxyFailed, "设置 Web 代理失败", err)
		}
		if err := run(ctx, "networksetup", "-setsecurewebproxy", service, hostHTTP, portHTTP); err != nil {
			return api.WrapError(api.ErrCodeSystemProxyFailed, "设置 Secure Web 代理失败", err)
		}
		if err := run(ctx, "networksetup", "-setsocksfirewallproxy", service, hostSOCKS, portSOCKS); err != nil {
			return api.WrapError(api.ErrCodeSystemProxyFailed, "设置 SOCKS 代理失败", err)
		}
		if err := run(ctx, "networksetup", "-setwebproxystate", service, "on"); err != nil {
			return api.WrapError(api.ErrCodeSystemProxyFailed, "开启 Web 代理失败", err)
		}
		if err := run(ctx, "networksetup", "-setsecurewebproxystate", service, "on"); err != nil {
			return api.WrapError(api.ErrCodeSystemProxyFailed, "开启 Secure Web 代理失败", err)
		}
		if err := run(ctx, "networksetup", "-setsocksfirewallproxystate", service, "on"); err != nil {
			return api.WrapError(api.ErrCodeSystemProxyFailed, "开启 SOCKS 代理失败", err)
		}
	}
	return nil
}

func (c *darwinController) ClearSystemProxy(ctx context.Context) error {
	services, err := c.listNetworkServices(ctx)
	if err != nil {
		return err
	}
	for _, service := range services {
		_ = run(ctx, "networksetup", "-setwebproxystate", service, "off")
		_ = run(ctx, "networksetup", "-setsecurewebproxystate", service, "off")
		_ = run(ctx, "networksetup", "-setsocksfirewallproxystate", service, "off")
	}
	return nil
}

func (c *darwinController) EnableAutoStart(ctx context.Context, executablePath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return api.WrapError(api.ErrCodePermissionDenied, "无法获取用户目录", err)
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return api.WrapError(api.ErrCodePermissionDenied, "无法创建 LaunchAgents 目录", err)
	}
	plistPath := filepath.Join(dir, "com.proxyseparator.app.plist")
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key>
    <string>com.proxyseparator.app</string>
    <key>ProgramArguments</key>
    <array>
      <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
  </dict>
</plist>
`, executablePath)
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return api.WrapError(api.ErrCodePermissionDenied, "写入 LaunchAgent 失败", err)
	}
	return nil
}

func (c *darwinController) DisableAutoStart(context.Context) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return api.WrapError(api.ErrCodePermissionDenied, "无法获取用户目录", err)
	}
	plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.proxyseparator.app.plist")
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return api.WrapError(api.ErrCodePermissionDenied, "删除 LaunchAgent 失败", err)
	}
	return nil
}

func (c *darwinController) CurrentDNSResolvers(ctx context.Context) ([]string, error) {
	output, err := exec.CommandContext(ctx, "scutil", "--dns").Output()
	if err != nil {
		return nil, fmt.Errorf("read scutil dns: %w", err)
	}
	seen := map[string]struct{}{}
	resolvers := make([]string, 0)
	for _, match := range darwinNameserverPattern.FindAllStringSubmatch(string(output), -1) {
		resolver := strings.TrimSpace(match[1])
		if resolver == "" || strings.HasPrefix(resolver, "127.") || resolver == "::1" {
			continue
		}
		if _, ok := seen[resolver]; ok {
			continue
		}
		seen[resolver] = struct{}{}
		resolvers = append(resolvers, resolver)
	}
	if len(resolvers) == 0 {
		return nil, fmt.Errorf("no active DNS resolver detected")
	}
	return resolvers, nil
}

func (c *darwinController) StartTUN(ctx context.Context, opts TUNOptions) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.tun != nil {
		return api.NewError(api.ErrCodeRuntimeAlreadyRunning, "TUN 已经启动")
	}

	services, err := c.listNetworkServices(ctx)
	if err != nil {
		return err
	}
	dnsSnapshot, err := c.snapshotDNSServers(ctx, services)
	if err != nil {
		return api.WrapError(api.ErrCodeTUNUnavailable, "读取系统 DNS 快照失败", err)
	}

	helper, err := startTUNHelper(ctx, c.logger, tunHelperOptions{
		Device:     darwinTUNDevice,
		Proxy:      "socks5://" + opts.SOCKSListenAddress,
		LogLevel:   "info",
		MTU:        maxInt(opts.MTU, 1500),
		UDPTimeout: 30 * time.Second,
	})
	if err != nil {
		return err
	}

	success := false
	tunInterface := ""
	defer func() {
		if success {
			return
		}
		c.cleanupFailedStart(ctx, helper, tunInterface, dnsSnapshot)
	}()

	tunInterface, err = helper.WaitReady(10 * time.Second)
	if err != nil {
		return err
	}
	if err := c.configureTUNInterface(ctx, tunInterface); err != nil {
		return api.WrapError(api.ErrCodeTUNUnavailable, "配置 utun 接口失败", err)
	}
	if err := c.installSplitRoutes(ctx, tunInterface); err != nil {
		return api.WrapError(api.ErrCodeTUNUnavailable, "写入 TUN 路由失败", err)
	}
	if err := c.applyLocalDNS(ctx, services, opts.DNSListenAddress); err != nil {
		return api.WrapError(api.ErrCodeSystemProxyFailed, "切换系统 DNS 到本地解析器失败", err)
	}

	c.tun = helper
	c.tunInterface = tunInterface
	c.dnsSnapshot = dnsSnapshot
	success = true

	c.logger.Info("platform.darwin", "TUN 已启动", map[string]any{
		"interface": tunInterface,
		"dns":       opts.DNSListenAddress,
	})
	return nil
}

func (c *darwinController) StopTUN(ctx context.Context) error {
	c.mu.Lock()
	helper := c.tun
	tunInterface := c.tunInterface
	dnsSnapshot := c.dnsSnapshot
	c.tun = nil
	c.tunInterface = ""
	c.dnsSnapshot = nil
	c.mu.Unlock()

	if helper == nil {
		return nil
	}

	var firstErr error
	if err := c.restoreDNSServers(ctx, dnsSnapshot); err != nil && firstErr == nil {
		firstErr = api.WrapError(api.ErrCodeSystemProxyFailed, "恢复系统 DNS 失败", err)
	}
	if err := c.removeSplitRoutes(ctx, tunInterface); err != nil && firstErr == nil {
		firstErr = api.WrapError(api.ErrCodeTUNUnavailable, "删除 TUN 路由失败", err)
	}
	if tunInterface != "" {
		_ = run(ctx, "ifconfig", tunInterface, "down")
	}
	if err := helper.Stop(ctx); err != nil && !errors.Is(err, context.Canceled) && firstErr == nil {
		firstErr = api.WrapError(api.ErrCodeTUNUnavailable, "停止 TUN helper 失败", err)
	}
	return firstErr
}

func (c *darwinController) cleanupFailedStart(ctx context.Context, helper *tunHelperProcess, tunInterface string, dnsSnapshot map[string][]string) {
	_ = c.restoreDNSServers(ctx, dnsSnapshot)
	_ = c.removeSplitRoutes(ctx, tunInterface)
	if tunInterface != "" {
		_ = run(ctx, "ifconfig", tunInterface, "down")
	}
	if helper != nil {
		_ = helper.Stop(ctx)
	}
}

func (c *darwinController) configureTUNInterface(ctx context.Context, name string) error {
	return run(ctx, "ifconfig", name, "inet", darwinTUNAddress, darwinTUNAddress, "up")
}

func (c *darwinController) installSplitRoutes(ctx context.Context, tunInterface string) error {
	for _, route := range darwinSplitRoutes {
		if err := run(ctx, "route", "-n", "add", "-net", route, "-interface", tunInterface); err != nil {
			return err
		}
	}
	return nil
}

func (c *darwinController) removeSplitRoutes(ctx context.Context, tunInterface string) error {
	if tunInterface == "" {
		return nil
	}
	var firstErr error
	for _, route := range darwinSplitRoutes {
		if err := run(ctx, "route", "-n", "delete", "-net", route, "-interface", tunInterface); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (c *darwinController) snapshotDNSServers(ctx context.Context, services []string) (map[string][]string, error) {
	snapshot := make(map[string][]string, len(services))
	for _, service := range services {
		servers, err := c.getDNSServers(ctx, service)
		if err != nil {
			return nil, err
		}
		snapshot[service] = servers
	}
	return snapshot, nil
}

func (c *darwinController) applyLocalDNS(ctx context.Context, services []string, dnsListenAddress string) error {
	host, _, err := splitHostPort(dnsListenAddress)
	if err != nil {
		return err
	}
	for _, service := range services {
		if err := c.setDNSServers(ctx, service, []string{host}); err != nil {
			return err
		}
	}
	return nil
}

func (c *darwinController) restoreDNSServers(ctx context.Context, snapshot map[string][]string) error {
	var firstErr error
	for service, servers := range snapshot {
		if err := c.setDNSServers(ctx, service, servers); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (c *darwinController) getDNSServers(ctx context.Context, service string) ([]string, error) {
	output, err := exec.CommandContext(ctx, "networksetup", "-getdnsservers", service).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("get dns servers for %s: %w: %s", service, err, strings.TrimSpace(string(output)))
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 1 && strings.Contains(lines[0], "There aren't any DNS Servers set") {
		return nil, nil
	}
	servers := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		servers = append(servers, line)
	}
	return servers, nil
}

func (c *darwinController) setDNSServers(ctx context.Context, service string, servers []string) error {
	args := []string{"-setdnsservers", service}
	if len(servers) == 0 {
		args = append(args, "Empty")
	} else {
		args = append(args, servers...)
	}
	return run(ctx, "networksetup", args...)
}

func (c *darwinController) listNetworkServices(ctx context.Context) ([]string, error) {
	cmd := exec.CommandContext(ctx, "networksetup", "-listallnetworkservices")
	output, err := cmd.Output()
	if err != nil {
		return nil, api.WrapError(api.ErrCodeSystemProxyFailed, "读取网络服务列表失败", err)
	}
	lines := strings.Split(string(output), "\n")
	services := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "An asterisk") || strings.HasPrefix(line, "*") {
			continue
		}
		services = append(services, line)
	}
	if len(services) == 0 {
		return nil, api.NewError(api.ErrCodeSystemProxyFailed, "未检测到可用网络服务")
	}
	return services, nil
}

func run(ctx context.Context, name string, args ...string) error {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err == nil {
		return nil
	}
	if trimmed := strings.TrimSpace(string(output)); trimmed != "" {
		return fmt.Errorf("%w: %s", err, trimmed)
	}
	return err
}
