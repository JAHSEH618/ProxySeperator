package runtime

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

	companyBypassInterface string
	companyBypassRoutes    []string
	companyDomainDialer    *companyDomainDialer
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

	autoRecovered, err := m.tryAutoRecoverLocked(ctx, cfg)
	if err != nil {
		m.logger.Warn("runtime", "自动恢复残留网络状态失败", map[string]any{"error": err.Error()})
	}

	state := m.evaluatePreflight(ctx, cfg)
	m.cfg = state.resolvedConfig
	m.health = state.health
	report := state.report
	if autoRecovered {
		report.AutoRecovered = true
		report.RecoveryMessage = "检测到上次退出遗留的网络状态，已自动恢复系统代理和 DNS"
	}
	m.applyPreflightToStatus(report)
	m.emitHealth()
	m.emitStatus()
	return report, nil
}

func (m *Manager) EnsureRecovered(cfg api.Config) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return m.tryAutoRecoverLocked(ctx, cfg)
}

func (m *Manager) RecoverNetwork() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.status.State == api.RuntimeStateRunning || m.status.State == api.RuntimeStateStarting || m.status.State == api.RuntimeStateStopping {
		return api.NewError(api.ErrCodeRuntimeAlreadyRunning, "运行时正在运行，无法执行网络恢复")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := m.recoverNetworkLocked(ctx, m.cfg)
	if err != nil {
		return err
	}
	// 恢复成功后清除残留的错误状态，确保前端不再显示旧错误
	m.status.LastErrorCode = ""
	m.status.LastErrorMessage = ""
	m.status.RecoveryRequired = false
	m.emitStatus()
	return nil
}

