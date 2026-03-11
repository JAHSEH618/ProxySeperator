package runtime

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
	localdns "github.com/friedhelmliu/ProxySeperator/internal/dns"
	"github.com/friedhelmliu/ProxySeperator/internal/logging"
	"github.com/friedhelmliu/ProxySeperator/internal/platform"
	"github.com/friedhelmliu/ProxySeperator/internal/proxy"
	"github.com/friedhelmliu/ProxySeperator/internal/rules"
)

const (
	defaultHTTPProxyListen  = "127.0.0.1:17900"
	defaultSOCKSProxyListen = "127.0.0.1:17901"
	defaultDNSListen        = "127.0.0.1:18553"
	defaultRecoveryJournal  = "recovery-journal.json"
)

type Manager struct {
	mu      sync.Mutex
	logger  *logging.Logger
	emitter EventEmitter

	platform        platform.Controller
	httpListenAddr  string
	socksListenAddr string
	dnsListenAddr   string
	dnsCache        *localdns.Cache
	dns             *localdns.Server
	stats           *StatsTracker
	journal         *recoveryJournal

	forwarder *Forwarder
	httpProxy *proxy.HTTPServer
	socks5    *proxy.SOCKS5Server

	health api.HealthStatus
	status api.RuntimeStatus
	cfg    api.Config
	cancel context.CancelFunc
}

type Options struct {
	Platform            platform.Controller
	HTTPListenAddr      string
	SOCKSListenAddr     string
	DNSListenAddr       string
	RecoveryJournalPath string
}

func NewManager(logger *logging.Logger, emitter EventEmitter) *Manager {
	return NewManagerWithOptions(logger, emitter, Options{})
}

func NewManagerWithOptions(logger *logging.Logger, emitter EventEmitter, opts Options) *Manager {
	if emitter == nil {
		emitter = NewNopEmitter()
	}
	controller := opts.Platform
	if controller == nil {
		controller = platform.NewController(logger)
	}
	if opts.HTTPListenAddr == "" {
		opts.HTTPListenAddr = defaultHTTPProxyListen
	}
	if opts.SOCKSListenAddr == "" {
		opts.SOCKSListenAddr = defaultSOCKSProxyListen
	}
	if opts.DNSListenAddr == "" {
		opts.DNSListenAddr = defaultDNSListen
	}
	if opts.RecoveryJournalPath == "" {
		if dir, err := os.UserConfigDir(); err == nil {
			opts.RecoveryJournalPath = filepath.Join(dir, api.AppName, defaultRecoveryJournal)
		}
	}
	return &Manager{
		logger:          logger,
		emitter:         emitter,
		platform:        controller,
		httpListenAddr:  opts.HTTPListenAddr,
		socksListenAddr: opts.SOCKSListenAddr,
		dnsListenAddr:   opts.DNSListenAddr,
		dnsCache:        localdns.NewCache(),
		stats:           NewStatsTracker(),
		journal:         newRecoveryJournal(opts.RecoveryJournalPath),
		status: api.RuntimeStatus{
			State:         api.RuntimeStateIdle,
			Mode:          api.ModeSystem,
			RequestedMode: api.ModeSystem,
		},
	}
}

func (m *Manager) RunPreflight(cfg api.Config) (api.PreflightReport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cfg = cfg
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	state := m.evaluatePreflight(ctx, cfg)
	m.cfg = state.resolvedConfig
	m.health = state.health
	report := state.report
	m.applyPreflightToStatus(report)
	m.emitHealth()
	m.emitStatus()
	return report, nil
}

