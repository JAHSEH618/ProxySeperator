package runtime

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
	"github.com/friedhelmliu/ProxySeperator/internal/logging"
	"github.com/friedhelmliu/ProxySeperator/internal/platform"
)

type fakePlatform struct {
	applied bool
	cleared bool
}

func (f *fakePlatform) ApplySystemProxy(context.Context, platform.SystemProxyConfig) error {
	f.applied = true
	return nil
}

func (f *fakePlatform) ClearSystemProxy(context.Context) error {
	f.cleared = true
	return nil
}

func (f *fakePlatform) EnableAutoStart(context.Context, string) error { return nil }
func (f *fakePlatform) DisableAutoStart(context.Context) error        { return nil }
func (f *fakePlatform) CurrentDNSResolvers(context.Context) ([]string, error) {
	return []string{"1.1.1.1:53"}, nil
}
func (f *fakePlatform) StartTUN(context.Context, platform.TUNOptions) error { return nil }
func (f *fakePlatform) StopTUN(context.Context) error                       { return nil }

func TestManagerStartStopSystemMode(t *testing.T) {
	companyStub := startSOCKS5Stub(t)
	personalStub := startSOCKS5Stub(t)

	logger := logging.NewLogger(logging.NewRingBuffer(50))
	controller := &fakePlatform{}
	manager := NewManagerWithOptions(logger, nil, Options{
		Platform:        controller,
		HTTPListenAddr:  "127.0.0.1:0",
		SOCKSListenAddr: "127.0.0.1:0",
		DNSListenAddr:   "127.0.0.1:0",
	})

	cfg := api.DefaultConfig()
	cfg.Advanced.Mode = api.ModeSystem
	cfg.CompanyUpstream = upstreamFromAddress(t, companyStub)
	cfg.PersonalUpstream = upstreamFromAddress(t, personalStub)

	status, err := manager.Start(cfg)
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if status.State != api.RuntimeStateRunning {
		t.Fatalf("unexpected status: %+v", status)
	}
	if !controller.applied {
		t.Fatal("expected system proxy to be applied")
	}

	if err := manager.Stop(); err != nil {
		t.Fatalf("stop failed: %v", err)
	}
	if !controller.cleared {
		t.Fatal("expected system proxy to be cleared")
	}
}

func upstreamFromAddress(t *testing.T, address string) api.UpstreamConfig {
	t.Helper()
	host, portString, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	var port int
	_, err = fmt.Sscanf(portString, "%d", &port)
	if err != nil {
		t.Fatal(err)
	}
	return api.UpstreamConfig{Host: host, Port: port, Protocol: api.ProtocolSOCKS5}
}
