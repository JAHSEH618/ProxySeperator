package runtime

import (
	"sort"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
	"github.com/friedhelmliu/ProxySeperator/internal/rules"
)

func companyBypassCIDRs(cfg api.Config) []string {
	parseResult := rules.ParseLines(cfg.Rules)
	seen := map[string]struct{}{}
	routes := make([]string, 0, parseResult.Summary.CIDR)
	for _, compiled := range parseResult.Compiled {
		if compiled.Type != api.RuleTypeIPCIDR {
			continue
		}
		prefix := compiled.Prefix.String()
		if prefix == "" {
			continue
		}
		if _, ok := seen[prefix]; ok {
			continue
		}
		seen[prefix] = struct{}{}
		routes = append(routes, prefix)
	}
	sort.Strings(routes)
	return routes
}
