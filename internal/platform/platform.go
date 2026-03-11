package platform

import "context"

type SystemProxyConfig struct {
	HTTPAddress  string
	HTTPSAddress string
	SOCKSAddress string
}

type TUNOptions struct {
	DNSListenAddress   string
	SOCKSListenAddress string
	MTU                int
}

type Controller interface {
	ApplySystemProxy(ctx context.Context, cfg SystemProxyConfig) error
	ClearSystemProxy(ctx context.Context) error
	EnableAutoStart(ctx context.Context, executablePath string) error
	DisableAutoStart(ctx context.Context) error
	CurrentDNSResolvers(ctx context.Context) ([]string, error)
	StartTUN(ctx context.Context, opts TUNOptions) error
	StopTUN(ctx context.Context) error
}
