package platform

import "context"

import "github.com/friedhelmliu/ProxySeperator/internal/api"

type SystemProxyConfig struct {
	HTTPAddress  string
	HTTPSAddress string
	SOCKSAddress string
}

type TUNOptions struct {
	DNSListenAddress   string
	SOCKSListenAddress string
	EgressInterface    string
	MTU                int
}

type Controller interface {
	ApplySystemProxy(ctx context.Context, cfg SystemProxyConfig) error
	ClearSystemProxy(ctx context.Context) error
	PreferredCompanyBypassInterface(ctx context.Context) (string, error)
	ApplyCompanyBypassRoutes(ctx context.Context, iface string, routes []string) error
	ClearCompanyBypassRoutes(ctx context.Context, iface string, routes []string) error
	EnableAutoStart(ctx context.Context, executablePath string) error
	DisableAutoStart(ctx context.Context) error
	CurrentSystemProxy(ctx context.Context) (api.SystemProxyState, error)
	CurrentDNSResolvers(ctx context.Context) ([]string, error)
	CaptureRecoverySnapshot(ctx context.Context, mode string) (api.RecoverySnapshot, error)
	RecoverNetwork(ctx context.Context, snapshot api.RecoverySnapshot) error
	DefaultEgressInterface(ctx context.Context) (string, error)
	// IsDefaultRouteViaVPN 检测默认路由是否通过 VPN 接口（utun*）
	// 返回: isVPN, vpnInterface, error
	IsDefaultRouteViaVPN(ctx context.Context) (bool, string, error)
	ValidateTUN(ctx context.Context) error
	StartTUN(ctx context.Context, opts TUNOptions) error
	StopTUN(ctx context.Context) error
	StopRouteHelper()
}
