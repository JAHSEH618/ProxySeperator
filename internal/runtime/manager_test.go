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
	applied              bool
	cleared              bool
	stopTUNCalled        bool
	bypassInterface      string
	bypassRoutes         []string
	clearedBypass        []string
	recoveredSnapshot    api.RecoverySnapshot
	systemProxy          api.SystemProxyState
	defaultEgress        string
	validateTUNErr       error
	defaultEgressErr     error
	applySystemProxyErr  error
	recoverCalled        bool
	recoverErr           error
	capturedSnapshots    []api.RecoverySnapshot
	isDefaultRouteVPN    bool
	vpnInterface         string
}

func (f *fakePlatform) ApplySystemProxy(context.Context, platform.SystemProxyConfig) error {
	f.applied = true
	return f.applySystemProxyErr
}

func (f *fakePlatform) ClearSystemProxy(context.Context) error {
	f.cleared = true
	return nil
}

func (f *fakePlatform) PreferredCompanyBypassInterface(context.Context) (string, error) {
	if f.defaultEgress == "" {
		return "en0", nil
	}
	return f.defaultEgress, nil
}

func (f *fakePlatform) ApplyCompanyBypassRoutes(_ context.Context, iface string, routes []string) error {
	f.bypassInterface = iface
	f.bypassRoutes = append([]string(nil), routes...)
	return nil
}