func (m *Manager) RecoverNetwork() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.status.State == api.RuntimeStateRunning || m.status.State == api.RuntimeStateStarting || m.status.State == api.RuntimeStateStopping {
		return api.NewError(api.ErrCodeRuntimeAlreadyRunning, "运行时正在运行，无法执行网络恢复")
	}
	if !m.journal.Exists() {
		m.status.RecoveryRequired = false
		m.status.LastErrorCode = ""
		m.status.LastErrorMessage = ""
		m.emitStatus()
		return nil
	}

	snapshot, err := m.journal.Load()
	if err != nil {
		m.status.RecoveryRequired = true
		m.status.LastErrorCode = api.ErrorCode(err)
		m.status.LastErrorMessage = err.Error()
		m.emitStatus()
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.platform.RecoverNetwork(ctx, snapshot); err != nil {
		wrapped := api.WrapError(api.ErrCodeRecoveryFailed, "恢复系统网络状态失败", err)
		m.status.RecoveryRequired = true
		m.status.LastErrorCode = wrapped.Code
		m.status.LastErrorMessage = wrapped.Error()
		m.emitStatus()
		return wrapped
	}
	if err := m.journal.Remove(); err != nil {
		m.status.RecoveryRequired = true
		m.status.LastErrorCode = api.ErrorCode(err)
		m.status.LastErrorMessage = err.Error()
		m.emitStatus()
		return err
	}

	m.status = api.RuntimeStatus{
		State:         api.RuntimeStateIdle,
		Mode:          requestedMode(m.cfg),
		RequestedMode: requestedMode(m.cfg),
	}
	m.emitStatus()
	return nil
}

