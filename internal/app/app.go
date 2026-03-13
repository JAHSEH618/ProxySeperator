package app

import (
	"context"
	"strings"
	"sync"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
	"github.com/friedhelmliu/ProxySeperator/internal/config"
	"github.com/friedhelmliu/ProxySeperator/internal/logging"
	"github.com/friedhelmliu/ProxySeperator/internal/rules"
	runtimeapp "github.com/friedhelmliu/ProxySeperator/internal/runtime"
)

type dynamicEmitter struct {
	mu   sync.RWMutex
	emit func(string, any)
}

func (e *dynamicEmitter) Emit(name string, payload any) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.emit != nil {
		e.emit(name, payload)
	}
}

func (e *dynamicEmitter) SetEmit(fn func(string, any)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emit = fn
}

type BackendAPI struct {
	mu      sync.Mutex
	store   *config.Store
	logger  *logging.Logger
	emitter *dynamicEmitter
	manager *runtimeapp.Manager

	cfg             api.Config
	onWindowRestore func()
}

func NewBackendAPI() *BackendAPI {
	buffer := logging.NewRingBuffer(500)
	logger := logging.NewLogger(buffer)
	logger.AddSink(logging.StdoutSink)
	emitter := &dynamicEmitter{}
	store := config.NewStore(api.AppName)
	journalPath, _ := store.RecoveryJournalPath()

	apiService := &BackendAPI{
		store:   store,
		logger:  logger,
		emitter: emitter,
		manager: runtimeapp.NewManagerWithOptions(logger, emitter, runtimeapp.Options{
			RecoveryJournalPath: journalPath,
		}),
		cfg: api.DefaultConfig(),
	}

	if cfg, err := store.Load(); err == nil {
		apiService.cfg = cfg
		if recovered, recoverErr := apiService.manager.EnsureRecovered(cfg); recoverErr != nil {
			logger.Warn("runtime", "初始化时自动恢复残留网络状态失败", map[string]any{"error": recoverErr.Error()})
		} else if recovered {
			logger.Info("runtime", "初始化时已自动恢复残留网络状态", nil)
		}
	} else {
		logger.Warn("config", "初始化时加载配置失败，跳过自动恢复", map[string]any{"error": err.Error()})
	}

	logger.AddSink(func(entry api.LogEntry) {
		emitter.Emit(api.EventRuntimeLog, entry)
	})
	logger.Info("app", "后端 API 已初始化", map[string]any{
		"recoveryJournalPath": journalPath,
	})
	return apiService
}

func (b *BackendAPI) BindEvents(fn func(string, any)) {
	b.emitter.SetEmit(fn)
}

// OnWindowRestore registers a callback invoked after privileged operations
// (Start, Stop, RecoverNetwork) complete. Use this to bring the application
// window back to the foreground after macOS authorization dialogs steal focus.
func (b *BackendAPI) OnWindowRestore(fn func()) {
	b.onWindowRestore = fn
}

func (b *BackendAPI) restoreWindow() {
	if b.onWindowRestore != nil {
		b.onWindowRestore()
	}
}

func (b *BackendAPI) LoadConfig(context.Context) (api.Config, error) {
	cfg, err := b.store.Load()
	if err != nil {
		b.logger.Error("config", "加载配置失败", map[string]any{"error": err.Error()})
		return api.Config{}, err
	}
	b.mu.Lock()
	b.cfg = cfg
	b.mu.Unlock()
	b.logger.Info("config", "配置已加载", map[string]any{
		"rules":         len(cfg.Rules),
		"requestedMode": configuredMode(cfg),
	})
	return cfg, nil
}

func (b *BackendAPI) RunPreflight(context.Context) (api.PreflightReport, error) {
	cfg, err := b.ensureConfig()
	if err != nil {
		b.logger.Error("runtime", "执行启动前检查失败", map[string]any{"error": err.Error()})
		return api.PreflightReport{}, err
	}
	report, err := b.manager.RunPreflight(cfg)
	if err != nil {
		b.logger.Error("runtime", "启动前检查执行失败", map[string]any{"error": err.Error()})
		return api.PreflightReport{}, err
	}
	b.logger.Info("runtime", "启动前检查完成", map[string]any{
		"canStart":      report.CanStart,
		"requestedMode": report.RequestedMode,
		"effectiveMode": report.EffectiveMode,
	})
	return report, nil
}

