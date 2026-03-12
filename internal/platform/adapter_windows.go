//go:build windows

package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
	"github.com/friedhelmliu/ProxySeperator/internal/logging"
	"golang.org/x/sys/windows/registry"
)

const (
	internetSettingsPath = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`
	runKeyPath           = `Software\Microsoft\Windows\CurrentVersion\Run`
	autoStartName        = "ProxySeparator"
	windowsTUNDevice     = "tun://ProxySeparatorTun"
	windowsTUNAddress    = "198.18.0.1"
)

var windowsSplitRoutes = []string{"0.0.0.0/1", "128.0.0.0/1"}

type windowsSystemProxySnapshot struct {
	Enabled      bool   `json:"enabled"`
	HTTPAddress  string `json:"httpAddress,omitempty"`
	HTTPSAddress string `json:"httpsAddress,omitempty"`
	SOCKSAddress string `json:"socksAddress,omitempty"`
	Server       string `json:"server,omitempty"`
}

type windowsDNSState struct {
	InterfaceAlias  string   `json:"InterfaceAlias"`
	ServerAddresses []string `json:"ServerAddresses"`
}

type windowsController struct {
	logger *logging.Logger

	mu           sync.Mutex
	tun          *tunHelperProcess
	tunInterface string
	dnsSnapshot  []windowsDNSState
}

func NewController(logger *logging.Logger) Controller {
	return &windowsController{logger: logger}
}

func (c *windowsController) ApplySystemProxy(_ context.Context, cfg SystemProxyConfig) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, internetSettingsPath, registry.SET_VALUE)
	if err != nil {
		return api.WrapError(api.ErrCodeSystemProxyFailed, "打开注册表失败", err)
	}
	defer key.Close()

	if err := key.SetDWordValue("ProxyEnable", 1); err != nil {
		return api.WrapError(api.ErrCodeSystemProxyFailed, "启用系统代理失败", err)
	}
	server := "http=" + cfg.HTTPAddress + ";https=" + cfg.HTTPSAddress + ";socks=" + cfg.SOCKSAddress
	if err := key.SetStringValue("ProxyServer", server); err != nil {
		return api.WrapError(api.ErrCodeSystemProxyFailed, "写入 ProxyServer 失败", err)
	}
	return nil
}

func (c *windowsController) ClearSystemProxy(_ context.Context) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, internetSettingsPath, registry.SET_VALUE)
	if err != nil {
		return api.WrapError(api.ErrCodeSystemProxyFailed, "打开注册表失败", err)
	}
	defer key.Close()

	if err := key.SetDWordValue("ProxyEnable", 0); err != nil {
		return api.WrapError(api.ErrCodeSystemProxyFailed, "关闭系统代理失败", err)
	}
	_ = key.DeleteValue("ProxyServer")
	return nil
}

func (c *windowsController) PreferredCompanyBypassInterface(ctx context.Context) (string, error) {
	return c.DefaultEgressInterface(ctx)
}

func (c *windowsController) ApplyCompanyBypassRoutes(ctx context.Context, iface string, routes []string) error {
	if iface == "" || len(routes) == 0 {
		return nil
	}
	for _, prefix := range routes {
		script := fmt.Sprintf(`
$existing = Get-NetRoute -InterfaceAlias '%s' -DestinationPrefix '%s' -ErrorAction SilentlyContinue
if (-not $existing) {
  New-NetRoute -InterfaceAlias '%s' -DestinationPrefix '%s' -NextHop '0.0.0.0' -RouteMetric 1 | Out-Null
}
`, psQuote(iface), prefix, psQuote(iface), prefix)
		if _, err := runPowerShell(ctx, script); err != nil {
			return api.WrapError(api.ErrCodeSystemProxyFailed, "安装公司旁路路由失败", err)
		}
	}
	return nil
}

func (c *windowsController) ClearCompanyBypassRoutes(ctx context.Context, iface string, routes []string) error {
	if iface == "" || len(routes) == 0 {
		return nil
	}
	var firstErr error
	for _, prefix := range routes {
		script := fmt.Sprintf("Remove-NetRoute -InterfaceAlias '%s' -DestinationPrefix '%s' -Confirm:$false -ErrorAction SilentlyContinue", psQuote(iface), prefix)
		if _, err := runPowerShell(ctx, script); err != nil && firstErr == nil {
			firstErr = api.WrapError(api.ErrCodeSystemProxyFailed, "清理公司旁路路由失败", err)
		}
	}
	return firstErr
}

func (c *windowsController) EnableAutoStart(_ context.Context, executablePath string) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return api.WrapError(api.ErrCodePermissionDenied, "打开注册表失败", err)
	}
	defer key.Close()
	if err := key.SetStringValue(autoStartName, executablePath); err != nil {
		return api.WrapError(api.ErrCodePermissionDenied, "写入开机自启失败", err)
	}
	return nil
}

func (c *windowsController) DisableAutoStart(_ context.Context) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return api.WrapError(api.ErrCodePermissionDenied, "打开注册表失败", err)
	}
	defer key.Close()
	_ = key.DeleteValue(autoStartName)
	return nil
}

func (c *windowsController) CurrentSystemProxy(_ context.Context) (api.SystemProxyState, error) {
	snapshot, err := c.readSystemProxySnapshot()
	if err != nil {
		return api.SystemProxyState{}, err
	}
	return api.SystemProxyState{
		Enabled:      snapshot.Enabled,
		HTTPAddress:  snapshot.HTTPAddress,
		HTTPSAddress: snapshot.HTTPSAddress,
		SOCKSAddress: snapshot.SOCKSAddress,
	}, nil
}

func (c *windowsController) CurrentDNSResolvers(ctx context.Context) ([]string, error) {
	states, err := c.snapshotDNSServers(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	resolvers := make([]string, 0)
	for _, state := range states {
		for _, server := range state.ServerAddresses {
			server = strings.TrimSpace(server)
			if server == "" || strings.HasPrefix(server, "127.") || server == "::1" {
				continue
			}
			if _, ok := seen[server]; ok {
				continue
			}
			seen[server] = struct{}{}
			resolvers = append(resolvers, server)
		}
	}
	if len(resolvers) == 0 {
		return nil, fmt.Errorf("no active DNS resolver detected")
	}
	sort.Strings(resolvers)
	return resolvers, nil
}

func (c *windowsController) CaptureRecoverySnapshot(ctx context.Context, mode string) (api.RecoverySnapshot, error) {
	proxySnapshot, err := c.readSystemProxySnapshot()
	if err != nil {
		return api.RecoverySnapshot{}, err
	}
	proxyData, err := json.Marshal(proxySnapshot)
	if err != nil {
		return api.RecoverySnapshot{}, err
	}
	dnsSnapshot, err := c.snapshotDNSServers(ctx)
	if err != nil {
		return api.RecoverySnapshot{}, err
	}
	dnsData, err := json.Marshal(dnsSnapshot)
	if err != nil {
		return api.RecoverySnapshot{}, err
	}

	c.mu.Lock()
	tunState := api.TUNRecoveryState{}
	if c.tunInterface != "" {
		tunState = api.TUNRecoveryState{
			Interface: c.tunInterface,
			Routes:    append([]string(nil), windowsSplitRoutes...),
		}
	}
	c.mu.Unlock()

	return api.RecoverySnapshot{
		Platform: "windows",
		Mode:     mode,
		SystemProxy: api.SystemProxyState{
			Enabled:      proxySnapshot.Enabled,
			HTTPAddress:  proxySnapshot.HTTPAddress,
			HTTPSAddress: proxySnapshot.HTTPSAddress,
			SOCKSAddress: proxySnapshot.SOCKSAddress,
		},
		SystemProxyData: proxyData,
		DNSState:        dnsData,
		TUNState:        tunState,
	}, nil
}

func (c *windowsController) RecoverNetwork(ctx context.Context, snapshot api.RecoverySnapshot) error {
	c.mu.Lock()
	helper := c.tun
	c.tun = nil
	c.tunInterface = ""
	c.dnsSnapshot = nil
	c.mu.Unlock()

	if len(snapshot.SystemProxyData) > 0 {
		var state windowsSystemProxySnapshot
		if err := json.Unmarshal(snapshot.SystemProxyData, &state); err != nil {
			return api.WrapError(api.ErrCodeRecoveryFailed, "解析系统代理快照失败", err)
		}
		if err := c.restoreSystemProxy(state); err != nil {
			return api.WrapError(api.ErrCodeRecoveryFailed, "恢复系统代理失败", err)
		}
	}
	if len(snapshot.DNSState) > 0 {
		var states []windowsDNSState
		if err := json.Unmarshal(snapshot.DNSState, &states); err != nil {
			return api.WrapError(api.ErrCodeRecoveryFailed, "解析 DNS 快照失败", err)
		}
		if err := c.restoreDNSServers(ctx, states); err != nil {
			return api.WrapError(api.ErrCodeRecoveryFailed, "恢复系统 DNS 失败", err)
		}
	}
	if snapshot.CompanyBypass.Interface != "" && len(snapshot.CompanyBypass.Routes) > 0 {
		if err := c.ClearCompanyBypassRoutes(ctx, snapshot.CompanyBypass.Interface, snapshot.CompanyBypass.Routes); err != nil {
			return api.WrapError(api.ErrCodeRecoveryFailed, "恢复公司旁路路由失败", err)
		}
	}
	if err := c.removeSplitRoutes(ctx, snapshot.TUNState.Interface); err != nil {
		return api.WrapError(api.ErrCodeRecoveryFailed, "删除残留 TUN 路由失败", err)
	}
	if err := c.removeTUNAddress(ctx, snapshot.TUNState.Interface); err != nil {
		return api.WrapError(api.ErrCodeRecoveryFailed, "删除残留 TUN 地址失败", err)
	}
	if helper != nil {
		if err := helper.Stop(ctx); err != nil && !errors.Is(err, context.Canceled) {
			return api.WrapError(api.ErrCodeRecoveryFailed, "停止残留 TUN helper 失败", err)
		}
	}
	return nil
}

func (c *windowsController) DefaultEgressInterface(ctx context.Context) (string, error) {
	output, err := runPowerShell(ctx, `Get-NetIPConfiguration | Where-Object { $_.IPv4DefaultGateway -ne $null -and $_.NetAdapter.Status -eq 'Up' } | Sort-Object InterfaceMetric | Select-Object -First 1 -ExpandProperty InterfaceAlias`)
	if err != nil {
		return "", api.WrapError(api.ErrCodeTUNUnavailable, "读取默认出口接口失败", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func (c *windowsController) ValidateTUN(ctx context.Context) error {
	if _, err := c.resolveWintunDirectory(); err != nil {
		return err
	}
	if _, err := c.snapshotDNSServers(ctx); err != nil {
		return api.WrapError(api.ErrCodeTUNUnavailable, "读取系统 DNS 快照失败", err)
	}
	return nil
}

func (c *windowsController) StartTUN(ctx context.Context, opts TUNOptions) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.tun != nil {
		return api.NewError(api.ErrCodeRuntimeAlreadyRunning, "TUN 已经启动")
	}

	workdir, err := c.resolveWintunDirectory()
	if err != nil {
		return err
	}
	dnsSnapshot, err := c.snapshotDNSServers(ctx)
	if err != nil {
		return api.WrapError(api.ErrCodeTUNUnavailable, "读取系统 DNS 快照失败", err)
	}

	helper, err := startTUNHelper(ctx, c.logger, tunHelperOptions{
		Device:           windowsTUNDevice,
		Proxy:            "socks5://" + opts.SOCKSListenAddress,
		Interface:        opts.EgressInterface,
		LogLevel:         "info",
		MTU:              maxInt(opts.MTU, 1500),
		UDPTimeout:       30 * time.Second,
		WorkingDirectory: workdir,
	})
	if err != nil {
		return err
	}

	success := false
	tunInterface := ""
	defer func() {
		if success {
			return
		}
		c.cleanupFailedStart(ctx, helper, tunInterface, dnsSnapshot)
	}()

	tunInterface, err = helper.WaitReady(10 * time.Second)
	if err != nil {
		return err
	}
	if err := c.configureTUNInterface(ctx, tunInterface); err != nil {
		return api.WrapError(api.ErrCodeTUNUnavailable, "配置 Wintun 接口失败", err)
	}
	if err := c.installSplitRoutes(ctx, tunInterface); err != nil {
		return api.WrapError(api.ErrCodeTUNUnavailable, "写入 TUN 路由失败", err)
	}
	if err := c.applyLocalDNS(ctx, dnsSnapshot, opts.DNSListenAddress); err != nil {
		return api.WrapError(api.ErrCodeSystemProxyFailed, "切换系统 DNS 到本地解析器失败", err)
	}

	c.tun = helper
	c.tunInterface = tunInterface
	c.dnsSnapshot = dnsSnapshot
	success = true

	c.logger.Info("platform.windows", "TUN 已启动", map[string]any{
		"interface": tunInterface,
		"dns":       opts.DNSListenAddress,
	})
	return nil
}

func (c *windowsController) StopTUN(ctx context.Context) error {
	c.mu.Lock()
	helper := c.tun
	tunInterface := c.tunInterface
	dnsSnapshot := c.dnsSnapshot
	c.tun = nil
	c.tunInterface = ""
	c.dnsSnapshot = nil
	c.mu.Unlock()

	if helper == nil {
		return nil
	}

	var firstErr error
	if err := c.restoreDNSServers(ctx, dnsSnapshot); err != nil && firstErr == nil {
		firstErr = api.WrapError(api.ErrCodeSystemProxyFailed, "恢复系统 DNS 失败", err)
	}
	if err := c.removeSplitRoutes(ctx, tunInterface); err != nil && firstErr == nil {
		firstErr = api.WrapError(api.ErrCodeTUNUnavailable, "删除 TUN 路由失败", err)
	}
	if err := c.removeTUNAddress(ctx, tunInterface); err != nil && firstErr == nil {
		firstErr = api.WrapError(api.ErrCodeTUNUnavailable, "删除 TUN 地址失败", err)
	}
	if err := helper.Stop(ctx); err != nil && !errors.Is(err, context.Canceled) && firstErr == nil {
		firstErr = api.WrapError(api.ErrCodeTUNUnavailable, "停止 TUN helper 失败", err)
	}
	return firstErr
}

func (c *windowsController) readSystemProxySnapshot() (windowsSystemProxySnapshot, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsPath, registry.QUERY_VALUE)
	if err != nil {
		return windowsSystemProxySnapshot{}, api.WrapError(api.ErrCodeSystemProxyFailed, "读取系统代理配置失败", err)
	}
	defer key.Close()

	proxyEnable, _, err := key.GetIntegerValue("ProxyEnable")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return windowsSystemProxySnapshot{}, api.WrapError(api.ErrCodeSystemProxyFailed, "读取 ProxyEnable 失败", err)
	}
	server, _, err := key.GetStringValue("ProxyServer")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return windowsSystemProxySnapshot{}, api.WrapError(api.ErrCodeSystemProxyFailed, "读取 ProxyServer 失败", err)
	}

	snapshot := windowsSystemProxySnapshot{
		Enabled: proxyEnable == 1,
		Server:  strings.TrimSpace(server),
	}
	applyProxyServer(&snapshot, snapshot.Server)
	return snapshot, nil
}

func restoreProxyServerString(snapshot windowsSystemProxySnapshot) string {
	if snapshot.Server != "" {
		return snapshot.Server
	}
	parts := make([]string, 0, 3)
	if snapshot.HTTPAddress != "" {
		parts = append(parts, "http="+snapshot.HTTPAddress)
	}
	if snapshot.HTTPSAddress != "" {
		parts = append(parts, "https="+snapshot.HTTPSAddress)
	}
	if snapshot.SOCKSAddress != "" {
		parts = append(parts, "socks="+snapshot.SOCKSAddress)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ";")
}

func applyProxyServer(target *windowsSystemProxySnapshot, server string) {
	server = strings.TrimSpace(server)
	if server == "" {
		return
	}
	if !strings.Contains(server, "=") {
		target.HTTPAddress = server
		target.HTTPSAddress = server
		target.SOCKSAddress = server
		return
	}
	for _, part := range strings.Split(server, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "http":
			target.HTTPAddress = strings.TrimSpace(value)
		case "https":
			target.HTTPSAddress = strings.TrimSpace(value)
		case "socks", "socks5":
			target.SOCKSAddress = strings.TrimSpace(value)
		}
	}
}

func (c *windowsController) restoreSystemProxy(snapshot windowsSystemProxySnapshot) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, internetSettingsPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()

	enabled := uint32(0)
	if snapshot.Enabled {
		enabled = 1
	}
	if err := key.SetDWordValue("ProxyEnable", enabled); err != nil {
		return err
	}
	server := restoreProxyServerString(snapshot)
	if server == "" {
		_ = key.DeleteValue("ProxyServer")
		return nil
	}
	return key.SetStringValue("ProxyServer", server)
}

func (c *windowsController) cleanupFailedStart(ctx context.Context, helper *tunHelperProcess, tunInterface string, dnsSnapshot []windowsDNSState) {
	_ = c.restoreDNSServers(ctx, dnsSnapshot)
	_ = c.removeSplitRoutes(ctx, tunInterface)
	_ = c.removeTUNAddress(ctx, tunInterface)
	if helper != nil {
		_ = helper.Stop(ctx)
	}
}

func (c *windowsController) resolveWintunDirectory() (string, error) {
	candidates := make([]string, 0, 2)
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd)
	}
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Dir(executable))
	}
	seen := map[string]struct{}{}
	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		if _, err := os.Stat(filepath.Join(dir, "wintun.dll")); err == nil {
			return dir, nil
		}
	}
	return "", api.NewError(api.ErrCodeTUNUnavailable, "缺少 wintun.dll，无法启动 Windows TUN")
}

func (c *windowsController) snapshotDNSServers(ctx context.Context) ([]windowsDNSState, error) {
	output, err := runPowerShell(ctx, `Get-DnsClientServerAddress -AddressFamily IPv4 | Where-Object { $_.InterfaceAlias -and $_.InterfaceAlias -notlike 'Loopback*' } | Select-Object InterfaceAlias,ServerAddresses | ConvertTo-Json -Compress`)
	if err != nil {
		return nil, err
	}
	return decodeDNSStateJSON(output)
}

func (c *windowsController) applyLocalDNS(ctx context.Context, snapshot []windowsDNSState, dnsListenAddress string) error {
	host, _, err := splitHostPort(dnsListenAddress)
	if err != nil {
		return err
	}
	for _, state := range snapshot {
		if state.InterfaceAlias == "" {
			continue
		}
		script := fmt.Sprintf("Set-DnsClientServerAddress -InterfaceAlias '%s' -ServerAddresses @('%s')", psQuote(state.InterfaceAlias), psQuote(host))
		if _, err := runPowerShell(ctx, script); err != nil {
			return err
		}
	}
	return nil
}

func (c *windowsController) restoreDNSServers(ctx context.Context, snapshot []windowsDNSState) error {
	var firstErr error
	for _, state := range snapshot {
		if state.InterfaceAlias == "" {
			continue
		}
		var script string
		if len(state.ServerAddresses) == 0 {
			script = fmt.Sprintf("Set-DnsClientServerAddress -InterfaceAlias '%s' -ResetServerAddresses", psQuote(state.InterfaceAlias))
		} else {
			servers := make([]string, 0, len(state.ServerAddresses))
			for _, server := range state.ServerAddresses {
				servers = append(servers, "'"+psQuote(server)+"'")
			}
			script = fmt.Sprintf("Set-DnsClientServerAddress -InterfaceAlias '%s' -ServerAddresses @(%s)", psQuote(state.InterfaceAlias), strings.Join(servers, ","))
		}
		if _, err := runPowerShell(ctx, script); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (c *windowsController) configureTUNInterface(ctx context.Context, tunInterface string) error {
	script := fmt.Sprintf(`
$existing = Get-NetIPAddress -InterfaceAlias '%s' -AddressFamily IPv4 -ErrorAction SilentlyContinue | Where-Object { $_.IPAddress -eq '%s' }
if (-not $existing) {
  New-NetIPAddress -InterfaceAlias '%s' -IPAddress '%s' -PrefixLength 15 -AddressFamily IPv4 | Out-Null
}
`, psQuote(tunInterface), windowsTUNAddress, psQuote(tunInterface), windowsTUNAddress)
	_, err := runPowerShell(ctx, script)
	return err
}

func (c *windowsController) removeTUNAddress(ctx context.Context, tunInterface string) error {
	if tunInterface == "" {
		return nil
	}
	script := fmt.Sprintf(`
Get-NetIPAddress -InterfaceAlias '%s' -AddressFamily IPv4 -ErrorAction SilentlyContinue | Where-Object { $_.IPAddress -eq '%s' } | Remove-NetIPAddress -Confirm:$false -ErrorAction SilentlyContinue
`, psQuote(tunInterface), windowsTUNAddress)
	_, err := runPowerShell(ctx, script)
	return err
}

func (c *windowsController) installSplitRoutes(ctx context.Context, tunInterface string) error {
	for _, prefix := range windowsSplitRoutes {
		script := fmt.Sprintf(`
$existing = Get-NetRoute -InterfaceAlias '%s' -DestinationPrefix '%s' -ErrorAction SilentlyContinue
if (-not $existing) {
  New-NetRoute -InterfaceAlias '%s' -DestinationPrefix '%s' -NextHop '0.0.0.0' -RouteMetric 1 | Out-Null
}
`, psQuote(tunInterface), prefix, psQuote(tunInterface), prefix)
		if _, err := runPowerShell(ctx, script); err != nil {
			return err
		}
	}
	return nil
}

func (c *windowsController) removeSplitRoutes(ctx context.Context, tunInterface string) error {
	if tunInterface == "" {
		return nil
	}
	var firstErr error
	for _, prefix := range windowsSplitRoutes {
		script := fmt.Sprintf("Remove-NetRoute -InterfaceAlias '%s' -DestinationPrefix '%s' -Confirm:$false -ErrorAction SilentlyContinue", psQuote(tunInterface), prefix)
		if _, err := runPowerShell(ctx, script); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func runPowerShell(ctx context.Context, script string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return output, nil
	}
	trimmed := strings.TrimSpace(string(output))
	if trimmed != "" {
		return nil, fmt.Errorf("%w: %s", err, trimmed)
	}
	return nil, err
}

func decodeDNSStateJSON(raw []byte) ([]windowsDNSState, error) {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] == '{' {
		var single windowsDNSState
		if err := json.Unmarshal(raw, &single); err != nil {
			return nil, err
		}
		return []windowsDNSState{single}, nil
	}
	var many []windowsDNSState
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, err
	}
	return many, nil
}

func psQuote(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}
