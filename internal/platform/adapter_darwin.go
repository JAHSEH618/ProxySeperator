//go:build darwin

package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
	"github.com/friedhelmliu/ProxySeperator/internal/logging"
)

const (
	darwinTUNAddress = "198.18.0.1"
	darwinTUNDevice  = "tun://utun"
)

var (
	darwinNameserverPattern    = regexp.MustCompile(`nameserver\[\d+\]\s*:\s*(\S+)`)
	darwinDefaultIfacePattern  = regexp.MustCompile(`interface:\s+(\S+)`)
	darwinServiceDevicePattern = regexp.MustCompile(`Device:\s*([^)]+)`)
	darwinFakeTUNPrefix        = netip.MustParsePrefix("198.18.0.0/15")
	darwinSplitRoutes          = []string{"0.0.0.0/1", "128.0.0.0/1"}
)

type darwinProxyEntry struct {
	Enabled bool   `json:"enabled"`
	Address string `json:"address,omitempty"`
}

type darwinServiceProxyState struct {
	Service string           `json:"service"`
	Web     darwinProxyEntry `json:"web"`
	Secure  darwinProxyEntry `json:"secure"`
	SOCKS   darwinProxyEntry `json:"socks"`
}

type darwinController struct {
	logger *logging.Logger

	mu           sync.Mutex
	tun          *tunHelperProcess
	tunInterface string
	dnsSnapshot  map[string][]string
}

func NewController(logger *logging.Logger) Controller {
	return &darwinController{logger: logger}
}

func (c *darwinController) ApplySystemProxy(ctx context.Context, cfg SystemProxyConfig) error {
	services, err := c.listNetworkServices(ctx)
	if err != nil {
		return err
	}
	hostHTTP, portHTTP, err := splitHostPort(cfg.HTTPAddress)
	if err != nil {
		return api.WrapError(api.ErrCodeSystemProxyFailed, "无效的 HTTP 代理地址", err)
	}
	hostSOCKS, portSOCKS, err := splitHostPort(cfg.SOCKSAddress)
	if err != nil {
		return api.WrapError(api.ErrCodeSystemProxyFailed, "无效的 SOCKS 代理地址", err)
	}

	var succeeded int
	var lastErr error
	for _, service := range services {
		if err := c.applyProxyForService(ctx, service, hostHTTP, portHTTP, hostSOCKS, portSOCKS); err != nil {
			c.logger.Warn("platform", "设置代理失败，跳过此服务", map[string]any{
				"service": service,
				"error":   err.Error(),
			})
			lastErr = err
			continue
		}
		succeeded++
	}

	if succeeded == 0 && lastErr != nil {
		return api.WrapError(api.ErrCodeSystemProxyFailed, "所有网络服务设置代理均失败", lastErr)
	}
	if lastErr != nil {
		c.logger.Warn("platform", "部分网络服务设置代理失败", map[string]any{
			"succeeded": succeeded,
			"total":     len(services),
		})
	}
	return nil
}

func (c *darwinController) applyProxyForService(ctx context.Context, service, hostHTTP, portHTTP, hostSOCKS, portSOCKS string) error {
	if err := run(ctx, "networksetup", "-setwebproxy", service, hostHTTP, portHTTP); err != nil {
		return err
	}
	if err := run(ctx, "networksetup", "-setsecurewebproxy", service, hostHTTP, portHTTP); err != nil {
		return err
	}
	if err := run(ctx, "networksetup", "-setsocksfirewallproxy", service, hostSOCKS, portSOCKS); err != nil {
		return err
	}
	_ = run(ctx, "networksetup", "-setwebproxystate", service, "on")
	_ = run(ctx, "networksetup", "-setsecurewebproxystate", service, "on")
	_ = run(ctx, "networksetup", "-setsocksfirewallproxystate", service, "on")
	return nil
}

func (c *darwinController) ClearSystemProxy(ctx context.Context) error {
	services, err := c.listNetworkServices(ctx)
	if err != nil {
		return err
	}
	for _, service := range services {
		_ = run(ctx, "networksetup", "-setwebproxystate", service, "off")
		_ = run(ctx, "networksetup", "-setsecurewebproxystate", service, "off")
		_ = run(ctx, "networksetup", "-setsocksfirewallproxystate", service, "off")
	}
	return nil
}

func (c *darwinController) PreferredCompanyBypassInterface(ctx context.Context) (string, error) {
	if iface := c.detectLikelyCompanyVPNInterface(); iface != "" {
		return iface, nil
	}
	output, err := exec.CommandContext(ctx, "networksetup", "-listnetworkserviceorder").CombinedOutput()
	if err == nil {
		for _, match := range darwinServiceDevicePattern.FindAllStringSubmatch(string(output), -1) {
			if len(match) != 2 {
				continue
			}
			device := strings.TrimSpace(match[1])
			if device == "" || device == "lo0" || strings.HasPrefix(device, "utun") {
				continue
			}
			return device, nil
		}
	}
	return c.DefaultEgressInterface(ctx)
}

