package rules

import (
	"net/netip"
	"regexp"
	"strings"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
)

var domainPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*[a-z0-9]$`)
var suffixPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)

func ParseLines(lines []string) ParseResult {
	result := ParseResult{}
	for idx, raw := range lines {
		result.Summary.Total++
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		compiled, reason, ok := parseRule(trimmed)
		if !ok {
			result.Invalid = append(result.Invalid, InvalidRule{
				Line:   idx + 1,
				Input:  raw,
				Reason: reason,
			})
			result.Summary.Invalid++
			continue
		}

		result.Compiled = append(result.Compiled, compiled)
		result.Valid = append(result.Valid, compiled.Original)
		result.Summary.Valid++
		switch compiled.Type {
		case api.RuleTypeDomainSuffix:
			result.Summary.DomainSuffix++
		case api.RuleTypeDomainExact:
			result.Summary.DomainExact++
		case api.RuleTypeDomainKeyword:
			result.Summary.DomainKeyword++
		case api.RuleTypeIPCIDR:
			result.Summary.CIDR++
		}
	}
	return result
}

func parseRule(input string) (CompiledRule, string, bool) {
	normalized := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(input), "."))
	if normalized == "" {
		return CompiledRule{}, "空规则", false
	}

	if strings.Contains(normalized, ",") {
		parts := strings.SplitN(normalized, ",", 2)
		if len(parts) != 2 {
			return CompiledRule{}, "规则格式错误", false
		}
		prefix := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		switch prefix {
		case "domain-suffix":
			return newDomainSuffixRule(input, value)
		case "domain-keyword":
			return newKeywordRule(input, value)
		case "ip-cidr":
			return newCIDRRule(input, value)
		case "domain":
			return newDomainExactRule(input, value)
		}
	}

	if _, err := netip.ParsePrefix(normalized); err == nil {
		return newCIDRRule(input, normalized)
	}
	if strings.HasPrefix(normalized, ".") {
		return newDomainSuffixRule(input, normalized[1:])
	}
	if strings.Contains(normalized, ".") {
		return newDomainExactRule(input, normalized)
	}
	return newKeywordRule(input, normalized)
}

func newDomainSuffixRule(original, value string) (CompiledRule, string, bool) {
	if !isValidSuffix(value) {
		return CompiledRule{}, "无效的域名后缀", false
	}
	return CompiledRule{
		Type:       api.RuleTypeDomainSuffix,
		Original:   strings.TrimSpace(original),
		Normalized: strings.TrimPrefix(strings.ToLower(value), "."),
	}, "", true
}

func newDomainExactRule(original, value string) (CompiledRule, string, bool) {
	if !isValidDomain(value) {
		return CompiledRule{}, "无效的完整域名", false
	}
	return CompiledRule{
		Type:       api.RuleTypeDomainExact,
		Original:   strings.TrimSpace(original),
		Normalized: strings.ToLower(value),
	}, "", true
}

func newKeywordRule(original, value string) (CompiledRule, string, bool) {
	if strings.TrimSpace(value) == "" {
		return CompiledRule{}, "无效的关键词规则", false
	}
	return CompiledRule{
		Type:       api.RuleTypeDomainKeyword,
		Original:   strings.TrimSpace(original),
		Normalized: strings.ToLower(value),
	}, "", true
}

func newCIDRRule(original, value string) (CompiledRule, string, bool) {
	prefix, err := netip.ParsePrefix(value)
	if err != nil {
		return CompiledRule{}, "无效的 CIDR 规则", false
	}
	return CompiledRule{
		Type:       api.RuleTypeIPCIDR,
		Original:   strings.TrimSpace(original),
		Normalized: prefix.String(),
		Prefix:     prefix,
	}, "", true
}

func isValidDomain(value string) bool {
	return domainPattern.MatchString(value) && strings.Contains(value, ".")
}

func isValidSuffix(value string) bool {
	return suffixPattern.MatchString(value)
}
