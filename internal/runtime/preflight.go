package runtime

import (
	"context"
	"fmt"
	"time"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
	"github.com/friedhelmliu/ProxySeperator/internal/platform"
	"github.com/friedhelmliu/ProxySeperator/internal/rules"
)

const (
	checkRulesValid          = "rules_valid"
	checkCompanyUpstream     = "company_upstream"
	checkPersonalUpstream    = "personal_upstream"
	checkNetworkRecovery     = "network_recovery"
	checkSystemProxyConflict = "system_proxy_conflict"
	checkTUNAvailable        = "tun_available"
	checkTUNEgressPath       = "tun_egress_path"
)

func requestedMode(cfg api.Config) string {
	if cfg.Advanced.TUNEnabled || cfg.Advanced.Mode == api.ModeTUN {
		return api.ModeTUN
	}
	return api.ModeSystem
}

func passCheck(id, message string) api.PreflightCheck {
	return api.PreflightCheck{ID: id, Status: "pass", Message: message}
}

func warnCheck(id, code, message string) api.PreflightCheck {
	return api.PreflightCheck{ID: id, Status: "warn", Code: code, Message: message}
}

func failCheck(id, code, message string) api.PreflightCheck {
	return api.PreflightCheck{ID: id, Status: "fail", Code: code, Message: message}
}

func checkFailed(check api.PreflightCheck) bool {
	return check.Status == "fail"
}

func matchesSystemProxyState(state api.SystemProxyState, ours platform.SystemProxyConfig) bool {
	if !state.Enabled || state.Mixed {
		return false
	}
	return state.HTTPAddress == ours.HTTPAddress &&
		state.HTTPSAddress == ours.HTTPSAddress &&
		state.SOCKSAddress == ours.SOCKSAddress
}

func firstFailureMessage(checks []api.PreflightCheck) string {
	for _, check := range checks {
		if check.Status == "fail" {
			return check.Message
		}
	}
	return "启动前检查未通过"
}

func (m *Manager) runPreflight(ctx context.Context, cfg api.Config) api.PreflightReport {
	report := api.PreflightReport{
		RequestedMode: requestedMode(cfg),
		EffectiveMode: requestedMode(cfg),
		ModeReason:    "按配置使用当前模式",
		CanStart:      true,
	}
	checks := make([]api.PreflightCheck, 0, 7)

	parseResult := rules.ParseLines(cfg.Rules)
	if len(parseResult.Invalid) > 0 {
		checks = append(checks, failCheck(checkRulesValid, api.ErrCodeRuleValidationFailed, fmt.Sprintf("规则校验失败，共 %d 条无效规则", len(parseResult.Invalid))))
	} else {
		checks = append(checks, passCheck(checkRulesValid, "规则校验通过"))
	}

	company := ProbeUpstream(ctx, cfg.CompanyUpstream)
	personal := ProbeUpstream(ctx, cfg.PersonalUpstream)
	m.health = api.HealthStatus{
		CheckedAt: time.Now(),
		Company:   company,
		Personal:  personal,
	}
	if !company.Reachable {
		checks = append(checks, failCheck(checkCompanyUpstream, api.ErrCodeUpstreamUnavailable, "公司代理端口不可达"))
	} else {
		checks = append(checks, passCheck(checkCompanyUpstream, "公司代理端口可达"))
	}
	if !personal.Reachable {
		checks = append(checks, failCheck(checkPersonalUpstream, api.ErrCodeUpstreamUnavailable, "个人代理端口不可达"))
	} else {
		checks = append(checks, passCheck(checkPersonalUpstream, "个人代理端口可达"))
	}

	if m.journal.Exists() {
		report.RecoveryRequired = true
		checks = append(checks, failCheck(checkNetworkRecovery, api.ErrCodeRecoveryFailed, "发现未完成的网络恢复，请先执行修复网络状态"))
	} else {
		checks = append(checks, passCheck(checkNetworkRecovery, "未发现残留网络状态"))
	}

	proxyState, err := m.platform.CurrentSystemProxy(ctx)
	proxyConflict := false
	if err != nil {
		if report.RequestedMode == api.ModeSystem {
			checks = append(checks, failCheck(checkSystemProxyConflict, api.ErrCodeSystemProxyFailed, "无法读取当前系统代理状态"))
		} else {
			checks = append(checks, passCheck(checkSystemProxyConflict, "TUN 模式不依赖系统代理状态"))
		}
	} else if report.RequestedMode == api.ModeSystem && proxyState.Enabled && !matchesSystemProxyState(proxyState, m.systemProxyConfig()) {
		proxyConflict = true
		checks = append(checks, warnCheck(checkSystemProxyConflict, api.ErrCodeSystemProxyFailed, "检测到外部系统代理已占用，需切换到 TUN 共存模式"))
	} else if report.RequestedMode == api.ModeSystem {
		checks = append(checks, passCheck(checkSystemProxyConflict, "系统代理可安全接管"))
	} else {
		checks = append(checks, passCheck(checkSystemProxyConflict, "TUN 模式不依赖系统代理接管"))
	}

	tunRequired := report.RequestedMode == api.ModeTUN || proxyConflict
	if !tunRequired {
		checks = append(checks, passCheck(checkTUNAvailable, "当前模式无需 TUN"))
		checks = append(checks, passCheck(checkTUNEgressPath, "当前模式无需 TUN 出口检测"))
		report.EffectiveMode = api.ModeSystem
		report.ModeReason = "未检测到系统代理冲突，使用系统代理模式"
		report.Checks = checks
		report.CanStart = preflightCanStart(checks)
		return report
	}

	if err := m.platform.ValidateTUN(ctx); err != nil {
		checks = append(checks, failCheck(checkTUNAvailable, api.ErrorCode(err), "TUN 不可用"))
	} else {
		checks = append(checks, passCheck(checkTUNAvailable, "TUN 可用"))
	}

	egress, err := m.platform.DefaultEgressInterface(ctx)
	if err != nil || egress == "" {
		checks = append(checks, failCheck(checkTUNEgressPath, api.ErrCodeTUNUnavailable, "无法识别可用的默认出口接口"))
	} else {
		checks = append(checks, passCheck(checkTUNEgressPath, "已识别 TUN 默认出口接口"))
	}

	if proxyConflict {
		report.EffectiveMode = api.ModeTUN
		report.ModeReason = "检测到外部系统代理占用，自动切换为 TUN 共存模式"
	} else {
		report.EffectiveMode = api.ModeTUN
		report.ModeReason = "按配置使用 TUN 模式"
	}
	report.Checks = checks
	report.CanStart = preflightCanStart(checks)
	return report
}

func preflightCanStart(checks []api.PreflightCheck) bool {
	for _, check := range checks {
		if check.Status == "fail" {
			return false
		}
	}
	return true
}
