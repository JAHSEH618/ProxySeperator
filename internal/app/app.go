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

	cfg api.Config
}

func NewBackendAPI() *BackendAPI {
	buffer := logging.NewRingBuffer(500)
	logger := logging.NewLogger(buffer)
	emitter := &dynamicEmitter{}
	manager := runtimeapp.NewManager(logger, emitter)

	apiService := &BackendAPI{
		store:   config.NewStore(api.AppName),
		logger:  logger,
		emitter: emitter,
		manager: manager,
		cfg:     api.DefaultConfig(),
	}

	logger.AddSink(func(entry api.LogEntry) {
		emitter.Emit(api.EventRuntimeLog, entry)
	})
	return apiService
}

func (b *BackendAPI) BindEvents(fn func(string, any)) {
	b.emitter.SetEmit(fn)
}

func (b *BackendAPI) LoadConfig(context.Context) (api.Config, error) {
	cfg, err := b.store.Load()
	if err != nil {
		return api.Config{}, err
	}
	b.mu.Lock()
	b.cfg = cfg
	b.mu.Unlock()
	return cfg, nil
}

func (b *BackendAPI) SaveConfig(_ context.Context, cfg api.Config) error {
	if err := validateConfig(cfg); err != nil {
		return err
	}
	if err := b.store.Save(cfg); err != nil {
		return err
	}
	b.mu.Lock()
	b.cfg = cfg
	b.mu.Unlock()
	return nil
}

func (b *BackendAPI) Start(ctx context.Context) (api.RuntimeStatus, error) {
	cfg, err := b.ensureConfig()
	if err != nil {
		return api.RuntimeStatus{}, err
	}
	return b.manager.Start(cfg)
}

func (b *BackendAPI) Stop(context.Context) error {
	return b.manager.Stop()
}

func (b *BackendAPI) Restart(ctx context.Context) (api.RuntimeStatus, error) {
	cfg, err := b.ensureConfig()
	if err != nil {
		return api.RuntimeStatus{}, err
	}
	return b.manager.Restart(cfg)
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
		return api.RouteTestResult{}, err
	}
	parseResult := rules.ParseLines(cfg.Rules)
	matcher := rules.NewMatcher(parseResult.Compiled)
	result := matcher.Match(input)
	return api.RouteTestResult{
		Input:       input,
		Normalized:  result.Normalized,
		Target:      result.Target,
		RuleType:    result.RuleType,
		MatchedRule: result.MatchedRule,
		Reason:      result.Reason,
	}, nil
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
	return api.RuleValidationResult{
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
	}, nil
}

func (b *BackendAPI) ListLogs(_ context.Context, limit int) ([]api.LogEntry, error) {
	return b.logger.List(limit), nil
}

func (b *BackendAPI) SetLanguage(_ context.Context, lang string) error {
	cfg, err := b.ensureConfig()
	if err != nil {
		return err
	}
	cfg.UI.Language = strings.TrimSpace(lang)
	if cfg.UI.Language == "" {
		cfg.UI.Language = api.DefaultConfig().UI.Language
	}
	if err := b.store.Save(cfg); err != nil {
		return err
	}
	b.mu.Lock()
	b.cfg = cfg
	b.mu.Unlock()
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
	if cfg.CompanyUpstream.Host == "" || cfg.PersonalUpstream.Host == "" {
		return api.NewError(api.ErrCodeInvalidConfig, "代理 Host 不能为空")
	}
	if cfg.CompanyUpstream.Port <= 0 || cfg.CompanyUpstream.Port > 65535 {
		return api.NewError(api.ErrCodeInvalidConfig, "公司代理端口无效")
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
