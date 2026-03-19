//go:build !darwin && !windows

package platform

import (
	"context"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
	"github.com/friedhelmliu/ProxySeperator/internal/logging"
)

type unsupportedController struct {
	logger *logging.Logger
}

func NewController(logger *logging.Logger) Controller {
	return &unsupportedController{logger: logger}
}

func (u *unsupportedController) ApplySystemProxy(context.Context, SystemProxyConfig) error {
	return api.NewError(api.ErrCodePlatformUnsupported, "当前平台不支持系统代理设置")
}

func (u *unsupportedController) ClearSystemProxy(context.Context) error {
	return nil
}

func (u *unsupportedController) PreferredCompanyBypassInterface(context.Context) (string, error) {
	return "", api.NewError(api.ErrCodePlatformUnsupported, "当前平台不支持公司旁路接口检测")
}

func (u *unsupportedController) ApplyCompanyBypassRoutes(context.Context, string, []string) error {
	return api.NewError(api.ErrCodePlatformUnsupported, "当前平台不支持公司旁路路由")
}

func (u *unsupportedController) ClearCompanyBypassRoutes(context.Context, string, []string) error {
	return nil
}

func (u *unsupportedController) EnableAutoStart(context.Context, string) error {
	return api.NewError(api.ErrCodePlatformUnsupported, "当前平台不支持开机自启")
}

func (u *unsupportedController) DisableAutoStart(context.Context) error {
	return nil
}

func (u *unsupportedController) CurrentSystemProxy(context.Context) (api.SystemProxyState, error) {
	return api.SystemProxyState{}, api.NewError(api.ErrCodePlatformUnsupported, "当前平台不支持系统代理读取")
}

func (u *unsupportedController) CurrentDNSResolvers(context.Context) ([]string, error) {
	return nil, api.NewError(api.ErrCodePlatformUnsupported, "当前平台不支持 DNS 解析器读取")
}

func (u *unsupportedController) CaptureRecoverySnapshot(context.Context, string) (api.RecoverySnapshot, error) {
	return api.RecoverySnapshot{}, api.NewError(api.ErrCodePlatformUnsupported, "当前平台不支持网络快照")
}

func (u *unsupportedController) RecoverNetwork(context.Context, api.RecoverySnapshot) error {
	return api.NewError(api.ErrCodePlatformUnsupported, "当前平台不支持网络恢复")
}

func (u *unsupportedController) DefaultEgressInterface(context.Context) (string, error) {
	return "", api.NewError(api.ErrCodePlatformUnsupported, "当前平台不支持默认出口检测")
}

func (u *unsupportedController) IsDefaultRouteViaVPN(context.Context) (bool, string, error) {
	return false, "", nil
}

func (u *unsupportedController) ValidateTUN(context.Context) error {
	return api.NewError(api.ErrCodeTUNUnavailable, "当前平台不支持 TUN")
}

func (u *unsupportedController) StartTUN(context.Context, TUNOptions) error {
	return api.NewError(api.ErrCodeTUNUnavailable, "当前平台不支持 TUN")
}

func (u *unsupportedController) StopTUN(context.Context) error {
	return nil
}

func (u *unsupportedController) StopRouteHelper() {}
