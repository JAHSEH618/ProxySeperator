package runtime

import (
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