func (m *Manager) Start(cfg api.Config) (api.RuntimeStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.status.State == api.RuntimeStateRunning || m.status.State == api.RuntimeStateStarting {
		return m.status, api.NewError(api.ErrCodeRuntimeAlreadyRunning, "运行时已经启动")
	}

	m.cfg = cfg
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	state := m.evaluatePreflight(ctx, cfg)
	m.cfg = state.resolvedConfig
	m.health = state.health
	report := state.report
	m.applyPreflightToStatus(report)
	m.emitHealth()
	m.emitStatus()

	if !report.CanStart {
		m.status.State = api.RuntimeStateIdle
		m.status.LastErrorCode = api.ErrCodePreflightFailed
		m.status.LastErrorMessage = firstFailureMessage(report.Checks)
		m.emitStatus()
		return m.status, api.NewError(api.ErrCodePreflightFailed, m.status.LastErrorMessage)
	}

	snapshot, err := m.platform.CaptureRecoverySnapshot(ctx, report.EffectiveMode)
	if err != nil {
		wrapped := api.WrapError(api.ErrCodeRecoveryFailed, "写入恢复快照前读取系统状态失败", err)
		m.status.State = api.RuntimeStateIdle
		m.status.LastErrorCode = wrapped.Code
		m.status.LastErrorMessage = wrapped.Error()
		m.emitStatus()
		return m.status, wrapped
	}
	snapshot.Mode = report.EffectiveMode
	snapshot.WrittenAt = time.Now()
	if err := m.journal.Save(snapshot); err != nil {
		m.status.State = api.RuntimeStateIdle
		m.status.LastErrorCode = api.ErrorCode(err)
		m.status.LastErrorMessage = err.Error()
		m.emitStatus()
		return m.status, err
	}

	m.status = api.RuntimeStatus{
		State:         api.RuntimeStateStarting,
		Mode:          report.EffectiveMode,
		RequestedMode: report.RequestedMode,
		ModeReason:    report.ModeReason,
	}
	m.emitStatus()

	matcher := rules.NewMatcher(rules.ParseLines(m.cfg.Rules).Compiled)
	m.forwarder = NewForwarder(m.cfg, matcher, m.dnsCache, m.stats, m.logger)
	m.httpProxy = proxy.NewHTTPServer(m.httpListenAddr, m.forwarder, m.logger)
	m.socks5 = proxy.NewSOCKS5Server(m.socksListenAddr, m.forwarder, m.logger)

	runCtx, runCancel := context.WithCancel(context.Background())
	m.cancel = runCancel

	dnsResolvers := []string(nil)
	if report.EffectiveMode == api.ModeTUN {
		resolvers, err := m.platform.CurrentDNSResolvers(runCtx)
		if err != nil {
			m.logger.Warn("runtime", "读取系统 DNS 失败，回退到默认公共解析器", map[string]any{"error": err.Error()})
		} else {
			dnsResolvers = resolvers
		}
	}
	m.dns = localdns.NewServer(m.dnsListenAddr, m.dnsCache, dnsResolvers, m.logger)

	m.stats.Start(report.EffectiveMode)
	if err := m.httpProxy.Start(runCtx); err != nil {
		m.rollbackLocked(runCtx, false)
		m.status.LastErrorCode = api.ErrorCode(err)
		m.status.LastErrorMessage = err.Error()
		m.emitStatus()
		return m.status, err
	}
	if err := m.socks5.Start(runCtx); err != nil {
		m.rollbackLocked(runCtx, false)
		m.status.LastErrorCode = api.ErrorCode(err)
		m.status.LastErrorMessage = err.Error()
		m.emitStatus()
		return m.status, err
	}

	if report.EffectiveMode == api.ModeTUN {
		if err := m.dns.Start(); err != nil {
			m.rollbackLocked(runCtx, false)
			wrapped := api.WrapError(api.ErrCodeRuntimeStartFailed, "启动本地 DNS 失败", err)
			m.status.LastErrorCode = wrapped.Code
			m.status.LastErrorMessage = wrapped.Error()
			m.emitStatus()
			return m.status, wrapped
		}
		egress, err := m.platform.DefaultEgressInterface(runCtx)
		if err != nil {
			m.rollbackLocked(runCtx, false)
			wrapped := api.WrapError(api.ErrCodeTUNUnavailable, "无法识别默认出口接口", err)
			m.status.LastErrorCode = wrapped.Code
			m.status.LastErrorMessage = wrapped.Error()
			m.emitStatus()
			return m.status, wrapped
		}
		if err := m.platform.StartTUN(runCtx, platform.TUNOptions{
			DNSListenAddress:   m.dnsListenAddr,
			SOCKSListenAddress: m.socksListenAddr,
			EgressInterface:    egress,
			MTU:                1500,
		}); err != nil {
			m.rollbackLocked(runCtx, false)
			m.status.LastErrorCode = api.ErrorCode(err)
			m.status.LastErrorMessage = err.Error()
			m.emitStatus()
			return m.status, err
		}

		if live, err := m.platform.CaptureRecoverySnapshot(runCtx, report.EffectiveMode); err == nil {
			snapshot.TUNState = live.TUNState
			snapshot.WrittenAt = time.Now()
			_ = m.journal.Save(snapshot)
		}
	} else {
		if err := m.platform.ApplySystemProxy(runCtx, m.systemProxyConfig()); err != nil {
			m.rollbackLocked(runCtx, false)
			m.status.LastErrorCode = api.ErrorCode(err)
			m.status.LastErrorMessage = err.Error()
			m.emitStatus()
			return m.status, err
		}
	}

	if m.cfg.Advanced.AutoStart {
		if exe, err := os.Executable(); err == nil {
			_ = m.platform.EnableAutoStart(runCtx, exe)
		}
	}

	now := time.Now()
	m.status = api.RuntimeStatus{
		State:         api.RuntimeStateRunning,
		Mode:          report.EffectiveMode,
		RequestedMode: report.RequestedMode,
		ModeReason:    report.ModeReason,
		StartedAt:     &now,
		UptimeSeconds: 0,
	}
	m.emitStatus()
	m.emitTraffic()
	go m.backgroundLoop(runCtx)
	return m.status, nil
}

func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.status.State == api.RuntimeStateIdle {
		return api.NewError(api.ErrCodeRuntimeNotRunning, "运行时未启动")
	}
	m.status.State = api.RuntimeStateStopping
	m.emitStatus()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m.rollbackLocked(ctx, true)
	m.status.LastErrorCode = ""
	m.status.LastErrorMessage = ""
	return nil
}

