package runtime

import (
	"context"
	"os"
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

	forwarder *Forwarder
	httpProxy *proxy.HTTPServer
	socks5    *proxy.SOCKS5Server

	health api.HealthStatus
	status api.RuntimeStatus
	cfg    api.Config
	cancel context.CancelFunc
}

type Options struct {
	Platform        platform.Controller
	HTTPListenAddr  string
	SOCKSListenAddr string
	DNSListenAddr   string
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
	return &Manager{
		logger:          logger,
		emitter:         emitter,
		platform:        controller,
		httpListenAddr:  opts.HTTPListenAddr,
		socksListenAddr: opts.SOCKSListenAddr,
		dnsListenAddr:   opts.DNSListenAddr,
		dnsCache:        localdns.NewCache(),
		stats:           NewStatsTracker(),
		status: api.RuntimeStatus{
			State: api.RuntimeStateIdle,
			Mode:  api.ModeSystem,
		},
	}
}

func (m *Manager) Start(cfg api.Config) (api.RuntimeStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.status.State == api.RuntimeStateRunning || m.status.State == api.RuntimeStateStarting {
		return m.status, api.NewError(api.ErrCodeRuntimeAlreadyRunning, "运行时已经启动")
	}

	parseResult := rules.ParseLines(cfg.Rules)
	if len(parseResult.Invalid) > 0 {
		return m.status, api.NewError(api.ErrCodeRuleValidationFailed, "规则校验失败，请先修正无效规则")
	}

	mode := cfg.Advanced.Mode
	if cfg.Advanced.TUNEnabled {
		mode = api.ModeTUN
	}
	if mode == "" {
		mode = api.ModeSystem
	}

	m.cfg = cfg
	m.status = api.RuntimeStatus{
		State: api.RuntimeStateStarting,
		Mode:  mode,
	}
	m.emitStatus()

	matcher := rules.NewMatcher(parseResult.Compiled)
	m.forwarder = NewForwarder(cfg, matcher, m.dnsCache, m.stats, m.logger)
	m.httpProxy = proxy.NewHTTPServer(m.httpListenAddr, m.forwarder, m.logger)
	m.socks5 = proxy.NewSOCKS5Server(m.socksListenAddr, m.forwarder, m.logger)

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	dnsResolvers := []string(nil)
	if mode == api.ModeTUN {
		resolvers, err := m.platform.CurrentDNSResolvers(ctx)
		if err != nil {
			m.logger.Warn("runtime", "读取系统 DNS 失败，回退到默认公共解析器", map[string]any{"error": err.Error()})
		} else {
			dnsResolvers = resolvers
		}
	}
	m.dns = localdns.NewServer(m.dnsListenAddr, m.dnsCache, dnsResolvers, m.logger)

	health := m.forwarder.RefreshHealth(ctx)
	m.health = health
	m.emitHealth()
	if !health.Company.Reachable {
		m.rollbackLocked(ctx)
		return m.status, api.NewError(api.ErrCodeUpstreamUnavailable, "公司代理端口不可达")
	}
	if !health.Personal.Reachable {
		m.rollbackLocked(ctx)
		return m.status, api.NewError(api.ErrCodeUpstreamUnavailable, "个人代理端口不可达")
	}

	m.stats.Start(mode)
	if err := m.httpProxy.Start(ctx); err != nil {
		m.rollbackLocked(ctx)
		return m.status, err
	}
	if err := m.socks5.Start(ctx); err != nil {
		m.rollbackLocked(ctx)
		return m.status, err
	}

	if mode == api.ModeTUN {
		if err := m.dns.Start(); err != nil {
			m.rollbackLocked(ctx)
			return m.status, api.WrapError(api.ErrCodeRuntimeStartFailed, "启动本地 DNS 失败", err)
		}
		if err := m.platform.StartTUN(ctx, platform.TUNOptions{
			DNSListenAddress:   m.dnsListenAddr,
			SOCKSListenAddress: m.socksListenAddr,
			MTU:                1500,
		}); err != nil {
			m.rollbackLocked(ctx)
			return m.status, err
		}
	} else {
		err := m.platform.ApplySystemProxy(ctx, platform.SystemProxyConfig{
			HTTPAddress:  m.httpListenAddr,
			HTTPSAddress: m.httpListenAddr,
			SOCKSAddress: m.socksListenAddr,
		})
		if err != nil {
			m.rollbackLocked(ctx)
			return m.status, err
		}
	}

	if cfg.Advanced.AutoStart {
		if exe, err := os.Executable(); err == nil {
			_ = m.platform.EnableAutoStart(ctx, exe)
		}
	}

	now := time.Now()
	m.status = api.RuntimeStatus{
		State:         api.RuntimeStateRunning,
		Mode:          mode,
		StartedAt:     &now,
		UptimeSeconds: 0,
	}
	m.emitStatus()
	m.emitTraffic()
	go m.backgroundLoop(ctx)
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

func (m *Manager) rollbackLocked(ctx context.Context) {
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
	m.dnsCache.Clear()
	m.stats.Stop()
	mode := m.status.Mode
	m.status = api.RuntimeStatus{
		State: api.RuntimeStateIdle,
		Mode:  mode,
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
