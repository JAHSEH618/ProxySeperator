package localdns

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	mdns "github.com/miekg/dns"

	"github.com/friedhelmliu/ProxySeperator/internal/logging"
)

type Server struct {
	address           string
	cache             *Cache
	upstreamResolvers []string
	logger            *logging.Logger

	server *mdns.Server
	mu     sync.Mutex
}

func NewServer(address string, cache *Cache, upstreamResolvers []string, logger *logging.Logger) *Server {
	if cache == nil {
		cache = NewCache()
	}
	return &Server{
		address:           address,
		cache:             cache,
		upstreamResolvers: normalizeResolvers(upstreamResolvers),
		logger:            logger,
	}
}

func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server != nil {
		return nil
	}

	mux := mdns.NewServeMux()
	mux.HandleFunc(".", s.handleQuery)
	server := &mdns.Server{
		Addr:    s.address,
		Net:     "udp",
		Handler: mux,
	}
	go func() {
		if err := server.ListenAndServe(); err != nil {
			s.logger.Warn("dns", "本地 DNS 服务退出", map[string]any{"error": err.Error()})
		}
	}()
	s.server = server
	s.logger.Info("dns", "本地 DNS 服务已启动", map[string]any{"address": s.address})
	return nil
}

func (s *Server) Stop(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server == nil {
		return nil
	}
	err := s.server.Shutdown()
	s.server = nil
	return err
}

func (s *Server) handleQuery(w mdns.ResponseWriter, req *mdns.Msg) {
	msg := new(mdns.Msg)
	msg.SetReply(req)
	msg.Authoritative = true

	for _, q := range req.Question {
		domain := strings.TrimSuffix(strings.ToLower(q.Name), ".")
		if domain == "" || !isSupportedQuestionType(q.Qtype) {
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ips, err := s.lookupUpstream(ctx, domain, q.Qtype)
		cancel()
		if err != nil {
			s.logger.Warn("dns", "DNS 查询失败", map[string]any{"domain": domain, "error": err.Error()})
			continue
		}

		addresses := append([]netip.Addr(nil), ips...)
		appendAnswers(msg, q.Name, q.Qtype, addresses)
		if len(addresses) > 0 {
			s.cache.Set(domain, addresses, 30*time.Second)
		}
	}
	_ = w.WriteMsg(msg)
}

func isSupportedQuestionType(qType uint16) bool {
	switch qType {
	case mdns.TypeA, mdns.TypeAAAA:
		return true
	default:
		return false
	}
}

func normalizeResolvers(resolvers []string) []string {
	if len(resolvers) == 0 {
		resolvers = []string{"1.1.1.1:53", "8.8.8.8:53"}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(resolvers))
	for _, resolver := range resolvers {
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
	if len(out) == 0 {
		return []string{"1.1.1.1:53", "8.8.8.8:53"}
	}
	return out
}

func (s *Server) lookupUpstream(ctx context.Context, domain string, qType uint16) ([]netip.Addr, error) {
	query := new(mdns.Msg)
	query.SetQuestion(mdns.Fqdn(domain), qType)
	query.RecursionDesired = true

	client := &mdns.Client{Timeout: 5 * time.Second}
	var lastErr error
	for _, resolver := range s.upstreamResolvers {
		response, _, err := client.ExchangeContext(ctx, query, resolver)
		if err != nil {
			lastErr = err
			continue
		}
		if response == nil {
			lastErr = fmt.Errorf("empty DNS response from %s", resolver)
			continue
		}
		addrs := extractAddresses(response.Answer, qType)
		if len(addrs) > 0 {
			return addrs, nil
		}
		if response.Rcode != mdns.RcodeSuccess {
			lastErr = fmt.Errorf("upstream resolver %s returned %s", resolver, mdns.RcodeToString[response.Rcode])
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("upstream resolvers returned no answer")
	}
	return nil, lastErr
}

func extractAddresses(records []mdns.RR, qType uint16) []netip.Addr {
	addresses := make([]netip.Addr, 0, len(records))
	for _, rr := range records {
		switch record := rr.(type) {
		case *mdns.A:
			if qType != mdns.TypeA {
				continue
			}
			if addr, ok := netip.AddrFromSlice(record.A); ok {
				addresses = append(addresses, addr)
			}
		case *mdns.AAAA:
			if qType != mdns.TypeAAAA {
				continue
			}
			if addr, ok := netip.AddrFromSlice(record.AAAA); ok {
				addresses = append(addresses, addr)
			}
		}
	}
	return addresses
}

func appendAnswers(msg *mdns.Msg, fqdn string, qType uint16, addresses []netip.Addr) {
	for _, addr := range addresses {
		switch qType {
		case mdns.TypeA:
			if !addr.Is4() {
				continue
			}
			msg.Answer = append(msg.Answer, &mdns.A{
				Hdr: mdns.RR_Header{Name: fqdn, Rrtype: mdns.TypeA, Class: mdns.ClassINET, Ttl: 30},
				A:   addr.AsSlice(),
			})
		case mdns.TypeAAAA:
			if !addr.Is6() {
				continue
			}
			msg.Answer = append(msg.Answer, &mdns.AAAA{
				Hdr:  mdns.RR_Header{Name: fqdn, Rrtype: mdns.TypeAAAA, Class: mdns.ClassINET, Ttl: 30},
				AAAA: addr.AsSlice(),
			})
		}
	}
}
