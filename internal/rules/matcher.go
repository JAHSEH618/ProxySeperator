package rules

import (
	"net"
	"net/netip"
	"strings"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
)

type Matcher struct {
	exact    map[string]CompiledRule
	suffixes *suffixTrie
	keywords []CompiledRule
	cidrs    []CompiledRule
}

func NewMatcher(compiled []CompiledRule) *Matcher {
	m := &Matcher{
		exact:    map[string]CompiledRule{},
		suffixes: newSuffixTrie(),
	}
	for _, rule := range compiled {
		switch rule.Type {
		case api.RuleTypeDomainExact:
			m.exact[rule.Normalized] = rule
		case api.RuleTypeDomainSuffix:
			m.suffixes.Insert(rule.Normalized)
		case api.RuleTypeDomainKeyword:
			m.keywords = append(m.keywords, rule)
		case api.RuleTypeIPCIDR:
			m.cidrs = append(m.cidrs, rule)
		}
	}
	return m
}

func (m *Matcher) Match(input string) MatchResult {
	normalized := normalizeInput(input)
	if normalized == "" {
		return MatchResult{
			Normalized: normalized,
			Target:     api.RouteTargetPersonal,
			RuleType:   api.RuleTypeDefault,
			Reason:     "空目标，走默认策略",
		}
	}

	if addr, err := netip.ParseAddr(normalized); err == nil {
		if isLocalOrPrivateAddress(addr) {
			return MatchResult{
				Normalized: normalized,
				Target:     api.RouteTargetDirect,
				RuleType:   api.RuleTypeLocalIP,
				Reason:     "本地或私有地址不代理",
			}
		}
		for _, rule := range m.cidrs {
			if rule.Prefix.Contains(addr) {
				return MatchResult{
					Normalized:  normalized,
					Target:      api.RouteTargetCompany,
					RuleType:    rule.Type,
					MatchedRule: rule.Original,
					Reason:      "命中 IP 段规则",
				}
			}
		}
		return MatchResult{
			Normalized: normalized,
			Target:     api.RouteTargetPersonal,
			RuleType:   api.RuleTypeDefault,
			Reason:     "未命中规则，走默认个人代理",
		}
	}

	if exact, ok := m.exact[normalized]; ok {
		return MatchResult{
			Normalized:  normalized,
			Target:      api.RouteTargetCompany,
			RuleType:    exact.Type,
			MatchedRule: exact.Original,
			Reason:      "命中完整域名规则",
		}
	}
	if suffix := m.suffixes.Match(normalized); suffix != "" {
		return MatchResult{
			Normalized:  normalized,
			Target:      api.RouteTargetCompany,
			RuleType:    api.RuleTypeDomainSuffix,
			MatchedRule: "." + suffix,
			Reason:      "命中域名后缀规则",
		}
	}
	for _, keyword := range m.keywords {
		if strings.Contains(normalized, keyword.Normalized) {
			return MatchResult{
				Normalized:  normalized,
				Target:      api.RouteTargetCompany,
				RuleType:    keyword.Type,
				MatchedRule: keyword.Original,
				Reason:      "命中域名关键词规则",
			}
		}
	}
	return MatchResult{
		Normalized: normalized,
		Target:     api.RouteTargetPersonal,
		RuleType:   api.RuleTypeDefault,
		Reason:     "未命中规则，走默认个人代理",
	}
}

func normalizeInput(input string) string {
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
