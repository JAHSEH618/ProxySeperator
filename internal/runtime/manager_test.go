package runtime

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
	"github.com/friedhelmliu/ProxySeperator/internal/logging"
	"github.com/friedhelmliu/ProxySeperator/internal/platform"
)

type fakePlatform struct {
	applied           bool
	cleared           bool
	systemProxy       api.SystemProxyState
	defaultEgress     string
	validateTUNErr    error
	defaultEgressErr  error
	recoverCalled     bool
	recoverErr        error
	capturedSnapshots []api.RecoverySnapshot
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
func (f *fakePlatform) CurrentSystemProxy(context.Context) (api.SystemProxyState, error) {
	return f.systemProxy, nil
}
func (f *fakePlatform) CurrentDNSResolvers(context.Context) ([]string, error) {
	return []string{"1.1.1.1:53"}, nil
}
func (f *fakePlatform) CaptureRecoverySnapshot(context.Context, string) (api.RecoverySnapshot, error) {
	snapshot := api.RecoverySnapshot{
		Platform:    "test",
		SystemProxy: f.systemProxy,
	}
	f.capturedSnapshots = append(f.capturedSnapshots, snapshot)
	return snapshot, nil
}
func (f *fakePlatform) RecoverNetwork(context.Context, api.RecoverySnapshot) error {
	f.recoverCalled = true
	return f.recoverErr
}
func (f *fakePlatform) DefaultEgressInterface(context.Context) (string, error) {
	if f.defaultEgressErr != nil {
		return "", f.defaultEgressErr
	}
	if f.defaultEgress == "" {
		return "en0", nil
	}
	return f.defaultEgress, nil
}
func (f *fakePlatform) ValidateTUN(context.Context) error {
	return f.validateTUNErr
}
func (f *fakePlatform) StartTUN(context.Context, platform.TUNOptions) error { return nil }
func (f *fakePlatform) StopTUN(context.Context) error                       { return nil }

func TestManagerStartStopSystemMode(t *testing.T) {
	companyStub := startSOCKS5Stub(t)
	personalStub := startSOCKS5Stub(t)

	logger := logging.NewLogger(logging.NewRingBuffer(50))
	controller := &fakePlatform{}
	manager := NewManagerWithOptions(logger, nil, Options{
		Platform:            controller,
		HTTPListenAddr:      "127.0.0.1:0",
		SOCKSListenAddr:     "127.0.0.1:0",
		DNSListenAddr:       "127.0.0.1:0",
		RecoveryJournalPath: t.TempDir() + "/recovery.json",
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

func TestRunPreflightAutoSwitchesToTUNOnSystemProxyConflict(t *testing.T) {
	companyStub := startSOCKS5Stub(t)
	personalStub := startSOCKS5Stub(t)

	logger := logging.NewLogger(logging.NewRingBuffer(50))
	controller := &fakePlatform{
		systemProxy: api.SystemProxyState{
			Enabled:      true,
			HTTPAddress:  "127.0.0.1:7890",
			HTTPSAddress: "127.0.0.1:7890",
			SOCKSAddress: "127.0.0.1:7890",
		},
		defaultEgress: "en0",
	}
	manager := NewManagerWithOptions(logger, nil, Options{
		Platform:            controller,
		HTTPListenAddr:      "127.0.0.1:17900",
		SOCKSListenAddr:     "127.0.0.1:17901",
		DNSListenAddr:       "127.0.0.1:0",
		RecoveryJournalPath: t.TempDir() + "/recovery.json",
	})

	cfg := api.DefaultConfig()
	cfg.Advanced.Mode = api.ModeSystem
	cfg.CompanyUpstream = upstreamFromAddress(t, companyStub)
	cfg.PersonalUpstream = upstreamFromAddress(t, personalStub)

	report, err := manager.RunPreflight(cfg)
	if err != nil {
		t.Fatalf("preflight failed: %v", err)
	}
	if !report.CanStart {
		t.Fatalf("expected preflight can start, got %+v", report)
	}
	if report.EffectiveMode != api.ModeTUN {
		t.Fatalf("expected TUN mode, got %+v", report)
	}
}

func TestRunPreflightUsesTUNWhenPersonalProxyOwnsSystemProxyAndCompanyUsesSystemRoute(t *testing.T) {
	personalStub := startSOCKS5Stub(t)
	personalUpstream := upstreamFromAddress(t, personalStub)

	logger := logging.NewLogger(logging.NewRingBuffer(50))
	controller := &fakePlatform{
		systemProxy: api.SystemProxyState{
			Enabled:      true,
			HTTPAddress:  personalUpstream.Address(),
			HTTPSAddress: personalUpstream.Address(),
			SOCKSAddress: personalUpstream.Address(),
		},
		defaultEgress: "utun8",
	}
	manager := NewManagerWithOptions(logger, nil, Options{
		Platform:            controller,
		HTTPListenAddr:      "127.0.0.1:17900",
		SOCKSListenAddr:     "127.0.0.1:17901",
		DNSListenAddr:       "127.0.0.1:0",
		RecoveryJournalPath: t.TempDir() + "/recovery.json",
	})

	cfg := api.DefaultConfig()
	cfg.Advanced.Mode = api.ModeSystem
	cfg.PersonalUpstream = personalUpstream

	report, err := manager.RunPreflight(cfg)
	if err != nil {
		t.Fatalf("preflight failed: %v", err)
	}
	if !report.CanStart {
		t.Fatalf("expected preflight to pass with TUN fallback, got %+v", report)
	}
	if report.EffectiveMode != api.ModeTUN {
		t.Fatalf("expected TUN mode, got %+v", report)
	}
}

func TestStartUsesTUNWhenPersonalProxyOwnsSystemProxyAndCompanyUsesSystemRoute(t *testing.T) {
	personalStub := startSOCKS5Stub(t)
	personalUpstream := upstreamFromAddress(t, personalStub)

	logger := logging.NewLogger(logging.NewRingBuffer(50))
	controller := &fakePlatform{
		systemProxy: api.SystemProxyState{
			Enabled:      true,
			HTTPAddress:  personalUpstream.Address(),
			HTTPSAddress: personalUpstream.Address(),
			SOCKSAddress: personalUpstream.Address(),
		},
		defaultEgress: "utun8",
	}
	manager := NewManagerWithOptions(logger, nil, Options{
		Platform:            controller,
		HTTPListenAddr:      "127.0.0.1:0",
		SOCKSListenAddr:     "127.0.0.1:0",
		DNSListenAddr:       "127.0.0.1:0",
		RecoveryJournalPath: t.TempDir() + "/recovery.json",
	})

	cfg := api.DefaultConfig()
	cfg.Advanced.Mode = api.ModeSystem
	cfg.PersonalUpstream = personalUpstream

	status, err := manager.Start(cfg)
	if err != nil {
		t.Fatalf("expected start to succeed with TUN fallback, got %v", err)
	}
	if status.Mode != api.ModeTUN {
		t.Fatalf("expected TUN mode, got %+v", status)
	}
	if !controller.cleared {
		t.Fatal("expected existing system proxy to be cleared before TUN mode")
	}
}

func TestRunPreflightBlocksWhenSystemProxyConflictAndTUNUnavailable(t *testing.T) {
	companyStub := startSOCKS5Stub(t)
	personalStub := startSOCKS5Stub(t)

	logger := logging.NewLogger(logging.NewRingBuffer(50))
	controller := &fakePlatform{
		systemProxy: api.SystemProxyState{
			Enabled:      true,
			HTTPAddress:  "127.0.0.1:7890",
			HTTPSAddress: "127.0.0.1:7890",
			SOCKSAddress: "127.0.0.1:7890",
		},
		validateTUNErr: api.NewError(api.ErrCodeTUNUnavailable, "missing wintun"),
	}
	manager := NewManagerWithOptions(logger, nil, Options{
		Platform:            controller,
		HTTPListenAddr:      "127.0.0.1:17900",
		SOCKSListenAddr:     "127.0.0.1:17901",
		DNSListenAddr:       "127.0.0.1:0",
		RecoveryJournalPath: t.TempDir() + "/recovery.json",
	})

	cfg := api.DefaultConfig()
	cfg.Advanced.Mode = api.ModeSystem
	cfg.CompanyUpstream = upstreamFromAddress(t, companyStub)
	cfg.PersonalUpstream = upstreamFromAddress(t, personalStub)

	report, err := manager.RunPreflight(cfg)
	if err != nil {
		t.Fatalf("preflight failed: %v", err)
	}
	if report.CanStart {
		t.Fatalf("expected preflight to fail, got %+v", report)
	}
}

func TestRunPreflightBlocksWhenAutoRecoveryFails(t *testing.T) {
	companyStub := startSOCKS5Stub(t)
	personalStub := startSOCKS5Stub(t)
	journalPath := t.TempDir() + "/recovery.json"

	logger := logging.NewLogger(logging.NewRingBuffer(50))
	controller := &fakePlatform{
		recoverErr: api.NewError(api.ErrCodeRecoveryFailed, "restore failed"),
	}
	manager := NewManagerWithOptions(logger, nil, Options{
		Platform:            controller,
		HTTPListenAddr:      "127.0.0.1:0",
		SOCKSListenAddr:     "127.0.0.1:0",
		DNSListenAddr:       "127.0.0.1:0",
		RecoveryJournalPath: journalPath,
	})

	if err := manager.journal.Save(api.RecoverySnapshot{Platform: "test", Mode: api.ModeSystem, WrittenAt: time.Now()}); err != nil {
		t.Fatalf("save journal: %v", err)
	}

	cfg := api.DefaultConfig()
	cfg.CompanyUpstream = upstreamFromAddress(t, companyStub)
	cfg.PersonalUpstream = upstreamFromAddress(t, personalStub)

	report, err := manager.RunPreflight(cfg)
	if err != nil {
		t.Fatalf("preflight failed: %v", err)
	}
	if !report.RecoveryRequired || report.CanStart {
		t.Fatalf("expected recovery to block start, got %+v", report)
	}
}

func TestRunPreflightDoesNotBlockOnRecoveryJournalWhileRuntimeIsActive(t *testing.T) {
	companyStub := startSOCKS5Stub(t)
	personalStub := startSOCKS5Stub(t)
	journalPath := t.TempDir() + "/recovery.json"

	logger := logging.NewLogger(logging.NewRingBuffer(50))
	controller := &fakePlatform{}
	manager := NewManagerWithOptions(logger, nil, Options{
		Platform:            controller,
		HTTPListenAddr:      "127.0.0.1:0",
		SOCKSListenAddr:     "127.0.0.1:0",
		DNSListenAddr:       "127.0.0.1:0",
		RecoveryJournalPath: journalPath,
	})

	if err := manager.journal.Save(api.RecoverySnapshot{Platform: "test", Mode: api.ModeSystem, WrittenAt: time.Now()}); err != nil {
		t.Fatalf("save journal: %v", err)
	}
	manager.status.State = api.RuntimeStateRunning

	cfg := api.DefaultConfig()
	cfg.CompanyUpstream = upstreamFromAddress(t, companyStub)
	cfg.PersonalUpstream = upstreamFromAddress(t, personalStub)

	report, err := manager.RunPreflight(cfg)
	if err != nil {
		t.Fatalf("preflight failed: %v", err)
	}
	if report.RecoveryRequired {
		t.Fatalf("expected no recovery requirement while runtime active, got %+v", report)
	}
	for _, check := range report.Checks {
		if check.ID == checkNetworkRecovery && check.Status == "fail" {
			t.Fatalf("expected recovery check not to fail, got %+v", report)
		}
	}
}

func TestRunPreflightAutoRecoversResidualNetworkState(t *testing.T) {
	companyStub := startSOCKS5Stub(t)
	personalStub := startSOCKS5Stub(t)
	journalPath := t.TempDir() + "/recovery.json"

	logger := logging.NewLogger(logging.NewRingBuffer(50))
	controller := &fakePlatform{}
	manager := NewManagerWithOptions(logger, nil, Options{
		Platform:            controller,
		HTTPListenAddr:      "127.0.0.1:0",
		SOCKSListenAddr:     "127.0.0.1:0",
		DNSListenAddr:       "127.0.0.1:0",
		RecoveryJournalPath: journalPath,
	})

	if err := manager.journal.Save(api.RecoverySnapshot{Platform: "test", Mode: api.ModeSystem, WrittenAt: time.Now()}); err != nil {
		t.Fatalf("save journal: %v", err)
	}

	cfg := api.DefaultConfig()
	cfg.CompanyUpstream = upstreamFromAddress(t, companyStub)
	cfg.PersonalUpstream = upstreamFromAddress(t, personalStub)

	report, err := manager.RunPreflight(cfg)
	if err != nil {
		t.Fatalf("preflight failed: %v", err)
	}
	if !report.AutoRecovered {
		t.Fatalf("expected auto recovery to be reported, got %+v", report)
	}
	if report.RecoveryRequired {
		t.Fatalf("expected residual network state to be auto-recovered, got %+v", report)
	}
	if !report.CanStart {
		t.Fatalf("expected preflight to pass after auto recovery, got %+v", report)
	}
	if !controller.recoverCalled {
		t.Fatal("expected platform recovery to be called")
	}
	if manager.journal.Exists() {
		t.Fatal("expected recovery journal to be removed after auto recovery")
	}
}

func TestStartAutoRecoversResidualNetworkStateBeforeStarting(t *testing.T) {
	companyStub := startSOCKS5Stub(t)
	personalStub := startSOCKS5Stub(t)
	journalPath := t.TempDir() + "/recovery.json"

	logger := logging.NewLogger(logging.NewRingBuffer(50))
	controller := &fakePlatform{}
	manager := NewManagerWithOptions(logger, nil, Options{
		Platform:            controller,
		HTTPListenAddr:      "127.0.0.1:0",
		SOCKSListenAddr:     "127.0.0.1:0",
		DNSListenAddr:       "127.0.0.1:0",
		RecoveryJournalPath: journalPath,
	})

	if err := manager.journal.Save(api.RecoverySnapshot{Platform: "test", Mode: api.ModeSystem, WrittenAt: time.Now()}); err != nil {
		t.Fatalf("save journal: %v", err)
	}

	cfg := api.DefaultConfig()
	cfg.CompanyUpstream = upstreamFromAddress(t, companyStub)
	cfg.PersonalUpstream = upstreamFromAddress(t, personalStub)

	status, err := manager.Start(cfg)
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if status.State != api.RuntimeStateRunning {
		t.Fatalf("expected runtime to be running after auto recovery, got %+v", status)
	}
	if !controller.recoverCalled {
		t.Fatal("expected platform recovery to be called before starting")
	}
}

func TestRunPreflightWarnsButDoesNotBlockWhenPersonalUpstreamUnreachable(t *testing.T) {
	logger := logging.NewLogger(logging.NewRingBuffer(50))
	controller := &fakePlatform{}
	manager := NewManagerWithOptions(logger, nil, Options{
		Platform:            controller,
		HTTPListenAddr:      "127.0.0.1:0",
		SOCKSListenAddr:     "127.0.0.1:0",
		DNSListenAddr:       "127.0.0.1:0",
		RecoveryJournalPath: t.TempDir() + "/recovery.json",
	})

	cfg := api.DefaultConfig()
	// 使用一个不可达的端口作为个人代理
	cfg.PersonalUpstream = api.UpstreamConfig{Host: "127.0.0.1", Port: 59999, Protocol: api.ProtocolSOCKS5}

	report, err := manager.RunPreflight(cfg)
	if err != nil {
		t.Fatalf("preflight failed: %v", err)
	}
	if !report.CanStart {
		t.Fatalf("expected preflight to still allow start (warn only), got canStart=false, checks: %+v", report.Checks)
	}
	found := false
	for _, check := range report.Checks {
		if check.ID == checkPersonalUpstream {
			if check.Status != "warn" {
				t.Fatalf("expected personal upstream check to be warn, got %s", check.Status)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("expected personal upstream check to be present")
	}
}

func TestStartBlocksWhenPersonalUpstreamUnreachable(t *testing.T) {
	logger := logging.NewLogger(logging.NewRingBuffer(50))
	controller := &fakePlatform{}
	manager := NewManagerWithOptions(logger, nil, Options{
		Platform:            controller,
		HTTPListenAddr:      "127.0.0.1:0",
		SOCKSListenAddr:     "127.0.0.1:0",
		DNSListenAddr:       "127.0.0.1:0",
		RecoveryJournalPath: t.TempDir() + "/recovery.json",
	})

	cfg := api.DefaultConfig()
	cfg.PersonalUpstream = api.UpstreamConfig{Host: "127.0.0.1", Port: 59999, Protocol: api.ProtocolSOCKS5}

	_, err := manager.Start(cfg)
	if err == nil {
		t.Fatal("expected start to fail when personal upstream is unreachable")
	}
	if api.ErrorCode(err) != api.ErrCodeUpstreamUnavailable {
		t.Fatalf("expected ERR_UPSTREAM_UNAVAILABLE, got %s: %v", api.ErrorCode(err), err)
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