func (c *darwinController) detectLikelyCompanyVPNInterface() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || !strings.HasPrefix(iface.Name, "utun") {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ip, ok := addrIP(addr)
			if !ok || ip.IsLoopback() {
				continue
			}
			if v4 := ip.To4(); v4 != nil {
				candidate, ok := netip.AddrFromSlice(v4)
				if !ok || darwinFakeTUNPrefix.Contains(candidate) {
					continue
				}
				if candidate.IsPrivate() {
					return iface.Name
				}
			}
		}
	}
	return ""
}

func addrIP(addr net.Addr) (net.IP, bool) {
	switch value := addr.(type) {
	case *net.IPNet:
		return value.IP, true
	case *net.IPAddr:
		return value.IP, true
	default:
		return nil, false
	}
}

func (c *darwinController) ApplyCompanyBypassRoutes(ctx context.Context, iface string, routes []string) error {
	if iface == "" || len(routes) == 0 {
		return nil
	}
	output, err := runPrivilegedScript(ctx, buildApplyCompanyBypassRoutesScript(iface, routes))
	if err != nil {
		return wrapPrivilegedCommandError("安装公司旁路路由失败", err, output)
	}
	return nil
}

func (c *darwinController) ClearCompanyBypassRoutes(ctx context.Context, iface string, routes []string) error {
	if iface == "" || len(routes) == 0 {
		return nil
	}
	output, err := runPrivilegedScript(ctx, buildClearCompanyBypassRoutesScript(iface, routes))
	if err != nil {
		return wrapPrivilegedCommandError("清理公司旁路路由失败", err, output)
	}
	return nil
}

func (c *darwinController) EnableAutoStart(ctx context.Context, executablePath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return api.WrapError(api.ErrCodePermissionDenied, "无法获取用户目录", err)
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return api.WrapError(api.ErrCodePermissionDenied, "无法创建 LaunchAgents 目录", err)
	}
	plistPath := filepath.Join(dir, "com.proxyseparator.app.plist")
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key>
    <string>com.proxyseparator.app</string>
    <key>ProgramArguments</key>
    <array>
      <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
  </dict>
