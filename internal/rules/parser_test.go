package rules

import (
	"testing"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
)

func TestParseLinesAndMatcher(t *testing.T) {
	result := ParseLines([]string{
		".company.com",
		"api.example.com",
		"corp",
		"8.8.8.0/24",
		"10.0.0.0/99",
	})

	if len(result.Invalid) != 1 {
		t.Fatalf("expected 1 invalid rule, got %d", len(result.Invalid))
	}
	if result.Summary.DomainSuffix != 1 || result.Summary.DomainExact != 1 || result.Summary.DomainKeyword != 1 || result.Summary.CIDR != 1 {
		t.Fatalf("unexpected summary: %+v", result.Summary)
	}

	matcher := NewMatcher(result.Compiled)

	if got := matcher.Match("git.company.com"); got.Target != api.RouteTargetCompany || got.RuleType != api.RuleTypeDomainSuffix {
		t.Fatalf("expected company suffix match, got %+v", got)
	}
	if got := matcher.Match("api.example.com"); got.Target != api.RouteTargetCompany || got.RuleType != api.RuleTypeDomainExact {
		t.Fatalf("expected company exact match, got %+v", got)
	}
	if got := matcher.Match("corp-dev.local"); got.Target != api.RouteTargetCompany || got.RuleType != api.RuleTypeDomainKeyword {
		t.Fatalf("expected company keyword match, got %+v", got)
	}
	if got := matcher.Match("8.8.8.8"); got.Target != api.RouteTargetCompany || got.RuleType != api.RuleTypeIPCIDR {
		t.Fatalf("expected company CIDR match, got %+v", got)
	}
	if got := matcher.Match("127.0.0.1"); got.Target != api.RouteTargetDirect {
		t.Fatalf("expected direct for loopback, got %+v", got)
	}
	if got := matcher.Match("google.com"); got.Target != api.RouteTargetPersonal {
		t.Fatalf("expected default personal, got %+v", got)
	}
}
