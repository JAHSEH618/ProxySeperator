package rules

import "net/netip"

type CompiledRule struct {
	Type       string
	Original   string
	Normalized string
	Prefix     netip.Prefix
}

type ParseResult struct {
	Compiled []CompiledRule
	Valid    []string
	Invalid  []InvalidRule
	Summary  Summary
}

type InvalidRule struct {
	Line   int
	Input  string
	Reason string
}

type Summary struct {
	Total         int
	Valid         int
	Invalid       int
	DomainSuffix  int
	DomainExact   int
	DomainKeyword int
	CIDR          int
}

type MatchResult struct {
	Normalized  string
	Target      string
	RuleType    string
	MatchedRule string
	Reason      string
}
