package runtime

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	mdns "github.com/miekg/dns"

	"github.com/friedhelmliu/ProxySeperator/internal/logging"
	"github.com/friedhelmliu/ProxySeperator/internal/platform"
)

var companyFakeIPPrefixes = []netip.Prefix{
	netip.MustParsePrefix("198.18.0.0/15"),
}

type companyDialPreparer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
	Refresh(ctx context.Context)
	DynamicRoutes() []string
}

type companyDomainDialer struct {
	logger   *logging.Logger
	platform platform.Controller
	iface    string

	resolvers      []string
	snapshotWriter func([]string)

	mu          sync.Mutex
	domains     map[string]resolvedCompanyDomain
	routeRefs   map[string]int
	refreshLead time.Duration
}

type resolvedCompanyDomain struct {
	addresses []netip.Addr
	routes    []string
	expiresAt time.Time
}

type companyDNSAnswer struct {
	addresses []netip.Addr
	ttl       time.Duration
	resolver  string
}

func newCompanyDomainDialer(
	logger *logging.Logger,
	controller platform.Controller,
	iface string,
	resolvers []string,
	snapshotWriter func([]string),
) *companyDomainDialer {
	return &companyDomainDialer{
		logger:         logger,
		platform:       controller,
		iface:          strings.TrimSpace(iface),
		resolvers:      normalizeCompanyResolvers(resolvers),
		snapshotWriter: snapshotWriter,
		domains:        map[string]resolvedCompanyDomain{},
		routeRefs:      map[string]int{},
		refreshLead:    5 * time.Second,
	}
}

// DialContext resolves the company domain to real IPs, ensures bypass routes
// are installed, and dials the target through the system route.
func (d *companyDomainDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := splitDialTarget(addr)
	if err != nil {
		return nil, err
	}

	// If already an IP address, dial directly.
	if parsed, parseErr := netip.ParseAddr(host); parseErr == nil {
		return dialSystemRouteCandidates(ctx, network, []string{net.JoinHostPort(parsed.String(), port)})
	}

	// Try cached DNS result first.
	if targets, ok := d.cachedTargets(host, port, time.Now()); ok {
		return dialSystemRouteCandidates(ctx, network, targets)
	}

	// Resolve via company DNS.
	answer, err := d.lookupDomain(ctx, host)
	if err != nil {
		// Fall back to stale cache.
		if targets, ok := d.cachedTargets(host, port, time.Now().Add(30*time.Second)); ok {
			d.logger.Warn("runtime.company_dns", "公司域名解析失败，回退到最近一次缓存结果", map[string]any{
				"domain": host,
				"error":  err.Error(),
			})
			return dialSystemRouteCandidates(ctx, network, targets)
		}
		return nil, err
	}
	if d.logger != nil {
		d.logger.Info("runtime.company_dns", "公司域名解析命中真实地址", map[string]any{
			"domain":   host,
			"resolver": answer.resolver,
			"targets":  joinResolvedTargets(answer.addresses, port),
			"ttl":      answer.ttl.String(),
		})
	}
	if err := d.updateDomain(ctx, host, answer.addresses, answer.ttl); err != nil {
		return nil, err
	}
	return dialSystemRouteCandidates(ctx, network, joinResolvedTargets(answer.addresses, port))
}

func (d *companyDomainDialer) Refresh(ctx context.Context) {
	if d == nil {
		return
	}
	now := time.Now()
	d.mu.Lock()
	domains := make([]string, 0, len(d.domains))
	for domain, entry := range d.domains {
		if entry.expiresAt.After(now.Add(d.refreshLead)) {
			continue
		}
		domains = append(domains, domain)
	}
	d.mu.Unlock()

	for _, domain := range domains {
		answer, err := d.lookupDomain(ctx, domain)
		if err != nil {
			d.logger.Warn("runtime.company_dns", "刷新公司域名解析失败", map[string]any{
				"domain": domain,
				"error":  err.Error(),
			})
			continue
		}
		if err := d.updateDomain(ctx, domain, answer.addresses, answer.ttl); err != nil {
			d.logger.Warn("runtime.company_dns", "刷新公司域名主机路由失败", map[string]any{
				"domain": domain,
				"error":  err.Error(),
			})
		}
	}
}