func (f *fakePlatform) ClearCompanyBypassRoutes(_ context.Context, _ string, routes []string) error {
	f.clearedBypass = append([]string(nil), routes...)
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
func (f *fakePlatform) RecoverNetwork(_ context.Context, snapshot api.RecoverySnapshot) error {
	f.recoverCalled = true
	f.recoveredSnapshot = snapshot
	if snapshot.CompanyBypass.Interface != "" && len(snapshot.CompanyBypass.Routes) > 0 {
		f.clearedBypass = append([]string(nil), snapshot.CompanyBypass.Routes...)
	}
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
func (f *fakePlatform) IsDefaultRouteViaVPN(context.Context) (bool, string, error) {
	return f.isDefaultRouteVPN, f.vpnInterface, nil
}
func (f *fakePlatform) ValidateTUN(context.Context) error {
	return f.validateTUNErr
}
func (f *fakePlatform) StartTUN(context.Context, platform.TUNOptions) error { return nil }
func (f *fakePlatform) StopTUN(context.Context) error {
	f.stopTUNCalled = true
	return nil
}
func (f *fakePlatform) StopRouteHelper() {}

func TestManagerStartStopSystemMode(t *testing.T) {
	companyStub := startSOCKS5Stub(t)
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
	cfg.CompanyUpstream = upstreamFromAddress(t, companyStub)
	cfg.PersonalUpstream = personalUpstream

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
	if !controller.recoverCalled {
		t.Fatal("expected stop to restore network from recovery snapshot")
	}
	if controller.cleared {
		t.Fatal("expected stop not to fall back to clearing system proxy when snapshot restore succeeds")
	}
	if manager.journal.Exists() {
		t.Fatal("expected recovery journal to be removed after successful stop restore")
	}
}

func TestManagerStopFallsBackToClearSystemProxyWhenSnapshotRecoveryFails(t *testing.T) {
	companyStub := startSOCKS5Stub(t)
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
		recoverErr: api.NewError(api.ErrCodeRecoveryFailed, "restore failed"),
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
	cfg.CompanyUpstream = upstreamFromAddress(t, companyStub)
	cfg.PersonalUpstream = personalUpstream

	if _, err := manager.Start(cfg); err != nil {
		t.Fatalf("start failed: %v", err)
	}

	if err := manager.Stop(); err != nil {
		t.Fatalf("stop failed: %v", err)
	}
	if !controller.recoverCalled {
		t.Fatal("expected stop to attempt snapshot recovery before falling back")
	}
	if !controller.cleared {
		t.Fatal("expected stop to fall back to clearing system proxy when snapshot recovery fails")
	}
	if !manager.journal.Exists() {
		t.Fatal("expected recovery journal to be preserved after failed snapshot restore")
	}
	if !manager.Status().RecoveryRequired {
		t.Fatal("expected runtime status to require recovery after failed snapshot restore")
	}
	status := manager.Status()
	if status.LastErrorCode == "" {
		t.Fatal("expected error code to be preserved when snapshot recovery fails during stop")
	}
	if status.LastErrorMessage == "" {
		t.Fatal("expected error message to be preserved when snapshot recovery fails during stop")
	}
}

func TestManagerStartStopSystemModeWithPersonalTUNAppliesCompanyBypassRoutes(t *testing.T) {
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
	cfg.Advanced.PersonalTUNMode = true
	cfg.CompanyUpstream = upstreamFromAddress(t, companyStub)
	cfg.PersonalUpstream = upstreamFromAddress(t, personalStub)

	status, err := manager.Start(cfg)
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if status.State != api.RuntimeStateRunning {
		t.Fatalf("unexpected status: %+v", status)
	}
	if got, want := controller.bypassInterface, "en0"; got != want {
		t.Fatalf("expected bypass interface %q, got %q", want, got)
	}
	if len(controller.bypassRoutes) == 0 {
		t.Fatal("expected company bypass routes to be installed")
	}

	if err := manager.Stop(); err != nil {
		t.Fatalf("stop failed: %v", err)
	}
	if len(controller.clearedBypass) == 0 {
		t.Fatal("expected company bypass routes to be cleared")
	}
}

func TestManagerStopRestoresTUNSnapshotInsteadOfStoppingTUNDirectly(t *testing.T) {
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
	cfg.Advanced.Mode = api.ModeTUN
	cfg.Advanced.TUNEnabled = true
	cfg.CompanyUpstream = upstreamFromAddress(t, companyStub)
	cfg.PersonalUpstream = upstreamFromAddress(t, personalStub)

	if _, err := manager.Start(cfg); err != nil {
		t.Fatalf("start failed: %v", err)
	}

	if err := manager.Stop(); err != nil {
		t.Fatalf("stop failed: %v", err)
	}
	if !controller.recoverCalled {
		t.Fatal("expected TUN stop to restore network from recovery snapshot")
	}
	if controller.stopTUNCalled {
		t.Fatal("expected TUN stop not to fall back to StopTUN when snapshot restore succeeds")
	}
}

func TestManagerStartFailureAfterJournalUsesSnapshotRollback(t *testing.T) {
	companyStub := startSOCKS5Stub(t)
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
		applySystemProxyErr: api.NewError(api.ErrCodeSystemProxyFailed, "apply failed"),
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
	cfg.CompanyUpstream = upstreamFromAddress(t, companyStub)
	cfg.PersonalUpstream = personalUpstream

	_, err := manager.Start(cfg)
	if err == nil {
		t.Fatal("expected start to fail when ApplySystemProxy fails")
	}
	if !controller.recoverCalled {
		t.Fatal("expected rollback to use journal snapshot recovery")
	}
	if controller.cleared {
		t.Fatal("expected rollback not to fall back to ClearSystemProxy when snapshot restore succeeds")
	}
	if manager.journal.Exists() {
		t.Fatal("expected journal to be removed after successful snapshot rollback")
	}
	if manager.Status().State != api.RuntimeStateIdle {
		t.Fatalf("expected idle state after failed start, got %s", manager.Status().State)
	}
}

func TestRunPreflightBlocksPersonalTUNModeWithoutCompanyCIDRRoutes(t *testing.T) {
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
	cfg.Advanced.PersonalTUNMode = true
	cfg.PersonalUpstream = personalUpstream
	cfg.Rules = []string{".cmft"}

	report, err := manager.RunPreflight(cfg)
	if err != nil {
		t.Fatalf("preflight failed: %v", err)
	}
	if report.CanStart {
		t.Fatalf("expected preflight to fail without CIDR bypass routes, got %+v", report)
	}
	found := false
	for _, check := range report.Checks {
		if check.ID == checkCompanyBypassRoutes {
			found = true
			if check.Status != "fail" {
				t.Fatalf("expected bypass route check to fail, got %+v", check)
			}
		}
	}
	if !found {
		t.Fatalf("expected %s check in report, got %+v", checkCompanyBypassRoutes, report)
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

func TestRunPreflightStaysSystemWhenPersonalProxyOwnsSystemProxyAndCompanyUsesSystemRoute(t *testing.T) {
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
		t.Fatalf("expected preflight to pass, got %+v", report)
	}
	if report.EffectiveMode != api.ModeSystem {
		t.Fatalf("expected system mode (takeover), got %+v", report)
	}
}

func TestStartStaysSystemWhenPersonalProxyOwnsSystemProxyAndCompanyUsesSystemRoute(t *testing.T) {
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
		t.Fatalf("expected start to succeed in system mode (takeover), got %v", err)
	}
	if status.Mode != api.ModeSystem {
		t.Fatalf("expected system mode, got %+v", status)
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

func TestGuardSystemProxyReappliesWhenTampered(t *testing.T) {
	companyStub := startSOCKS5Stub(t)
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
	cfg.CompanyUpstream = upstreamFromAddress(t, companyStub)
	cfg.PersonalUpstream = personalUpstream

	if _, err := manager.Start(cfg); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer manager.Stop()

	// Simulate external tool changing system proxy.
	controller.applied = false
	controller.systemProxy = api.SystemProxyState{
		Enabled:      true,
		HTTPAddress:  "127.0.0.1:7890",
		HTTPSAddress: "127.0.0.1:7890",
	}

	// Run the guard check.
	manager.guardSystemProxy(context.Background())

	if !controller.applied {
		t.Fatal("expected guardSystemProxy to re-apply system proxy when tampered")
	}
}

func TestGuardSystemProxyNoopWhenCorrect(t *testing.T) {
	companyStub := startSOCKS5Stub(t)
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
	cfg.CompanyUpstream = upstreamFromAddress(t, companyStub)
	cfg.PersonalUpstream = personalUpstream

	if _, err := manager.Start(cfg); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer manager.Stop()

	// Set the system proxy to match what the manager expects.
	controller.systemProxy = api.SystemProxyState{
		Enabled:      true,
		HTTPAddress:  manager.httpListenAddr,
		HTTPSAddress: manager.httpListenAddr,
	}
	controller.applied = false

	manager.guardSystemProxy(context.Background())

	if controller.applied {
		t.Fatal("expected guardSystemProxy to be a no-op when proxy is correct")
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
