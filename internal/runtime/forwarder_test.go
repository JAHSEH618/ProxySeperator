package runtime

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
	localdns "github.com/friedhelmliu/ProxySeperator/internal/dns"
	"github.com/friedhelmliu/ProxySeperator/internal/logging"
	"github.com/friedhelmliu/ProxySeperator/internal/rules"
)

func TestForwarderRouteFallsBackToDomainFromDNSCache(t *testing.T) {
	cfg := api.DefaultConfig()
	cfg.Rules = []string{".company.internal"}

	cache := localdns.NewCache()
	cache.Set("api.company.internal", []netip.Addr{netip.MustParseAddr("203.0.113.8")}, time.Minute)

	forwarder := NewForwarder(
		cfg,
		rules.NewMatcher(rules.ParseLines(cfg.Rules).Compiled),
		cache,
		NewStatsTracker(),
		logging.NewLogger(logging.NewRingBuffer(10)),
	)

	result := forwarder.TestRoute("203.0.113.8:443")
	if result.Target != api.RouteTargetCompany {
		t.Fatalf("expected company target, got %+v", result)
	}
	if result.RuleType != api.RuleTypeDomainSuffix {
		t.Fatalf("expected domain suffix rule, got %+v", result)
	}
}

func TestForwarderKeepsLocalAddressDirectEvenWithDNSCache(t *testing.T) {
	cfg := api.DefaultConfig()
	cfg.Rules = []string{".company.internal"}

	cache := localdns.NewCache()
	cache.Set("api.company.internal", []netip.Addr{netip.MustParseAddr("192.168.1.30")}, time.Minute)

	forwarder := NewForwarder(
		cfg,
		rules.NewMatcher(rules.ParseLines(cfg.Rules).Compiled),
		cache,
		NewStatsTracker(),
		logging.NewLogger(logging.NewRingBuffer(10)),
	)

	result := forwarder.TestRoute("192.168.1.30:443")
	if result.Target != api.RouteTargetDirect {
		t.Fatalf("expected direct target, got %+v", result)
	}
	if result.RuleType != api.RuleTypeLocalIP {
		t.Fatalf("expected local IP rule, got %+v", result)
	}
}

func TestForwarderDialsCompanyTrafficDirectly(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	accepted := make(chan struct{}, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			accepted <- struct{}{}
			_ = conn.Close()
		}
	}()

	cfg := api.DefaultConfig()
	cfg.Rules = []string{"DOMAIN-KEYWORD,localhost"}
	cfg.PersonalUpstream = api.UpstreamConfig{Host: "127.0.0.1", Port: 1, Protocol: api.ProtocolSOCKS5}

	forwarder := NewForwarder(
		cfg,
		rules.NewMatcher(rules.ParseLines(cfg.Rules).Compiled),
		localdns.NewCache(),
		NewStatsTracker(),
		logging.NewLogger(logging.NewRingBuffer(10)),
	)

	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}

	conn, target, err := forwarder.DialTarget(context.Background(), "tcp", net.JoinHostPort("localhost", port))
	if err != nil {
		t.Fatalf("dial target failed: %v", err)
	}
	_ = conn.Close()

	if target != api.RouteTargetCompany {
		t.Fatalf("expected company target, got %q", target)
	}

	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("expected company traffic to dial destination directly")
	}
}

func TestForwarderFallsBackToSystemRouteLookupForCompanyTraffic(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	accepted := make(chan struct{}, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			accepted <- struct{}{}
			_ = conn.Close()
		}
	}()

	previousLookup := lookupSystemRouteAddrs
	previousDial := systemRouteDialContext
	lookupSystemRouteAddrs = func(context.Context, string) ([]string, error) {
		return []string{"127.0.0.1"}, nil
	}
	defer func() {
		lookupSystemRouteAddrs = previousLookup
		systemRouteDialContext = previousDial
	}()
	systemRouteDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err == nil && host == "service.invalid" {
			return nil, errors.New("lookup failed")
		}
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, network, addr)
	}

	cfg := api.DefaultConfig()
	cfg.Rules = []string{"DOMAIN-KEYWORD,service"}
	cfg.PersonalUpstream = api.UpstreamConfig{Host: "127.0.0.1", Port: 1, Protocol: api.ProtocolSOCKS5}

	forwarder := NewForwarder(
		cfg,
		rules.NewMatcher(rules.ParseLines(cfg.Rules).Compiled),
		localdns.NewCache(),
		NewStatsTracker(),
		logging.NewLogger(logging.NewRingBuffer(10)),
	)

	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, target, err := forwarder.DialTarget(ctx, "tcp", net.JoinHostPort("service.invalid", port))
	if err != nil {
		t.Fatalf("dial target failed: %v", err)
	}
	_ = conn.Close()

	if target != api.RouteTargetCompany {
		t.Fatalf("expected company target, got %q", target)
	}

	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("expected company traffic to use system-route lookup fallback")
	}
}