func (d *companyDomainDialer) DynamicRoutes() []string {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return mapKeysSorted(d.routeRefs)
}

func (d *companyDomainDialer) cachedTargets(domain, port string, deadline time.Time) ([]string, bool) {
	if d == nil {
		return nil, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	entry, ok := d.domains[domain]
	if !ok || deadline.After(entry.expiresAt) || len(entry.addresses) == 0 {
		return nil, false
	}
	return joinResolvedTargets(entry.addresses, port), true
}

func (d *companyDomainDialer) updateDomain(ctx context.Context, domain string, addresses []netip.Addr, ttl time.Duration) error {
	if d == nil {
		return nil
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	routes := prefixesFromAddrs(addresses)

	d.mu.Lock()
	defer d.mu.Unlock()

	current := d.domains[domain]
	toAdd := make([]string, 0)
	for _, route := range routes {
		if d.routeRefs[route] == 0 {
			toAdd = append(toAdd, route)
		}
	}

	nextCounts := cloneRouteRefs(d.routeRefs)
	for _, route := range current.routes {
		if nextCounts[route] <= 1 {
			delete(nextCounts, route)
		} else {
			nextCounts[route]--
		}
	}
	for _, route := range routes {
		nextCounts[route]++
	}

	toRemove := make([]string, 0)
	for _, route := range current.routes {
		if nextCounts[route] == 0 {
			toRemove = append(toRemove, route)
		}
	}

	if len(toAdd) > 0 {
		if err := d.platform.ApplyCompanyBypassRoutes(ctx, d.iface, toAdd); err != nil {
			return err
		}
	}
	if len(toRemove) > 0 {
		if err := d.platform.ClearCompanyBypassRoutes(ctx, d.iface, toRemove); err != nil {
			return err
		}
	}

	d.routeRefs = nextCounts
	d.domains[domain] = resolvedCompanyDomain{
		addresses: append([]netip.Addr(nil), addresses...),
		routes:    append([]string(nil), routes...),
		expiresAt: time.Now().Add(ttl),
	}
	if d.snapshotWriter != nil {
		d.snapshotWriter(mapKeysSorted(d.routeRefs))
	}
	if d.logger != nil && (len(toAdd) > 0 || len(toRemove) > 0) {
		d.logger.Info("runtime.company_dns", "已刷新公司动态主机路由", map[string]any{
			"domain":  domain,
			"iface":   d.iface,
			"add":     toAdd,
			"remove":  toRemove,
			"active":  mapKeysSorted(d.routeRefs),
			"expires": d.domains[domain].expiresAt.Format(time.RFC3339),
		})
	}
	return nil
}

func (d *companyDomainDialer) lookupDomain(ctx context.Context, domain string) (companyDNSAnswer, error) {
	var lastErr error
	for _, resolver := range d.resolvers {
		answer, err := d.queryResolver(ctx, resolver, domain, mdns.TypeA)
		if err != nil {
			lastErr = err
			continue
		}
		if len(answer.addresses) > 0 {
			return answer, nil
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("公司域名 %s 没有可用的真实 IP 解析结果", domain)
	}
	return companyDNSAnswer{}, lastErr
}

func (d *companyDomainDialer) queryResolver(ctx context.Context, resolver, domain string, qType uint16) (companyDNSAnswer, error) {
	query := new(mdns.Msg)
	query.SetQuestion(mdns.Fqdn(domain), qType)
	query.RecursionDesired = true

	client := &mdns.Client{
		Net:     "udp",
		Timeout: 5 * time.Second,
	}
	if dialer := companyResolverDialer(d.iface, resolver); dialer != nil {
		client.Dialer = dialer
	}

	response, _, err := client.ExchangeContext(ctx, query, resolver)
	if err != nil {
		return companyDNSAnswer{}, err
	}
	if response == nil {
		return companyDNSAnswer{}, fmt.Errorf("resolver %s returned empty response", resolver)
	}
	if response.Rcode != mdns.RcodeSuccess {
		return companyDNSAnswer{}, fmt.Errorf("resolver %s returned %s", resolver, mdns.RcodeToString[response.Rcode])
	}

	addresses := make([]netip.Addr, 0, len(response.Answer))
	ttl := time.Duration(0)
	for _, record := range response.Answer {
		switch answer := record.(type) {
		case *mdns.A:
			addr, ok := netip.AddrFromSlice(answer.A)
			if !ok || isFakeCompanyAddr(addr) {
				continue
			}
			addresses = append(addresses, addr)
			ttl = minPositiveTTL(ttl, time.Duration(answer.Hdr.Ttl)*time.Second)
		case *mdns.AAAA:
			addr, ok := netip.AddrFromSlice(answer.AAAA)
			if !ok || isFakeCompanyAddr(addr) {
				continue
			}
			addresses = append(addresses, addr)
			ttl = minPositiveTTL(ttl, time.Duration(answer.Hdr.Ttl)*time.Second)
		}
	}
	if len(addresses) == 0 {
		return companyDNSAnswer{}, fmt.Errorf("resolver %s returned only fake or empty answers for %s", resolver, domain)
	}

	slices.SortFunc(addresses, func(a, b netip.Addr) int {
		return strings.Compare(a.String(), b.String())
	})
	return companyDNSAnswer{
		addresses: addresses,
		ttl:       ttl,
		resolver:  resolver,
	}, nil
}

func companyResolverDialer(iface, resolver string) *net.Dialer {
	if strings.TrimSpace(iface) == "" {
		return nil
	}
	host, _, err := net.SplitHostPort(resolver)
	if err != nil {
		host = resolver
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() {
		return nil
	}

	localAddr := localAddrForInterfaceFamily(iface, ip.To4() == nil)
	if localAddr == nil {
		return nil
	}
	return &net.Dialer{
		Timeout:   5 * time.Second,
		LocalAddr: localAddr,
	}
}

func localAddrForInterfaceFamily(iface string, wantIPv6 bool) net.Addr {
	netIf, err := net.InterfaceByName(iface)
	if err != nil {
		return nil
	}
	addrs, err := netIf.Addrs()
	if err != nil {
		return nil
	}
	for _, addr := range addrs {
		var ip net.IP
		switch value := addr.(type) {
		case *net.IPNet:
			ip = value.IP
		case *net.IPAddr:
			ip = value.IP
		default:
			continue
		}
		if ip == nil || ip.IsLoopback() {
			continue
		}
		if wantIPv6 {
			if ip.To16() == nil || ip.To4() != nil {
				continue
			}
			return &net.UDPAddr{IP: ip}
		}
		if v4 := ip.To4(); v4 != nil {
			return &net.UDPAddr{IP: v4}
		}
	}
	return nil
}

func normalizeCompanyResolvers(resolvers []string) []string {
	values := append([]string(nil), resolvers...)
	values = append(values, "127.0.0.1:53")
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, resolver := range values {
		resolver = strings.TrimSpace(resolver)
		if resolver == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(resolver); err != nil {
			resolver = net.JoinHostPort(resolver, "53")
		}
		if _, ok := seen[resolver]; ok {
			continue
		}
		seen[resolver] = struct{}{}
		out = append(out, resolver)
	}
	return out
}

func splitDialTarget(addr string) (string, string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(host), strings.TrimSpace(port), nil
}

func joinResolvedTargets(addresses []netip.Addr, port string) []string {
	targets := make([]string, 0, len(addresses))
	for _, addr := range addresses {
		targets = append(targets, net.JoinHostPort(addr.String(), port))
	}
	return targets
}

func prefixesFromAddrs(addresses []netip.Addr) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(addresses))
	for _, addr := range addresses {
		bits := 32
		if addr.Is6() {
			bits = 128
		}
		prefix := netip.PrefixFrom(addr, bits).String()
		if _, ok := seen[prefix]; ok {
			continue
		}
		seen[prefix] = struct{}{}
		out = append(out, prefix)
	}
	sort.Strings(out)
	return out
}

func isFakeCompanyAddr(addr netip.Addr) bool {
	for _, prefix := range companyFakeIPPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func minPositiveTTL(current, next time.Duration) time.Duration {
	switch {
	case current <= 0:
		return next
	case next <= 0:
		return current
	case next < current:
		return next
	default:
		return current
	}
}

func cloneRouteRefs(values map[string]int) map[string]int {
	cloned := make(map[string]int, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func mapKeysSorted(values map[string]int) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