</plist>
`, executablePath)
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return api.WrapError(api.ErrCodePermissionDenied, "写入 LaunchAgent 失败", err)
	}
	return nil
}

func (c *darwinController) DisableAutoStart(context.Context) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return api.WrapError(api.ErrCodePermissionDenied, "无法获取用户目录", err)
	}
	plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.proxyseparator.app.plist")
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return api.WrapError(api.ErrCodePermissionDenied, "删除 LaunchAgent 失败", err)
	}
	return nil
}

func (c *darwinController) CurrentSystemProxy(ctx context.Context) (api.SystemProxyState, error) {
	if state, ok, err := c.currentEffectiveSystemProxy(ctx); err == nil && ok {
		return state, nil
	}
	states, err := c.captureSystemProxySnapshot(ctx)
	if err != nil {
		return api.SystemProxyState{}, err
	}
	return summarizeDarwinProxyState(states), nil
}

func (c *darwinController) currentEffectiveSystemProxy(ctx context.Context) (api.SystemProxyState, bool, error) {
	output, err := exec.CommandContext(ctx, "scutil", "--proxy").CombinedOutput()
	if err != nil {
		return api.SystemProxyState{}, false, fmt.Errorf("read scutil proxy: %w", err)
	}

	fields := map[string]string{}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "<dictionary> {" || line == "}" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}

	state := api.SystemProxyState{}
	if fields["HTTPEnable"] == "1" {
		state.Enabled = true
		state.HTTPAddress = joinProxyAddress(fields["HTTPProxy"], fields["HTTPPort"])
	}
	if fields["HTTPSEnable"] == "1" {
		state.Enabled = true
		state.HTTPSAddress = joinProxyAddress(fields["HTTPSProxy"], fields["HTTPSPort"])
	}
	if fields["SOCKSEnable"] == "1" {
		state.Enabled = true
		state.SOCKSAddress = joinProxyAddress(fields["SOCKSProxy"], fields["SOCKSPort"])
	}
	if !state.Enabled {
		return api.SystemProxyState{}, false, nil
	}
	return state, true, nil
}

func joinProxyAddress(host, port string) string {
	host = strings.TrimSpace(host)
	port = strings.TrimSpace(port)
	if host == "" || port == "" {
		return ""
	}
	return net.JoinHostPort(host, port)
}

func (c *darwinController) CurrentDNSResolvers(ctx context.Context) ([]string, error) {
	output, err := exec.CommandContext(ctx, "scutil", "--dns").Output()
	if err != nil {
		return nil, fmt.Errorf("read scutil dns: %w", err)
	}
	seen := map[string]struct{}{}
	resolvers := make([]string, 0)
	for _, match := range darwinNameserverPattern.FindAllStringSubmatch(string(output), -1) {
		resolver := strings.TrimSpace(match[1])
		if resolver == "" || strings.HasPrefix(resolver, "127.") || resolver == "::1" {
			continue
		}
		if _, ok := seen[resolver]; ok {
			continue
		}
		seen[resolver] = struct{}{}
		resolvers = append(resolvers, resolver)
	}
	if len(resolvers) == 0 {
		return nil, fmt.Errorf("no active DNS resolver detected")
	}
	return resolvers, nil
}

func (c *darwinController) CaptureRecoverySnapshot(ctx context.Context, mode string) (api.RecoverySnapshot, error) {
	proxyStates, err := c.captureSystemProxySnapshot(ctx)
	if err != nil {
		return api.RecoverySnapshot{}, err
	}
	proxyData, err := json.Marshal(proxyStates)
	if err != nil {
		return api.RecoverySnapshot{}, err
	}
	services, err := c.listNetworkServices(ctx)
	if err != nil {
		return api.RecoverySnapshot{}, err
	}
	dnsSnapshot, err := c.snapshotDNSServers(ctx, services)
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
			Routes:    append([]string(nil), darwinSplitRoutes...),
		}
	}
	c.mu.Unlock()

	return api.RecoverySnapshot{
		Platform:        "darwin",
		Mode:            mode,
		SystemProxy:     summarizeDarwinProxyState(proxyStates),
		SystemProxyData: proxyData,
		DNSState:        dnsData,
		TUNState:        tunState,
	}, nil
}

func (c *darwinController) RecoverNetwork(ctx context.Context, snapshot api.RecoverySnapshot) error {
	c.mu.Lock()
	helper := c.tun
	c.tun = nil
	c.tunInterface = ""
	c.dnsSnapshot = nil
	c.mu.Unlock()

	// 恢复系统代理（非特权操作，system 和 TUN 模式都可能修改）
	if len(snapshot.SystemProxyData) > 0 {
		var states []darwinServiceProxyState
		if err := json.Unmarshal(snapshot.SystemProxyData, &states); err != nil {
			return api.WrapError(api.ErrCodeRecoveryFailed, "解析系统代理快照失败", err)
		}
		if err := c.restoreSystemProxySnapshot(ctx, states); err != nil {
			return api.WrapError(api.ErrCodeRecoveryFailed, "恢复系统代理失败", err)
		}
	}

	// DNS 和路由仅在 TUN 成功启动后才会被修改。
	// TUN 启动后 tunState.Interface 会被填入接口名（如 utun8）。
	// 如果 tunState.Interface 为空，说明 TUN 从未成功启动，DNS 和路由从未被修改，
	// 无需执行需要管理员特权的恢复操作。
	tunInterface := snapshot.TUNState.Interface
	if tunInterface != "" {
		var dnsSnapshot map[string][]string
		if len(snapshot.DNSState) > 0 {
			if err := json.Unmarshal(snapshot.DNSState, &dnsSnapshot); err != nil {
				return api.WrapError(api.ErrCodeRecoveryFailed, "解析 DNS 快照失败", err)
			}
		}
		// 单次特权脚本恢复 DNS + 路由 + 接口
		script := buildRecoverTUNScript(tunInterface, dnsSnapshot)
		output, err := runPrivilegedScript(ctx, script)
		if err != nil {
			return api.WrapError(api.ErrCodeRecoveryFailed,
				"恢复系统网络设置失败",
				wrapCommandError(err, output))
		}
	}
	if snapshot.CompanyBypass.Interface != "" && len(snapshot.CompanyBypass.Routes) > 0 {
		output, err := runPrivilegedScript(ctx, buildClearCompanyBypassRoutesScript(snapshot.CompanyBypass.Interface, snapshot.CompanyBypass.Routes))
		if err != nil {
			return api.WrapError(api.ErrCodeRecoveryFailed,
				"恢复公司旁路路由失败",
				wrapCommandError(err, output))
		}
	}

	if helper != nil {
		if err := helper.Stop(ctx); err != nil && !errors.Is(err, context.Canceled) {
			return api.WrapError(api.ErrCodeRecoveryFailed, "停止残留 TUN helper 失败", err)
		}
	}
	return nil
}

func (c *darwinController) DefaultEgressInterface(ctx context.Context) (string, error) {
	output, err := exec.CommandContext(ctx, "route", "-n", "get", "default").CombinedOutput()
	if err != nil {
		return "", api.WrapError(api.ErrCodeTUNUnavailable, "读取默认出口接口失败", err)
	}
	matches := darwinDefaultIfacePattern.FindStringSubmatch(string(output))
	if len(matches) != 2 {
		return "", api.NewError(api.ErrCodeTUNUnavailable, "未识别默认出口接口")
	}
	return strings.TrimSpace(matches[1]), nil
}

func (c *darwinController) ValidateTUN(ctx context.Context) error {
	if _, err := c.listNetworkServices(ctx); err != nil {
		return err
	}
	if _, err := c.CurrentDNSResolvers(ctx); err != nil {
		return api.WrapError(api.ErrCodeTUNUnavailable, "读取系统 DNS 失败", err)
	}
	return nil
}

func (c *darwinController) StartTUN(ctx context.Context, opts TUNOptions) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.tun != nil {
		return api.NewError(api.ErrCodeRuntimeAlreadyRunning, "TUN 已经启动")
	}

	// Non-privileged preparation
	services, err := c.listNetworkServices(ctx)
	if err != nil {
		return err
	}
	dnsSnapshot, err := c.snapshotDNSServers(ctx, services)
	if err != nil {
		return api.WrapError(api.ErrCodeTUNUnavailable, "读取系统 DNS 快照失败", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return api.WrapError(api.ErrCodeTUNUnavailable, "无法定位当前可执行文件", err)
	}
	dnsHost, _, err := splitHostPort(opts.DNSListenAddress)
	if err != nil {
		return api.WrapError(api.ErrCodeTUNUnavailable, "无效的 DNS 监听地址", err)
	}

	// Prepare TUN helper args
	helperArgs := []string{
		"tun-helper",
		"--device", darwinTUNDevice,
		"--proxy", "socks5://" + opts.SOCKSListenAddress,
		"--loglevel", "info",
		"--mtu", fmt.Sprintf("%d", maxInt(opts.MTU, 1500)),
		"--udp-timeout", (30 * time.Second).String(),
	}
	if opts.EgressInterface != "" {
		helperArgs = append(helperArgs, "--interface", opts.EgressInterface)
	}

	// Create temp files for TUN helper output
	stdoutFile, err := os.CreateTemp("", "proxyseparator-tun-stdout-*.log")
	if err != nil {
		return api.WrapError(api.ErrCodeTUNUnavailable, "无法创建 TUN helper 输出文件", err)
	}
	stdoutPath := stdoutFile.Name()
	_ = stdoutFile.Close()

	stderrFile, err := os.CreateTemp("", "proxyseparator-tun-stderr-*.log")
	if err != nil {
		_ = os.Remove(stdoutPath)
		return api.WrapError(api.ErrCodeTUNUnavailable, "无法创建 TUN helper 错误输出文件", err)
	}
	stderrPath := stderrFile.Name()
	_ = stderrFile.Close()

	// Build and execute combined script — SINGLE password prompt
	script := buildStartTUNScript(executable, helperArgs, stdoutPath, stderrPath, services, dnsHost, dnsSnapshot)
	output, err := runPrivilegedScript(ctx, script)
	if err != nil {
		stderrContent, _ := os.ReadFile(stderrPath)
		c.logger.Error("platform.darwin", "TUN 特权脚本执行失败", map[string]any{
			"output": strings.TrimSpace(string(output)),
			"stderr": strings.TrimSpace(string(stderrContent)),
			"error":  err.Error(),
		})
		_ = os.Remove(stdoutPath)
		_ = os.Remove(stderrPath)
		return wrapPrivilegedCommandError("未授予 macOS 管理员权限，无法启动 TUN", err, output)
	}

	// Parse result
	trimmedOutput := strings.TrimSpace(string(output))
	pid, tunInterface, parseErr := parseStartTUNResult(trimmedOutput)
	if parseErr != nil {
		// Read stderr for additional diagnostics
		stderrContent, _ := os.ReadFile(stderrPath)
		c.logger.Error("platform.darwin", "TUN 启动脚本输出解析失败", map[string]any{
			"stdout": trimmedOutput,
			"stderr": strings.TrimSpace(string(stderrContent)),
			"error":  parseErr.Error(),
		})
		// The privileged script exited successfully but output is unparseable.
		// TUN setup may be partially or fully complete — attempt cleanup to
		// avoid leaving the system in a broken network state.
		stdoutContent, _ := os.ReadFile(stdoutPath)
		ifaceName := extractTUNReadyInterface(string(stdoutContent))
		if ifaceName != "" {
			c.logger.Warn("platform.darwin", "TUN 输出解析失败但接口已创建，正在清理", map[string]any{"interface": ifaceName})
			cleanScript := buildStopTUNScript(ifaceName, 0, dnsSnapshot)
			if cleanOut, cleanErr := runPrivilegedScript(ctx, cleanScript); cleanErr != nil {
				c.logger.Error("platform.darwin", "清理失败启动的 TUN 残留失败", map[string]any{
					"error":  cleanErr.Error(),
					"output": strings.TrimSpace(string(cleanOut)),
				})
			}
		}
		_ = os.Remove(stdoutPath)
		_ = os.Remove(stderrPath)
		return api.WrapError(api.ErrCodeTUNUnavailable, "TUN 启动脚本返回异常", parseErr)
	}

	// Create tunHelperProcess for lifecycle management
	process := &tunHelperProcess{
		logger:     c.logger,
		readyCh:    make(chan string, 1),
		doneCh:     make(chan struct{}),
		pid:        pid,
		privileged: true,
		stdoutPath: stdoutPath,
		stderrPath: stderrPath,
	}
	go process.pumpFile(stdoutPath, "stdout")
	go process.pumpFile(stderrPath, "stderr")
	go process.waitPrivilegedExit()

	c.tun = process
	c.tunInterface = tunInterface
	c.dnsSnapshot = dnsSnapshot

	c.logger.Info("platform.darwin", "TUN 已启动", map[string]any{
		"interface": tunInterface,
		"dns":       opts.DNSListenAddress,
	})
	return nil
}

func (c *darwinController) StopTUN(ctx context.Context) error {
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

	script := buildStopTUNScript(tunInterface, helper.pid, dnsSnapshot)
	output, err := runPrivilegedScript(ctx, script)

	// Wait for helper process to exit (the script sent SIGTERM/SIGKILL)
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer waitCancel()
	select {
	case <-helper.doneCh:
	case <-waitCtx.Done():
	}
	helper.cleanupTempFiles()

	if err != nil {
		return wrapPrivilegedCommandError("停止 TUN 失败", err, output)
	}
	return nil
}

func (c *darwinController) cleanupFailedStart(ctx context.Context, helper *tunHelperProcess, tunInterface string, dnsSnapshot map[string][]string) {
	if tunInterface != "" {
		pid := 0
		if helper != nil {
			pid = helper.pid
		}
		script := buildStopTUNScript(tunInterface, pid, dnsSnapshot)
		_, _ = runPrivilegedScript(ctx, script)
	} else if helper != nil {
		_ = helper.Stop(ctx)
	}
	if helper != nil {
		select {
		case <-helper.doneCh:
		default:
		}
		helper.cleanupTempFiles()
	}
}

func (c *darwinController) snapshotDNSServers(ctx context.Context, services []string) (map[string][]string, error) {
	snapshot := make(map[string][]string, len(services))
	for _, service := range services {
		servers, err := c.getDNSServers(ctx, service)
		if err != nil {
			return nil, err
		}
		snapshot[service] = servers
	}
	return snapshot, nil
}

func (c *darwinController) getDNSServers(ctx context.Context, service string) ([]string, error) {
	output, err := exec.CommandContext(ctx, "networksetup", "-getdnsservers", service).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("get dns servers for %s: %w: %s", service, err, strings.TrimSpace(string(output)))
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 1 && strings.Contains(lines[0], "There aren't any DNS Servers set") {
		return nil, nil
	}
	servers := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		servers = append(servers, line)
	}
	return servers, nil
}

func (c *darwinController) captureSystemProxySnapshot(ctx context.Context) ([]darwinServiceProxyState, error) {
	services, err := c.listNetworkServices(ctx)
	if err != nil {
		return nil, err
	}
	states := make([]darwinServiceProxyState, 0, len(services))
	for _, service := range services {
		web, err := c.getProxyEntry(ctx, service, "-getwebproxy")
		if err != nil {
			return nil, err
		}
		secure, err := c.getProxyEntry(ctx, service, "-getsecurewebproxy")
		if err != nil {
			return nil, err
		}
		socks, err := c.getProxyEntry(ctx, service, "-getsocksfirewallproxy")
		if err != nil {
			return nil, err
		}
		states = append(states, darwinServiceProxyState{
			Service: service,
			Web:     web,
			Secure:  secure,
			SOCKS:   socks,
		})
	}
	return states, nil
}

func summarizeDarwinProxyState(states []darwinServiceProxyState) api.SystemProxyState {
	httpValues := make(map[string]struct{})
	httpsValues := make(map[string]struct{})
	socksValues := make(map[string]struct{})
	summary := api.SystemProxyState{}
	for _, state := range states {
		if state.Web.Enabled {
			summary.Enabled = true
			httpValues[state.Web.Address] = struct{}{}
		}
		if state.Secure.Enabled {
			summary.Enabled = true
			httpsValues[state.Secure.Address] = struct{}{}
		}
		if state.SOCKS.Enabled {
			summary.Enabled = true
			socksValues[state.SOCKS.Address] = struct{}{}
		}
	}
	if len(httpValues) == 1 {
		for value := range httpValues {
			summary.HTTPAddress = value
		}
	}
	if len(httpsValues) == 1 {
		for value := range httpsValues {
			summary.HTTPSAddress = value
		}
	}
	if len(socksValues) == 1 {
		for value := range socksValues {
			summary.SOCKSAddress = value
		}
	}
	if len(httpValues) > 1 || len(httpsValues) > 1 || len(socksValues) > 1 {
		summary.Mixed = true
	}
	return summary
}

func (c *darwinController) getProxyEntry(ctx context.Context, service, command string) (darwinProxyEntry, error) {
	output, err := exec.CommandContext(ctx, "networksetup", command, service).CombinedOutput()
	if err != nil {
		return darwinProxyEntry{}, api.WrapError(api.ErrCodeSystemProxyFailed, "读取系统代理状态失败", err)
	}
	entry := darwinProxyEntry{}
	lines := strings.Split(string(output), "\n")
	server := ""
	port := ""
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "Enabled:"):
			entry.Enabled = strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(line, "Enabled:")), "yes")
		case strings.HasPrefix(line, "Server:"):
			server = strings.TrimSpace(strings.TrimPrefix(line, "Server:"))
		case strings.HasPrefix(line, "Port:"):
			port = strings.TrimSpace(strings.TrimPrefix(line, "Port:"))
		}
	}
	if server != "" && port != "" {
		entry.Address = server + ":" + port
	}
	return entry, nil
}

func (c *darwinController) restoreSystemProxySnapshot(ctx context.Context, states []darwinServiceProxyState) error {
	for _, state := range states {
		if err := c.restoreServiceProxy(ctx, state); err != nil {
			return err
		}
	}
	return nil
}

func (c *darwinController) restoreServiceProxy(ctx context.Context, state darwinServiceProxyState) error {
	if err := c.applyProxyEntry(ctx, state.Service, state.Web, "-setwebproxy", "-setwebproxystate"); err != nil {
		return err
	}
	if err := c.applyProxyEntry(ctx, state.Service, state.Secure, "-setsecurewebproxy", "-setsecurewebproxystate"); err != nil {
		return err
	}
	if err := c.applyProxyEntry(ctx, state.Service, state.SOCKS, "-setsocksfirewallproxy", "-setsocksfirewallproxystate"); err != nil {
		return err
	}
	return nil
}

func (c *darwinController) applyProxyEntry(ctx context.Context, service string, entry darwinProxyEntry, setCommand string, stateCommand string) error {
	if entry.Enabled {
		host, port, err := splitHostPort(entry.Address)
		if err != nil {
			return api.WrapError(api.ErrCodeSystemProxyFailed, "恢复系统代理地址失败", err)
		}
		if err := run(ctx, "networksetup", setCommand, service, host, port); err != nil {
			return api.WrapError(api.ErrCodeSystemProxyFailed, "恢复系统代理失败", err)
		}
		if err := run(ctx, "networksetup", stateCommand, service, "on"); err != nil {
			return api.WrapError(api.ErrCodeSystemProxyFailed, "恢复系统代理状态失败", err)
		}
		return nil
	}
	if err := run(ctx, "networksetup", stateCommand, service, "off"); err != nil {
		return api.WrapError(api.ErrCodeSystemProxyFailed, "关闭系统代理状态失败", err)
	}
	return nil
}

func (c *darwinController) listNetworkServices(ctx context.Context) ([]string, error) {
	cmd := exec.CommandContext(ctx, "networksetup", "-listallnetworkservices")
	output, err := cmd.Output()
	if err != nil {
		return nil, api.WrapError(api.ErrCodeSystemProxyFailed, "读取网络服务列表失败", err)
	}
	lines := strings.Split(string(output), "\n")
	services := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "An asterisk") || strings.HasPrefix(line, "*") {
			continue
		}
		services = append(services, line)
	}
	if len(services) == 0 {
		return nil, api.NewError(api.ErrCodeSystemProxyFailed, "未检测到可用网络服务")
	}
	return services, nil
}

func run(ctx context.Context, name string, args ...string) error {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err == nil {
		return nil
	}
	if trimmed := strings.TrimSpace(string(output)); trimmed != "" {
		return fmt.Errorf("%w: %s", err, trimmed)
	}
	return err
}

func buildStartTUNScript(
	executable string,
	args []string,
	stdoutPath string,
	stderrPath string,
	services []string,
	dnsHost string,
	dnsSnapshot map[string][]string,
) string {
	var b strings.Builder
	b.WriteString("#!/bin/bash\n\n")

	// Cleanup function for rollback on failure
	b.WriteString("HELPER_PID=''\nIFACE=''\n\n")
	b.WriteString("cleanup() {\n")
	for _, service := range services {
		servers := dnsSnapshot[service]
		if len(servers) == 0 {
			fmt.Fprintf(&b, "  /usr/sbin/networksetup -setdnsservers %s Empty 2>/dev/null || true\n",
				shellQuote(service))
		} else {
			dnsArgs := shellQuote(service)
			for _, s := range servers {
				dnsArgs += " " + shellQuote(s)
			}
			fmt.Fprintf(&b, "  /usr/sbin/networksetup -setdnsservers %s 2>/dev/null || true\n", dnsArgs)
		}
	}
	b.WriteString("  if [ -n \"$IFACE\" ]; then\n")
	writeRouteCleanupForInterface(&b, "\"$IFACE\"", "    ")
	b.WriteString("    /sbin/ifconfig \"$IFACE\" down 2>/dev/null || true\n")
	b.WriteString("  fi\n")
	b.WriteString("  if [ -n \"$HELPER_PID\" ] && kill -0 \"$HELPER_PID\" 2>/dev/null; then\n")
	b.WriteString("    kill -15 \"$HELPER_PID\" 2>/dev/null || true\n")
	b.WriteString("    sleep 0.5\n")
	b.WriteString("    kill -9 \"$HELPER_PID\" 2>/dev/null || true\n")
	b.WriteString("  fi\n")
	b.WriteString("}\n\n")

	// Step 1: Launch TUN helper in background
	commandLine := buildShellCommand(executable, args...)
	fmt.Fprintf(&b, "%s > %s 2> %s &\n", commandLine, shellQuote(stdoutPath), shellQuote(stderrPath))
	b.WriteString("HELPER_PID=$!\n\n")

	// Step 2: Poll stdout file for TUN_READY <interface>
	b.WriteString("for i in $(seq 1 100); do\n")
	fmt.Fprintf(&b, "  LINE=$(grep -m1 '^TUN_READY ' %s 2>/dev/null || true)\n", shellQuote(stdoutPath))
	b.WriteString("  if [ -n \"$LINE\" ]; then\n")
	b.WriteString("    IFACE=$(echo \"$LINE\" | sed 's/^TUN_READY //' | tr -d '[:space:]')\n")
	b.WriteString("    break\n")
	b.WriteString("  fi\n")
	b.WriteString("  if ! kill -0 \"$HELPER_PID\" 2>/dev/null; then\n")
	b.WriteString("    echo 'ERROR:helper_exited'\n")
	b.WriteString("    exit 1\n")
	b.WriteString("  fi\n")
	b.WriteString("  sleep 0.1\n")
	b.WriteString("done\n\n")

	b.WriteString("if [ -z \"$IFACE\" ]; then\n")
	b.WriteString("  cleanup\n")
	b.WriteString("  echo 'ERROR:timeout'\n")
	b.WriteString("  exit 1\n")
	b.WriteString("fi\n\n")

	// Step 3: Configure TUN interface
	fmt.Fprintf(&b, "/sbin/ifconfig \"$IFACE\" inet %s %s up > /dev/null || {\n", darwinTUNAddress, darwinTUNAddress)
	b.WriteString("  cleanup\n")
	b.WriteString("  echo 'ERROR:ifconfig'\n")
	b.WriteString("  exit 1\n")
	b.WriteString("}\n\n")

	// Step 4: Install split routes
	for _, route := range darwinSplitRoutes {
		fmt.Fprintf(&b, "/sbin/route -n add -net %s -interface \"$IFACE\" > /dev/null || {\n", route)
		b.WriteString("  cleanup\n")
		b.WriteString("  echo 'ERROR:route'\n")
		b.WriteString("  exit 1\n")
		b.WriteString("}\n\n")
	}

	// Step 5: Set DNS for each service
	for _, service := range services {
		fmt.Fprintf(&b, "/usr/sbin/networksetup -setdnsservers %s %s > /dev/null || {\n",
			shellQuote(service), shellQuote(dnsHost))
		b.WriteString("  cleanup\n")
		b.WriteString("  echo 'ERROR:dns'\n")
		b.WriteString("  exit 1\n")
		b.WriteString("}\n\n")
	}

	// Step 6: Success
	b.WriteString("echo \"OK:${HELPER_PID}:${IFACE}\"\n")
	b.WriteString("exit 0\n")
	return b.String()
}

func parseStartTUNResult(output string) (int, string, error) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "OK:") {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			return 0, "", fmt.Errorf("malformed OK line: %q", line)
		}
		pid, err := strconv.Atoi(parts[1])
		if err != nil || pid <= 0 {
			return 0, "", fmt.Errorf("invalid PID in result: %q", parts[1])
		}
		iface := strings.TrimSpace(parts[2])
		if iface == "" {
			return 0, "", fmt.Errorf("empty interface name in result")
		}
		return pid, iface, nil
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ERROR:") {
			return 0, "", fmt.Errorf("script error: %s", strings.TrimPrefix(line, "ERROR:"))
		}
	}
	return 0, "", fmt.Errorf("no result in script output: %q", output)
}

func buildStopTUNScript(tunInterface string, pid int, dnsSnapshot map[string][]string) string {
	var b strings.Builder
	b.WriteString("#!/bin/bash\n\n")

	// Step 1: Restore DNS (best-effort, sorted for determinism)
	sortedServices := sortedMapKeys(dnsSnapshot)
	for _, service := range sortedServices {
		servers := dnsSnapshot[service]
		if len(servers) == 0 {
			fmt.Fprintf(&b, "/usr/sbin/networksetup -setdnsservers %s Empty 2>/dev/null || true\n",
				shellQuote(service))
		} else {
			dnsArgs := shellQuote(service)
			for _, s := range servers {
				dnsArgs += " " + shellQuote(s)
			}
			fmt.Fprintf(&b, "/usr/sbin/networksetup -setdnsservers %s 2>/dev/null || true\n", dnsArgs)
		}
	}

	// Step 2: Remove routes (comprehensive — includes TUN helper routes)
	if tunInterface != "" {
		writeRouteCleanupForInterface(&b, shellQuote(tunInterface), "")
		// Step 3: Bring interface down
		fmt.Fprintf(&b, "/sbin/ifconfig %s down 2>/dev/null || true\n", shellQuote(tunInterface))
	}

	// Step 4: Kill helper (SIGTERM, wait, SIGKILL)
	if pid > 0 {
		fmt.Fprintf(&b, "\nif kill -0 %d 2>/dev/null; then\n", pid)
		fmt.Fprintf(&b, "  kill -15 %d 2>/dev/null || true\n", pid)
		b.WriteString("  for i in $(seq 1 30); do\n")
		fmt.Fprintf(&b, "    kill -0 %d 2>/dev/null || break\n", pid)
		b.WriteString("    sleep 0.1\n")
		b.WriteString("  done\n")
		fmt.Fprintf(&b, "  kill -9 %d 2>/dev/null || true\n", pid)
		b.WriteString("fi\n")
	}

	b.WriteString("\necho 'OK'\n")
	return b.String()
}

func buildRecoverTUNScript(tunInterface string, dnsSnapshot map[string][]string) string {
	var b strings.Builder
	b.WriteString("#!/bin/bash\n\n")

	// Restore DNS (best-effort, sorted for determinism)
	sortedServices := sortedMapKeys(dnsSnapshot)
	for _, service := range sortedServices {
		servers := dnsSnapshot[service]
		if len(servers) == 0 {
			fmt.Fprintf(&b, "/usr/sbin/networksetup -setdnsservers %s Empty 2>/dev/null || true\n",
				shellQuote(service))
		} else {
			dnsArgs := shellQuote(service)
			for _, s := range servers {
				dnsArgs += " " + shellQuote(s)
			}
			fmt.Fprintf(&b, "/usr/sbin/networksetup -setdnsservers %s 2>/dev/null || true\n", dnsArgs)
		}
	}

	// Remove routes (comprehensive — includes TUN helper routes)
	writeRouteCleanupForInterface(&b, shellQuote(tunInterface), "")

	// Bring interface down
	fmt.Fprintf(&b, "/sbin/ifconfig %s down 2>/dev/null || true\n\n", shellQuote(tunInterface))

	b.WriteString("echo 'OK'\n")
	return b.String()
}

func buildApplyCompanyBypassRoutesScript(iface string, routes []string) string {
	var b strings.Builder
	b.WriteString("#!/bin/bash\n\n")
	sortedRoutes := append([]string(nil), routes...)
	sort.Strings(sortedRoutes)
	for _, prefix := range sortedRoutes {
		fmt.Fprintf(&b, "/sbin/route -n delete -net %s -interface %s 2>/dev/null || true\n", shellQuote(prefix), shellQuote(iface))
		fmt.Fprintf(&b, "/sbin/route -n add -net %s -interface %s >/dev/null || {\n", shellQuote(prefix), shellQuote(iface))
		b.WriteString("  echo 'ERROR:route'\n")
		b.WriteString("  exit 1\n")
		b.WriteString("}\n")
	}
	b.WriteString("\necho 'OK'\n")
	return b.String()
}

func buildClearCompanyBypassRoutesScript(iface string, routes []string) string {
	var b strings.Builder
	b.WriteString("#!/bin/bash\n\n")
	sortedRoutes := append([]string(nil), routes...)
	sort.Strings(sortedRoutes)
	for _, prefix := range sortedRoutes {
		fmt.Fprintf(&b, "/sbin/route -n delete -net %s -interface %s 2>/dev/null || true\n", shellQuote(prefix), shellQuote(iface))
	}
	b.WriteString("\necho 'OK'\n")
	return b.String()
}

// writeRouteCleanupForInterface writes shell commands that delete all routes
// through the specified TUN interface. shellIface is a shell expression that
// evaluates to the interface name (e.g. "\"$IFACE\"" or "'utun8'").
func writeRouteCleanupForInterface(b *strings.Builder, shellIface, indent string) {
	for _, route := range darwinSplitRoutes {
		fmt.Fprintf(b, "%s/sbin/route -n delete -net %s -interface %s 2>/dev/null || true\n",
			indent, route, shellIface)
	}
	// Also remove any routes the TUN helper may have added directly
	// (e.g. tun2socks adds granular routes like 1/8, 2/7, 4/6, 8/5, 16/4, 32/3, 64/2)
	fmt.Fprintf(b, "%s/usr/sbin/netstat -rnf inet 2>/dev/null | /usr/bin/grep -w %s | /usr/bin/awk '{print $1}' | while read -r _CLRT; do\n",
		indent, shellIface)
	fmt.Fprintf(b, "%s  /sbin/route -n delete \"$_CLRT\" -interface %s 2>/dev/null || true\n",
		indent, shellIface)
	fmt.Fprintf(b, "%sdone\n", indent)
}

func extractTUNReadyInterface(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, tunReadyPrefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, tunReadyPrefix))
		}
	}
	return ""
}

func buildPrivilegedKillScript(pid int) string {
	var b strings.Builder
	b.WriteString("#!/bin/bash\n")
	fmt.Fprintf(&b, "kill -15 %d 2>/dev/null || true\n", pid)
	b.WriteString("for i in $(seq 1 30); do\n")
	fmt.Fprintf(&b, "  kill -0 %d 2>/dev/null || exit 0\n", pid)
	b.WriteString("  sleep 0.1\n")
	b.WriteString("done\n")
	fmt.Fprintf(&b, "kill -9 %d 2>/dev/null || true\n", pid)
	b.WriteString("exit 0\n")
	return b.String()
}

func sortedMapKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