func (b *BackendAPI) RecoverNetwork(context.Context) error {
	b.logger.Info("runtime", "收到恢复网络请求", nil)
	err := b.manager.RecoverNetwork()
	defer b.restoreWindow()
	if err != nil {
		b.logger.Error("runtime", "恢复网络失败", map[string]any{"error": err.Error()})
		return err
	}
	b.logger.Info("runtime", "系统网络状态已恢复", nil)
	return nil
}

func (b *BackendAPI) ForceRecoverNetwork() error {
	b.logger.Info("runtime", "收到强制恢复网络请求", nil)
	err := b.manager.ForceRecoverNetwork()
	defer b.restoreWindow()
	if err != nil {
		b.logger.Error("runtime", "强制恢复网络失败", map[string]any{"error": err.Error()})
		return err
	}
	b.logger.Info("runtime", "系统网络状态已强制恢复", nil)
	return nil
}

func (b *BackendAPI) SaveConfig(_ context.Context, cfg api.Config) error {
	if err := validateConfig(cfg); err != nil {
		b.logger.Warn("config", "配置校验失败", map[string]any{"error": err.Error()})
		return err
	}
	if err := b.store.Save(cfg); err != nil {
		b.logger.Error("config", "保存配置失败", map[string]any{"error": err.Error()})
		return err
	}
	b.mu.Lock()
	b.cfg = cfg
	b.mu.Unlock()
	b.logger.Info("config", "配置已保存", map[string]any{
		"rules":         len(cfg.Rules),
		"requestedMode": configuredMode(cfg),
	})
	return nil
}

func (b *BackendAPI) Start(ctx context.Context) (api.RuntimeStatus, error) {
	cfg, err := b.ensureConfig()
	if err != nil {
		b.logger.Error("runtime", "启动失败，读取配置出错", map[string]any{"error": err.Error()})
		return api.RuntimeStatus{}, err
	}
	b.logger.Info("runtime", "收到启动请求", map[string]any{"requestedMode": configuredMode(cfg)})
	status, err := b.manager.Start(cfg)
	defer b.restoreWindow()
	if err != nil {
		b.logger.Error("runtime", "启动失败", map[string]any{"error": err.Error()})
		return api.RuntimeStatus{}, err
	}
	b.logger.Info("runtime", "启动成功", map[string]any{"mode": status.Mode})
	return status, nil
}

func (b *BackendAPI) Stop(context.Context) error {
	b.logger.Info("runtime", "收到停止请求", nil)
	err := b.manager.Stop()
	defer b.restoreWindow()
	if err != nil {
		b.logger.Error("runtime", "停止失败", map[string]any{"error": err.Error()})
		return err
	}
	b.logger.Info("runtime", "停止完成", nil)
	return nil
}

func (b *BackendAPI) Restart(ctx context.Context) (api.RuntimeStatus, error) {
	cfg, err := b.ensureConfig()
	if err != nil {
		b.logger.Error("runtime", "重启失败，读取配置出错", map[string]any{"error": err.Error()})
		return api.RuntimeStatus{}, err
	}
	b.logger.Info("runtime", "收到重启请求", map[string]any{"requestedMode": configuredMode(cfg)})
	status, err := b.manager.Restart(cfg)
	defer b.restoreWindow()
	if err != nil {
		b.logger.Error("runtime", "重启失败", map[string]any{"error": err.Error()})
		return api.RuntimeStatus{}, err
	}
	b.logger.Info("runtime", "重启成功", map[string]any{"mode": status.Mode})
	return status, nil
}

func (b *BackendAPI) GetRuntimeStatus(context.Context) (api.RuntimeStatus, error) {
	return b.manager.Status(), nil
}

func (b *BackendAPI) GetHealthStatus(context.Context) (api.HealthStatus, error) {
	return b.manager.Health(), nil
}

func (b *BackendAPI) GetTrafficStats(context.Context) (api.TrafficStats, error) {
	return b.manager.Traffic(), nil
}