func (m *Manager) Restart(cfg api.Config) (api.RuntimeStatus, error) {
	if err := m.Stop(); err != nil && api.ErrorCode(err) != api.ErrCodeRuntimeNotRunning {
		return m.status, err
	}
	return m.Start(cfg)
}

func (m *Manager) Status() api.RuntimeStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.State == api.RuntimeStateRunning && m.status.StartedAt != nil {
		m.status.UptimeSeconds = int64(time.Since(*m.status.StartedAt).Seconds())
	}
	return m.status
}

func (m *Manager) Health() api.HealthStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.health
}

func (m *Manager) Traffic() api.TrafficStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stats.Snapshot(m.status.Mode)
}

func (m *Manager) TestRoute(input string) api.RouteTestResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.forwarder == nil {
		parseResult := rules.ParseLines(m.cfg.Rules)
		m.forwarder = NewForwarder(m.cfg, rules.NewMatcher(parseResult.Compiled), m.dnsCache, m.stats, m.logger)
	}
	return m.forwarder.TestRoute(input)
}

func (m *Manager) applyPreflightToStatus(report api.PreflightReport) {
	m.status.RequestedMode = report.RequestedMode
	m.status.Mode = report.EffectiveMode
	m.status.ModeReason = report.ModeReason
	m.status.RecoveryRequired = report.RecoveryRequired
	if !report.RecoveryRequired {
		m.status.LastErrorCode = ""
		m.status.LastErrorMessage = ""
	}
}

func (m *Manager) rollbackLocked(ctx context.Context, clearJournal bool) {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	if m.httpProxy != nil {
		_ = m.httpProxy.Stop(ctx)
		m.httpProxy = nil
	}
	if m.socks5 != nil {
		_ = m.socks5.Stop()
		m.socks5 = nil
	}
	if m.status.Mode == api.ModeTUN {
		if m.dns != nil {
			_ = m.dns.Stop(ctx)
		}
		_ = m.platform.StopTUN(ctx)
	} else {
		_ = m.platform.ClearSystemProxy(ctx)
	}
	m.dns = nil
	m.dnsCache.Clear()
	m.stats.Stop()
	if clearJournal {
		_ = m.journal.Remove()
	}
	mode := m.status.Mode
	requested := m.status.RequestedMode
	recoveryRequired := clearJournal && m.journal.Exists()
	m.status = api.RuntimeStatus{
		State:            api.RuntimeStateIdle,
		Mode:             mode,
		RequestedMode:    requested,
		RecoveryRequired: recoveryRequired,
	}
	m.emitStatus()
}

func (m *Manager) backgroundLoop(ctx context.Context) {
	healthTicker := time.NewTicker(5 * time.Second)
	trafficTicker := time.NewTicker(1 * time.Second)
	defer healthTicker.Stop()
	defer trafficTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-healthTicker.C:
			health := m.forwarder.RefreshHealth(ctx)
			m.mu.Lock()
			m.health = health
			m.mu.Unlock()
			m.emitHealth()
		case <-trafficTicker.C:
			m.emitTraffic()
			m.emitStatus()
		}
	}
}

func (m *Manager) emitStatus() {
	status := m.status
	if status.State == api.RuntimeStateRunning && status.StartedAt != nil {
		status.UptimeSeconds = int64(time.Since(*status.StartedAt).Seconds())
	}
	m.emitter.Emit(api.EventRuntimeStatus, status)
}

func (m *Manager) emitHealth() {
	m.emitter.Emit(api.EventRuntimeHealth, m.health)
}

func (m *Manager) emitTraffic() {
	m.emitter.Emit(api.EventRuntimeTraffic, m.stats.Snapshot(m.status.Mode))
}

func (m *Manager) systemProxyConfig() platform.SystemProxyConfig {
	return platform.SystemProxyConfig{
		HTTPAddress:  m.httpListenAddr,
		HTTPSAddress: m.httpListenAddr,
		SOCKSAddress: m.socksListenAddr,
	}
}
