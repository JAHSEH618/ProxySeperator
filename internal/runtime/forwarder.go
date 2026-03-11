package runtime

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
	localdns "github.com/friedhelmliu/ProxySeperator/internal/dns"
	"github.com/friedhelmliu/ProxySeperator/internal/logging"
	"github.com/friedhelmliu/ProxySeperator/internal/rules"
)

type Forwarder struct {
	logger   *logging.Logger
	stats    *StatsTracker
	dnsCache *localdns.Cache

	mu             sync.RWMutex
	matcher        *rules.Matcher
	companyConfig  api.UpstreamConfig
	personalConfig api.UpstreamConfig
	health         api.HealthStatus
}

func NewForwarder(cfg api.Config, matcher *rules.Matcher, dnsCache *localdns.Cache, stats *StatsTracker, logger *logging.Logger) *Forwarder {
	return &Forwarder{
		logger:         logger,
		stats:          stats,
		dnsCache:       dnsCache,
		matcher:        matcher,
		companyConfig:  cfg.CompanyUpstream,
		personalConfig: cfg.PersonalUpstream,
	}
}

func (f *Forwarder) RefreshHealth(ctx context.Context) api.HealthStatus {
	company := ProbeUpstream(ctx, f.companyConfig)
	personal := ProbeUpstream(ctx, f.personalConfig)
	status := api.HealthStatus{
		CheckedAt: time.Now(),
		Company:   company,
		Personal:  personal,
	}
	f.mu.Lock()
	f.health = status
	f.mu.Unlock()
	return status
}

func (f *Forwarder) Health() api.HealthStatus {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.health
}

func (f *Forwarder) TestRoute(input string) api.RouteTestResult {
	result := f.matchTarget(input)
	return api.RouteTestResult{
		Input:       input,
		Normalized:  result.Normalized,
		Target:      result.Target,
		RuleType:    result.RuleType,
		MatchedRule: result.MatchedRule,
		Reason:      result.Reason,
	}
}

func (f *Forwarder) DialTarget(ctx context.Context, network, addr string) (net.Conn, string, error) {
	result := f.matchTarget(addr)
	target := result.Target

	var conn net.Conn
	var err error
	switch target {
	case api.RouteTargetCompany:
		conn, err = f.dialViaUpstream(ctx, f.companyConfig, addr)
	case api.RouteTargetDirect:
		conn, err = (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
	default:
		target = api.RouteTargetPersonal
		conn, err = f.dialViaUpstream(ctx, f.personalConfig, addr)
	}
	if err != nil {
		return nil, target, err
	}
	f.stats.SessionStarted()
	return &trackedConn{Conn: conn, target: target, stats: f.stats}, target, nil
}

func (f *Forwarder) matchTarget(input string) rules.MatchResult {
	host := normalizeTarget(input)
	result := f.matcher.Match(host)
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return result
	}
	if result.RuleType != api.RuleTypeDefault {
		return result
	}
	if f.dnsCache == nil {
		return result
	}
	domain, ok := f.dnsCache.LookupAddr(addr)
	if !ok {
		return result
	}
	hinted := f.matcher.Match(domain)
	if hinted.RuleType == api.RuleTypeDefault {
		return result
	}
	hinted.Normalized = host
	hinted.Reason = "通过 DNS 缓存回溯域名后，" + hinted.Reason
	return hinted
}

func normalizeTarget(input string) string {
	value := strings.TrimSpace(strings.ToLower(input))
	value = strings.TrimSuffix(value, ".")
	if value == "" {
		return value
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		return host
	}
	return value
}

func (f *Forwarder) dialViaUpstream(ctx context.Context, cfg api.UpstreamConfig, target string) (net.Conn, error) {
	protocol := cfg.Protocol
	if protocol == "" || protocol == api.ProtocolAuto {
		status := ProbeUpstream(ctx, cfg)
		protocol = status.Protocol
	}
	switch protocol {
	case api.ProtocolDirect:
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", target)
	case api.ProtocolSOCKS5:
		return dialSOCKS5(ctx, cfg.Address(), target)
	case api.ProtocolHTTP:
		return dialHTTPConnect(ctx, cfg.Address(), target)
	default:
		return nil, api.NewError(api.ErrCodeUpstreamUnavailable, "无法识别上游代理协议")
	}
}

type trackedConn struct {
	net.Conn
	target string
	stats  *StatsTracker
	once   sync.Once
}

func (c *trackedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.stats.AddRX(uint64(n), c.target)
	}
	return n, err
}

func (c *trackedConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.stats.AddTX(uint64(n), c.target)
	}
	return n, err
}

func (c *trackedConn) Close() error {
	var err error
	c.once.Do(func() {
		c.stats.SessionEnded()
		err = c.Conn.Close()
	})
	return err
}

func dialHTTPConnect(ctx context.Context, proxyAddr, target string) (net.Conn, error) {
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	request := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Connection: Keep-Alive\r\n\r\n", target, target)
	if _, err := conn.Write([]byte(request)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if !strings.Contains(line, "200") {
		_ = conn.Close()
		return nil, fmt.Errorf("http connect failed: %s", strings.TrimSpace(line))
	}
	return conn, nil
}

func dialSOCKS5(ctx context.Context, proxyAddr, target string) (net.Conn, error) {
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	resp := make([]byte, 2)
	if _, err := ioReadFull(conn, resp); err != nil {
		_ = conn.Close()
		return nil, err
	}

	host, portString, err := net.SplitHostPort(target)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	packet := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			packet = append(packet, 0x01)
			packet = append(packet, v4...)
		} else {
			packet = append(packet, 0x04)
			packet = append(packet, ip.To16()...)
		}
	} else {
		packet = append(packet, 0x03, byte(len(host)))
		packet = append(packet, host...)
	}
	portBytes := []byte{0, 0}
	binary.BigEndian.PutUint16(portBytes, uint16(port))
	packet = append(packet, portBytes...)
	if _, err := conn.Write(packet); err != nil {
		_ = conn.Close()
		return nil, err
	}
	head := make([]byte, 4)
	if _, err := ioReadFull(conn, head); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if head[1] != 0x00 {
		_ = conn.Close()
		return nil, fmt.Errorf("socks5 connect failed: %d", head[1])
	}
	if err := consumeSOCKS5Address(conn, head[3]); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func consumeSOCKS5Address(conn net.Conn, atyp byte) error {
	var size int
	switch atyp {
	case 0x01:
		size = 4 + 2
	case 0x03:
		length := []byte{0}
		if _, err := ioReadFull(conn, length); err != nil {
			return err
		}
		size = int(length[0]) + 2
		buf := make([]byte, size)
		_, err := ioReadFull(conn, buf)
		return err
	case 0x04:
		size = 16 + 2
	default:
		return fmt.Errorf("unsupported atyp %d", atyp)
	}
	buf := make([]byte, size)
	_, err := ioReadFull(conn, buf)
	return err
}