// ForceRecoverNetwork forces a network recovery regardless of runtime state.
// If the runtime is active, it stops it first, then recovers from the journal.
// If no journal exists, falls back to a bare system proxy cleanup.
// This is the "nuclear button" for the tray menu.
func (m *Manager) ForceRecoverNetwork() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// If runtime is active, stop it first.
	if m.status.State == api.RuntimeStateRunning || m.status.State == api.RuntimeStateStarting {
		m.status.State = api.RuntimeStateStopping
		m.emitStatus()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		m.rollbackLocked(stopCtx)
		stopCancel()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Try journal-based recovery if journal still exists.
	if m.journal.Exists() {
		if _, err := m.recoverNetworkLocked(ctx, m.cfg); err != nil {
			// Journal recovery failed — fall back to bare cleanup.
			m.logger.Warn("runtime", "强制恢复：快照恢复失败，执行基础网络清理", map[string]any{"error": err.Error()})
			_ = m.platform.ClearSystemProxy(ctx)
			_ = m.journal.Remove()
		}
	} else {
		// No journal — bare cleanup as last resort.
		_ = m.platform.ClearSystemProxy(ctx)
	}

	m.status.LastErrorCode = ""
	m.status.LastErrorMessage = ""
	m.status.RecoveryRequired = false
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

	if _, err := m.tryAutoRecoverLocked(ctx, cfg); err != nil {
		m.logger.Warn("runtime", "启动前自动恢复残留网络状态失败", map[string]any{"error": err.Error()})
	}

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

	// 启动时必须确保个人代理已就绪（预检仅 warn，此处硬检查）
	if cfg.PersonalUpstream.Protocol != api.ProtocolDirect {
		personalRecheck := ProbeUpstream(ctx, cfg.PersonalUpstream)
		if !personalRecheck.Reachable {
			m.status.State = api.RuntimeStateIdle
			m.status.LastErrorCode = api.ErrCodeUpstreamUnavailable
			m.status.LastErrorMessage = "个人代理端口不可达，请先启动个人 VPN"
			m.emitStatus()
			return m.status, api.NewError(api.ErrCodeUpstreamUnavailable, m.status.LastErrorMessage)
		}
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
	m.companyBypassInterface = ""
	m.companyBypassRoutes = nil
	m.companyDomainDialer = nil
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
		m.rollbackLocked(runCtx)
		m.status.LastErrorCode = api.ErrorCode(err)
		m.status.LastErrorMessage = err.Error()
		m.emitStatus()
		return m.status, err
	}
	if err := m.socks5.Start(runCtx); err != nil {
		m.rollbackLocked(runCtx)
		m.status.LastErrorCode = api.ErrorCode(err)
		m.status.LastErrorMessage = err.Error()
		m.emitStatus()
		return m.status, err
	}

	if report.EffectiveMode == api.ModeTUN {
		if err := m.dns.Start(); err != nil {
			m.rollbackLocked(runCtx)
			wrapped := api.WrapError(api.ErrCodeRuntimeStartFailed, "启动本地 DNS 失败", err)
			m.status.LastErrorCode = wrapped.Code
			m.status.LastErrorMessage = wrapped.Error()
			m.emitStatus()
			return m.status, wrapped
		}
		egress, err := m.platform.DefaultEgressInterface(runCtx)
		if err != nil {
			m.rollbackLocked(runCtx)
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
			m.rollbackLocked(runCtx)
			m.status.LastErrorCode = api.ErrorCode(err)
			m.status.LastErrorMessage = err.Error()
			m.emitStatus()
			return m.status, err
		}
		if snapshot.SystemProxy.Enabled {
			if err := m.platform.ClearSystemProxy(runCtx); err != nil {
				m.rollbackLocked(runCtx)
				wrapped := api.WrapError(api.ErrCodeSystemProxyFailed, "切换到 TUN 模式前清理系统代理失败", err)
				m.status.LastErrorCode = wrapped.Code
				m.status.LastErrorMessage = wrapped.Error()
				m.emitStatus()
				return m.status, wrapped
			}
		}

		if live, err := m.platform.CaptureRecoverySnapshot(runCtx, report.EffectiveMode); err == nil {
			snapshot.TUNState = live.TUNState
			snapshot.WrittenAt = time.Now()
			_ = m.journal.Save(snapshot)
		}
	} else {
		if m.cfg.Advanced.PersonalTUNMode {
			bypassRoutes := companyBypassCIDRs(m.cfg)
			if len(bypassRoutes) > 0 {
				iface, err := m.platform.PreferredCompanyBypassInterface(runCtx)
				if err != nil {
					m.rollbackLocked(runCtx)
					wrapped := api.WrapError(api.ErrCodeSystemProxyFailed, "无法识别公司旁路接口", err)
					m.status.LastErrorCode = wrapped.Code
					m.status.LastErrorMessage = wrapped.Error()
					m.emitStatus()
					return m.status, wrapped
				}
				if err := m.platform.ApplyCompanyBypassRoutes(runCtx, iface, bypassRoutes); err != nil {
					m.rollbackLocked(runCtx)
					m.status.LastErrorCode = api.ErrorCode(err)
					m.status.LastErrorMessage = err.Error()
					m.emitStatus()
					return m.status, err
				}
				m.companyBypassInterface = iface
				m.companyBypassRoutes = append([]string(nil), bypassRoutes...)
				snapshot.CompanyBypass = api.CompanyBypassState{
					Interface: iface,
					Routes:    append([]string(nil), bypassRoutes...),
				}
				snapshot.WrittenAt = time.Now()
				_ = m.journal.Save(snapshot)
			}
			resolvers := []string(nil)
			if currentResolvers, err := m.platform.CurrentDNSResolvers(runCtx); err == nil {
				resolvers = append(resolvers, currentResolvers...)
			} else {
				m.logger.Warn("runtime", "读取当前 DNS 解析器失败，将仅使用本地回环解析器做公司域名解析", map[string]any{"error": err.Error()})
			}
			m.companyDomainDialer = newCompanyDomainDialer(
				m.logger,
				m.platform,
				m.companyBypassInterface,
				resolvers,
				func(dynamicRoutes []string) {
					if !m.journal.Exists() {
						return
					}
					snapshot, err := m.journal.Load()
					if err != nil {
						return
					}
					snapshot.CompanyBypass = api.CompanyBypassState{
						Interface: m.companyBypassInterface,
						Routes:    mergeRouteLists(m.companyBypassRoutes, dynamicRoutes),
					}
					snapshot.WrittenAt = time.Now()
					_ = m.journal.Save(snapshot)
				},
			)
			m.forwarder.SetCompanyDialPreparer(m.companyDomainDialer)
		}
		if err := m.platform.ApplySystemProxy(runCtx, m.systemProxyConfig()); err != nil {
			m.rollbackLocked(runCtx)
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
	m.rollbackLocked(ctx)
	if !m.status.RecoveryRequired {
		m.status.LastErrorCode = ""
		m.status.LastErrorMessage = ""
	}
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

func (m *Manager) tryAutoRecoverLocked(ctx context.Context, cfg api.Config) (bool, error) {
	if runtimeActive(m.status.State) || !m.journal.Exists() {
		return false, nil
	}
	recovered, err := m.recoverNetworkLocked(ctx, cfg)
	if err != nil {
		return false, err
	}
	if recovered {
		m.logger.Info("runtime", "检测到残留网络状态并已自动恢复", nil)
	}
	return recovered, nil
}

func (m *Manager) recoverNetworkLocked(ctx context.Context, cfg api.Config) (bool, error) {
	if !m.journal.Exists() {
		m.status.RecoveryRequired = false
		m.status.LastErrorCode = ""
		m.status.LastErrorMessage = ""
		m.companyBypassInterface = ""
		m.companyBypassRoutes = nil
		m.companyDomainDialer = nil
		m.emitStatus()
		return false, nil
	}

	snapshot, err := m.journal.Load()
	if err != nil {
		m.status.RecoveryRequired = true
		m.status.LastErrorCode = api.ErrorCode(err)
		m.status.LastErrorMessage = err.Error()
		m.emitStatus()
		return false, err
	}

	if err := m.platform.RecoverNetwork(ctx, snapshot); err != nil {
		wrapped := api.WrapError(api.ErrCodeRecoveryFailed, "恢复系统网络状态失败", err)
		m.status.RecoveryRequired = true
		m.status.LastErrorCode = wrapped.Code
		m.status.LastErrorMessage = wrapped.Error()
		m.emitStatus()
		return false, wrapped
	}
	if err := m.journal.Remove(); err != nil {
		m.status.RecoveryRequired = true
		m.status.LastErrorCode = api.ErrorCode(err)
		m.status.LastErrorMessage = err.Error()
		m.emitStatus()
		return false, err
	}

	mode := requestedMode(cfg)
	m.status = api.RuntimeStatus{
		State:         api.RuntimeStateIdle,
		Mode:          mode,
		RequestedMode: mode,
	}
	m.companyBypassInterface = ""
	m.companyBypassRoutes = nil
	m.companyDomainDialer = nil
	m.emitStatus()
	return true, nil
}

func (m *Manager) rollbackLocked(_ context.Context) {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}

	// Each cleanup step gets its own independent timeout so that one
	// blocking step (e.g. TUN auth dialog) cannot starve the others.

	if m.httpProxy != nil {
		proxyCtx, proxyCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = m.httpProxy.Stop(proxyCtx)
		proxyCancel()
		m.httpProxy = nil
	}
	if m.socks5 != nil {
		_ = m.socks5.Stop()
		m.socks5 = nil
	}

	if m.dns != nil {
		dnsCtx, dnsCancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := m.dns.Stop(dnsCtx); err != nil {
			m.logger.Warn("runtime", "停止 DNS 服务失败", map[string]any{"error": err.Error()})
		}
		dnsCancel()
	}

	recoveredFromJournal := false
	var rollbackErr error
	if m.journal.Exists() {
		recoverCtx, recoverCancel := context.WithTimeout(context.Background(), 5*time.Second)
		snapshot, err := m.journal.Load()
		if err != nil {
			rollbackErr = err
			m.logger.Warn("runtime", "读取恢复快照失败，回退到基础网络清理", map[string]any{
				"error": err.Error(),
			})
		} else if err := m.platform.RecoverNetwork(recoverCtx, snapshot); err != nil {
			rollbackErr = err
			m.logger.Warn("runtime", "按恢复快照还原网络失败，回退到基础网络清理", map[string]any{
				"error": err.Error(),
				"mode":  snapshot.Mode,
			})
		} else {
			recoveredFromJournal = true
			if err := m.journal.Remove(); err != nil {
				m.logger.Warn("runtime", "删除恢复快照失败", map[string]any{
					"error": err.Error(),
				})
			}
		}
		recoverCancel()
	}

	if !recoveredFromJournal && m.status.Mode == api.ModeTUN {
		tunCtx, tunCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := m.platform.StopTUN(tunCtx); err != nil {
			m.logger.Warn("runtime", "停止 TUN 失败", map[string]any{"error": err.Error()})
		}
		tunCancel()
	} else if !recoveredFromJournal {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 2*time.Second)
		routes := append([]string(nil), m.companyBypassRoutes...)
		if m.companyDomainDialer != nil {
			routes = mergeRouteLists(routes, m.companyDomainDialer.DynamicRoutes())
		}
		if m.companyBypassInterface != "" && len(routes) > 0 {
			if err := m.platform.ClearCompanyBypassRoutes(cleanCtx, m.companyBypassInterface, routes); err != nil {
				m.logger.Warn("runtime", "清理公司旁路路由失败", map[string]any{"error": err.Error()})
			}
		}
		if err := m.platform.ClearSystemProxy(cleanCtx); err != nil {
			m.logger.Warn("runtime", "清除系统代理失败", map[string]any{"error": err.Error()})
		}
		cleanCancel()
	}

	m.dns = nil
	m.dnsCache.Clear()
	m.stats.Stop()
	mode := m.status.Mode
	requested := m.status.RequestedMode
	recoveryRequired := m.journal.Exists()
	m.status = api.RuntimeStatus{
		State:            api.RuntimeStateIdle,
		Mode:             mode,
		RequestedMode:    requested,
		RecoveryRequired: recoveryRequired,
	}
	if recoveryRequired && rollbackErr != nil {
		wrapped := api.WrapError(api.ErrCodeRecoveryFailed, "停止时恢复网络状态失败", rollbackErr)
		m.status.LastErrorCode = wrapped.Code
		m.status.LastErrorMessage = wrapped.Error()
	}
	m.companyBypassInterface = ""
	m.companyBypassRoutes = nil
	m.companyDomainDialer = nil
	m.emitStatus()
}

func (m *Manager) backgroundLoop(ctx context.Context) {
	healthTicker := time.NewTicker(5 * time.Second)
	trafficTicker := time.NewTicker(1 * time.Second)
	companyTicker := time.NewTicker(15 * time.Second)
	defer healthTicker.Stop()
	defer trafficTicker.Stop()
	defer companyTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-healthTicker.C:
			health := m.forwarder.RefreshHealth(ctx)
			m.mu.Lock()
			m.health = health
			mode := m.status.Mode
			m.mu.Unlock()
			m.emitHealth()
			if mode == api.ModeSystem {
				m.guardSystemProxy(ctx)
			}
		case <-companyTicker.C:
			m.mu.Lock()
			companyDialer := m.companyDomainDialer
			m.mu.Unlock()
			if companyDialer != nil {
				companyDialer.Refresh(ctx)
			}
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

// guardSystemProxy checks that the OS-level proxy settings still point to our
// listeners.  If an external tool (Clash, V2Ray, etc.) overwrote them, we
// re-apply ours and notify the user via an event.
func (m *Manager) guardSystemProxy(parent context.Context) {
	checkCtx, checkCancel := context.WithTimeout(parent, 2*time.Second)
	defer checkCancel()

	current, err := m.platform.CurrentSystemProxy(checkCtx)
	if err != nil {
		m.logger.Warn("runtime", "检查系统代理状态失败", map[string]any{"error": err.Error()})
		return
	}

	expected := m.systemProxyConfig()
	if current.Enabled &&
		current.HTTPAddress == expected.HTTPAddress &&
		current.HTTPSAddress == expected.HTTPSAddress {
		return
	}

	m.logger.Warn("runtime", "系统代理被外部修改，正在自动恢复", map[string]any{
		"expected_http":   expected.HTTPAddress,
		"current_http":    current.HTTPAddress,
		"current_enabled": current.Enabled,
	})

	applyCtx, applyCancel := context.WithTimeout(parent, 2*time.Second)
	defer applyCancel()

	if err := m.platform.ApplySystemProxy(applyCtx, expected); err != nil {
		m.logger.Error("runtime", "自动恢复系统代理失败", map[string]any{"error": err.Error()})
		return
	}
	m.emitter.Emit(api.EventRuntimeError, map[string]any{
		"message": "系统代理被外部修改，已自动恢复",
	})
}

func (m *Manager) systemProxyConfig() platform.SystemProxyConfig {
	return platform.SystemProxyConfig{
		HTTPAddress:  m.httpListenAddr,
		HTTPSAddress: m.httpListenAddr,
		SOCKSAddress: m.socksListenAddr,
	}
}

func mergeRouteLists(routeSets ...[]string) []string {
	seen := map[string]struct{}{}
	merged := make([]string, 0)
	for _, routes := range routeSets {
		for _, route := range routes {
			route = strings.TrimSpace(route)
			if route == "" {
				continue
			}
			if _, ok := seen[route]; ok {
				continue
			}
			seen[route] = struct{}{}
			merged = append(merged, route)
		}
	}
	sort.Strings(merged)
	return merged
}
