package runtime

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	mdns "github.com/miekg/dns"

	"github.com/friedhelmliu/ProxySeperator/internal/logging"
)

func TestCompanyDomainDialerUsesRealResolverAndRegistersHostRoute(t *testing.T) {
	resolverAddr := startTestDNSServer(t, func(domain string) ([]dnsAnswerRecord, bool) {
		if domain != "auth.4a.cmft" {
			return nil, false
		}
		return []dnsAnswerRecord{{value: "203.0.113.10", ttl: 60}}, true
	})

	controller := &fakePlatform{}
	dialer := newCompanyDomainDialer(
		logging.NewLogger(logging.NewRingBuffer(20)),
		controller,
		"utun4",
		[]string{resolverAddr},
		nil,
	)

	targets, err := dialer.PrepareDialTargets(context.Background(), "auth.4a.cmft:443")
	if err != nil {
		t.Fatalf("prepare dial targets failed: %v", err)
	}
	if len(targets) != 1 || targets[0] != "203.0.113.10:443" {
		t.Fatalf("unexpected targets: %+v", targets)
	}
	if got, want := controller.bypassInterface, "utun4"; got != want {
		t.Fatalf("expected bypass interface %q, got %q", want, got)
	}
	if len(controller.bypassRoutes) != 1 || controller.bypassRoutes[0] != "203.0.113.10/32" {
		t.Fatalf("unexpected bypass routes: %+v", controller.bypassRoutes)
	}
}

func TestCompanyDomainDialerSkipsFakeIPAnswers(t *testing.T) {
	fakeResolver := startTestDNSServer(t, func(domain string) ([]dnsAnswerRecord, bool) {
		if domain != "auth.4a.cmft" {
			return nil, false
		}
		return []dnsAnswerRecord{{value: "198.18.2.24", ttl: 60}}, true
	})
	realResolver := startTestDNSServer(t, func(domain string) ([]dnsAnswerRecord, bool) {
		if domain != "auth.4a.cmft" {
			return nil, false
		}
		return []dnsAnswerRecord{{value: "203.0.113.21", ttl: 60}}, true
	})

	controller := &fakePlatform{}
	dialer := newCompanyDomainDialer(
		logging.NewLogger(logging.NewRingBuffer(20)),
		controller,
		"utun4",
		[]string{fakeResolver, realResolver},
		nil,
	)

	targets, err := dialer.PrepareDialTargets(context.Background(), "auth.4a.cmft:443")
	if err != nil {
		t.Fatalf("prepare dial targets failed: %v", err)
	}
	if len(targets) != 1 || targets[0] != "203.0.113.21:443" {
		t.Fatalf("unexpected targets: %+v", targets)
	}
}

func TestCompanyDomainDialerRefreshReplacesExpiredRoutes(t *testing.T) {
	var (
		mu        sync.Mutex
		currentIP = "203.0.113.30"
	)
	resolverAddr := startTestDNSServer(t, func(domain string) ([]dnsAnswerRecord, bool) {
		if domain != "auth.4a.cmft" {
			return nil, false
		}
		mu.Lock()
		defer mu.Unlock()
		return []dnsAnswerRecord{{value: currentIP, ttl: 1}}, true
	})

	controller := &fakePlatform{}
	dialer := newCompanyDomainDialer(
		logging.NewLogger(logging.NewRingBuffer(20)),
		controller,
		"utun4",
		[]string{resolverAddr},
		nil,
	)
	dialer.refreshLead = 0

	if _, err := dialer.PrepareDialTargets(context.Background(), "auth.4a.cmft:443"); err != nil {
		t.Fatalf("prepare dial targets failed: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)

	mu.Lock()
	currentIP = "203.0.113.31"
	mu.Unlock()

	dialer.Refresh(context.Background())

	routes := dialer.DynamicRoutes()
	if len(routes) != 1 || routes[0] != "203.0.113.31/32" {
		t.Fatalf("unexpected refreshed routes: %+v", routes)
	}
	if len(controller.clearedBypass) != 1 || controller.clearedBypass[0] != "203.0.113.30/32" {
		t.Fatalf("expected old host route to be cleared, got %+v", controller.clearedBypass)
	}
}

type dnsAnswerRecord struct {
	value string
	ttl   uint32
}

func startTestDNSServer(t *testing.T, handler func(domain string) ([]dnsAnswerRecord, bool)) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}

	mux := mdns.NewServeMux()
	mux.HandleFunc(".", func(w mdns.ResponseWriter, req *mdns.Msg) {
		resp := new(mdns.Msg)
		resp.SetReply(req)
		for _, q := range req.Question {
			domain := strings.TrimSuffix(strings.ToLower(q.Name), ".")
			answers, ok := handler(domain)
			if !ok {
				continue
			}
			for _, answer := range answers {
				resp.Answer = append(resp.Answer, &mdns.A{
					Hdr: mdns.RR_Header{Name: q.Name, Rrtype: mdns.TypeA, Class: mdns.ClassINET, Ttl: answer.ttl},
					A:   net.ParseIP(answer.value).To4(),
				})
			}
		}
		_ = w.WriteMsg(resp)
	})

	server := &mdns.Server{PacketConn: pc, Handler: mux}
	go func() {
		_ = server.ActivateAndServe()
	}()

	t.Cleanup(func() {
		_ = server.Shutdown()
		_ = pc.Close()
	})

	return pc.LocalAddr().String()
}
