package app

import (
	"context"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
)

type BackendAPIInterface interface {
	LoadConfig(context.Context) (api.Config, error)
	RunPreflight(context.Context) (api.PreflightReport, error)
	RecoverNetwork(context.Context) error
	SaveConfig(context.Context, api.Config) error
	Start(context.Context) (api.RuntimeStatus, error)
	Stop(context.Context) error
	Restart(context.Context) (api.RuntimeStatus, error)
	GetRuntimeStatus(context.Context) (api.RuntimeStatus, error)
	GetHealthStatus(context.Context) (api.HealthStatus, error)
	GetTrafficStats(context.Context) (api.TrafficStats, error)
	GetRecentConnections(context.Context) ([]api.ConnectionRecord, error)
	ListRuleTemplates(context.Context) ([]api.RuleTemplate, error)
	TestRoute(context.Context, string) (api.RouteTestResult, error)
	ValidateRules(context.Context, []string) (api.RuleValidationResult, error)
	ListLogs(context.Context, int) ([]api.LogEntry, error)
	SetLanguage(context.Context, string) error
}