func (b *BackendAPI) TestRoute(_ context.Context, input string) (api.RouteTestResult, error) {
	cfg, err := b.ensureConfig()
	if err != nil {
		b.logger.Error("route", "执行路由测试失败，读取配置出错", map[string]any{"error": err.Error()})
		return api.RouteTestResult{}, err
	}
	parseResult := rules.ParseLines(cfg.Rules)
	matcher := rules.NewMatcher(parseResult.Compiled)
	result := matcher.Match(input)
	output := api.RouteTestResult{
		Input:       input,
		Normalized:  result.Normalized,
		Target:      result.Target,
		RuleType:    result.RuleType,
		MatchedRule: result.MatchedRule,
		Reason:      result.Reason,
	}
	b.logger.Info("route", "路由测试完成", map[string]any{
		"input":       input,
		"target":      output.Target,
		"ruleType":    output.RuleType,
		"matchedRule": output.MatchedRule,
	})
	return output, nil
}

func (b *BackendAPI) ValidateRules(_ context.Context, lines []string) (api.RuleValidationResult, error) {
	parseResult := rules.ParseLines(lines)
	invalid := make([]api.InvalidRule, 0, len(parseResult.Invalid))
	for _, item := range parseResult.Invalid {
		invalid = append(invalid, api.InvalidRule{
			Line:   item.Line,
			Input:  item.Input,
			Reason: item.Reason,
		})
	}
	result := api.RuleValidationResult{
		ValidRules:   parseResult.Valid,
		InvalidRules: invalid,
		Summary: api.RuleSummary{
			Total:         parseResult.Summary.Total,
			Valid:         parseResult.Summary.Valid,
			Invalid:       parseResult.Summary.Invalid,
			DomainSuffix:  parseResult.Summary.DomainSuffix,
			DomainExact:   parseResult.Summary.DomainExact,
			DomainKeyword: parseResult.Summary.DomainKeyword,
			CIDR:          parseResult.Summary.CIDR,
		},
	}
	b.logger.Info("rules", "规则校验完成", map[string]any{
		"total":   result.Summary.Total,
		"valid":   result.Summary.Valid,
		"invalid": result.Summary.Invalid,
	})
	return result, nil
}

func (b *BackendAPI) ListLogs(_ context.Context, limit int) ([]api.LogEntry, error) {
	return b.logger.List(limit), nil
}

func (b *BackendAPI) SetLanguage(_ context.Context, lang string) error {
	cfg, err := b.ensureConfig()
	if err != nil {
		b.logger.Error("config", "切换语言失败，读取配置出错", map[string]any{"error": err.Error()})
		return err
	}
	cfg.UI.Language = strings.TrimSpace(lang)
	if cfg.UI.Language == "" {
		cfg.UI.Language = api.DefaultConfig().UI.Language
	}
	if err := b.store.Save(cfg); err != nil {
		b.logger.Error("config", "保存语言设置失败", map[string]any{"error": err.Error()})
		return err
	}
	b.mu.Lock()
	b.cfg = cfg
	b.mu.Unlock()
	b.logger.Info("config", "语言设置已更新", map[string]any{"language": cfg.UI.Language})
	return nil
}

func (b *BackendAPI) Shutdown(context.Context) error {
	if err := b.manager.Stop(); err != nil && api.ErrorCode(err) != api.ErrCodeRuntimeNotRunning {
		return err
	}
	return nil
}

func (b *BackendAPI) ensureConfig() (api.Config, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cfg.Version == 0 {
		cfg, err := b.store.Load()
		if err != nil {
			return api.Config{}, err
		}
		b.cfg = cfg
	}
	return b.cfg, nil
}

func validateConfig(cfg api.Config) error {
	if strings.TrimSpace(cfg.PersonalUpstream.Host) == "" {
		return api.NewError(api.ErrCodeInvalidConfig, "个人代理 Host 不能为空")
	}
	if cfg.PersonalUpstream.Port <= 0 || cfg.PersonalUpstream.Port > 65535 {
		return api.NewError(api.ErrCodeInvalidConfig, "个人代理端口无效")
	}
	parseResult := rules.ParseLines(cfg.Rules)
	if len(parseResult.Invalid) > 0 {
		return api.NewError(api.ErrCodeRuleValidationFailed, "存在无效规则")
	}
	return nil
}

func configuredMode(cfg api.Config) string {
	if cfg.Advanced.PersonalTUNMode {
		return api.ModeSystem
	}
	if cfg.Advanced.TUNEnabled || cfg.Advanced.Mode == api.ModeTUN {
		return api.ModeTUN
	}
	return api.ModeSystem
}
