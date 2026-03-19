package app

import (
	"context"
	"testing"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
	"github.com/friedhelmliu/ProxySeperator/internal/logging"
	"github.com/friedhelmliu/ProxySeperator/internal/platform"
	runtimeapp "github.com/friedhelmliu/ProxySeperator/internal/runtime"
)

type fakePlatformController struct {
	applied       bool
	cleared       bool
	bypassCleared bool
	recoverCalled bool
}

func (f *fakePlatformController) ApplySystemProxy(context.Context, platform.SystemProxyConfig) error {
	f.applied = true
	return nil
}

func (f *fakePlatformController) ClearSystemProxy(context.Context) error {
	f.cleared = true
	return nil
}

func (f *fakePlatformController) PreferredCompanyBypassInterface(context.Context) (string, error) {
	return "en0", nil
}

func (f *fakePlatformController) ApplyCompanyBypassRoutes(context.Context, string, []string) error {
	return nil
}

func (f *fakePlatformController) ClearCompanyBypassRoutes(context.Context, string, []string) error {
	f.bypassCleared = true
	return nil
}

func (f *fakePlatformController) EnableAutoStart(context.Context, string) error { return nil }
func (f *fakePlatformController) DisableAutoStart(context.Context) error        { return nil }
func (f *fakePlatformController) CurrentSystemProxy(context.Context) (api.SystemProxyState, error) {
	return api.SystemProxyState{}, nil
}
func (f *fakePlatformController) CurrentDNSResolvers(context.Context) ([]string, error) {
	return []string{"1.1.1.1:53"}, nil
}
func (f *fakePlatformController) CaptureRecoverySnapshot(context.Context, string) (api.RecoverySnapshot, error) {
	return api.RecoverySnapshot{Platform: "test"}, nil
}
func (f *fakePlatformController) RecoverNetwork(context.Context, api.RecoverySnapshot) error {
	f.recoverCalled = true
	return nil
}
func (f *fakePlatformController) DefaultEgressInterface(context.Context) (string, error) {
	return "en0", nil
}
func (f *fakePlatformController) ValidateTUN(context.Context) error { return nil }
func (f *fakePlatformController) StartTUN(context.Context, platform.TUNOptions) error {
	return nil
}
func (f *fakePlatformController) StopTUN(context.Context) error { return nil }
func (f *fakePlatformController) StopRouteHelper()              {}
func (f *fakePlatformController) IsDefaultRouteViaVPN(context.Context) (bool, string, error) {
	return false, "", nil
}

func TestOnShutdownStopsRunningRuntime(t *testing.T) {
	logger := logging.NewLogger(logging.NewRingBuffer(50))
	controller := &fakePlatformController{}
	manager := runtimeapp.NewManagerWithOptions(logger, nil, runtimeapp.Options{
		Platform:            controller,
		HTTPListenAddr:      "127.0.0.1:0",
		SOCKSListenAddr:     "127.0.0.1:0",
		DNSListenAddr:       "127.0.0.1:0",
		RecoveryJournalPath: t.TempDir() + "/recovery.json",
	})

	cfg := api.DefaultConfig()
	cfg.Advanced.Mode = api.ModeSystem
	cfg.CompanyUpstream = api.UpstreamConfig{Host: "127.0.0.1", Port: 1, Protocol: api.ProtocolDirect}
	cfg.PersonalUpstream = api.UpstreamConfig{Host: "127.0.0.1", Port: 2, Protocol: api.ProtocolDirect}

	service := &BackendAPI{
		logger:  logger,
		manager: manager,
		cfg:     cfg,
	}

	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if !controller.applied {
		t.Fatal("expected system proxy to be applied")
	}

	if err := service.OnShutdown(context.Background()); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}
	if !controller.recoverCalled {
		t.Fatal("expected shutdown to restore network from recovery snapshot")
	}
	if controller.cleared {
		t.Fatal("expected shutdown not to fall back to clearing system proxy when snapshot restore succeeds")
	}
}
