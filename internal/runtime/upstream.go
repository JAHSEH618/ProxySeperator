package runtime

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
)

type Upstream struct {
	Name   string
	Config api.UpstreamConfig
	Health api.UpstreamHealth
}

func ProbeUpstream(ctx context.Context, cfg api.UpstreamConfig) api.UpstreamHealth {
	start := time.Now()
	health := api.UpstreamHealth{Protocol: api.ProtocolUnknown}

	conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", cfg.Address())
	if err != nil {
		health.ConsecutiveFailures++
		return health
	}
	_ = conn.Close()
	health.Reachable = true
	health.RTTMs = time.Since(start).Milliseconds()
	health.LastSuccessAt = time.Now()

	protocol := cfg.Protocol
	if protocol == "" {
		protocol = api.ProtocolAuto
	}
	switch protocol {
	case api.ProtocolSOCKS5:
		if err := detectSOCKS5(ctx, cfg); err == nil {
			health.Protocol = api.ProtocolSOCKS5
			return health
		}
	case api.ProtocolHTTP:
		if err := detectHTTPProxy(ctx, cfg); err == nil {
			health.Protocol = api.ProtocolHTTP
			return health
		}
	default:
		if err := detectSOCKS5(ctx, cfg); err == nil {
			health.Protocol = api.ProtocolSOCKS5
			return health
		}
		if err := detectHTTPProxy(ctx, cfg); err == nil {
			health.Protocol = api.ProtocolHTTP
			return health
		}
	}
	health.Protocol = api.ProtocolUnknown
	return health
}

func detectSOCKS5(ctx context.Context, cfg api.UpstreamConfig) error {
	conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", cfg.Address())
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return err
	}
	resp := make([]byte, 2)
	if _, err := ioReadFull(conn, resp); err != nil {
		return err
	}
	if resp[0] != 0x05 {
		return fmt.Errorf("not socks5")
	}
	return nil
}

func detectHTTPProxy(ctx context.Context, cfg api.UpstreamConfig) error {
	conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", cfg.Address())
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("CONNECT 127.0.0.1:1 HTTP/1.1\r\nHost: 127.0.0.1:1\r\n\r\n")); err != nil {
		return err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return err
	}
	if !strings.HasPrefix(line, "HTTP/") {
		return fmt.Errorf("not http proxy")
	}
	return nil
}

func ioReadFull(conn net.Conn, buf []byte) (int, error) {
	read := 0
	for read < len(buf) {
		n, err := conn.Read(buf[read:])
		read += n
		if err != nil {
			return read, err
		}
	}
	return read, nil
}
