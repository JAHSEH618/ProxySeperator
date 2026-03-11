package platform

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
	"github.com/friedhelmliu/ProxySeperator/internal/logging"
)

const tunReadyPrefix = "TUN_READY "

type tunHelperOptions struct {
	Device           string
	Proxy            string
	Interface        string
	LogLevel         string
	WorkingDirectory string
	MTU              int
	UDPTimeout       time.Duration
}

type tunHelperProcess struct {
	cmd    *exec.Cmd
	logger *logging.Logger

	readyCh chan string
	doneCh  chan struct{}

	pid        int
	privileged bool
	stdoutPath string
	stderrPath string

	mu      sync.RWMutex
	waitErr error
}

func startTUNHelper(ctx context.Context, logger *logging.Logger, opts tunHelperOptions) (*tunHelperProcess, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, api.WrapError(api.ErrCodeTUNUnavailable, "无法定位当前可执行文件", err)
	}
	if opts.Device == "" {
		return nil, api.NewError(api.ErrCodeTUNUnavailable, "缺少 TUN 设备配置")
	}
	if opts.Proxy == "" {
		return nil, api.NewError(api.ErrCodeTUNUnavailable, "缺少 TUN 代理出口配置")
	}
	if opts.MTU <= 0 {
		opts.MTU = 1500
	}
	if opts.LogLevel == "" {
		opts.LogLevel = "info"
	}
	if opts.UDPTimeout <= 0 {
		opts.UDPTimeout = 30 * time.Second
	}
	if opts.WorkingDirectory == "" {
		opts.WorkingDirectory = filepath.Dir(executable)
	}

	args := []string{
		"tun-helper",
		"--device", opts.Device,
		"--proxy", opts.Proxy,
		"--loglevel", opts.LogLevel,
		"--mtu", fmt.Sprintf("%d", opts.MTU),
		"--udp-timeout", opts.UDPTimeout.String(),
	}
	if opts.Interface != "" {
		args = append(args, "--interface", opts.Interface)
	}

	if runtime.GOOS == "darwin" {
		return startPrivilegedTUNHelper(ctx, logger, executable, args)
	}

	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = opts.WorkingDirectory

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, api.WrapError(api.ErrCodeTUNUnavailable, "无法创建 TUN helper 标准输出管道", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, api.WrapError(api.ErrCodeTUNUnavailable, "无法创建 TUN helper 标准错误管道", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, api.WrapError(api.ErrCodeTUNUnavailable, "启动 TUN helper 失败", err)
	}

	process := &tunHelperProcess{
		cmd:     cmd,
		logger:  logger,
		readyCh: make(chan string, 1),
		doneCh:  make(chan struct{}),
	}

	go process.pump(stdout, "stdout")
	go process.pump(stderr, "stderr")
	go func() {
		err := cmd.Wait()
		process.mu.Lock()
		process.waitErr = err
		process.mu.Unlock()
		close(process.doneCh)
	}()

	return process, nil
}

func startPrivilegedTUNHelper(ctx context.Context, logger *logging.Logger, executable string, args []string) (*tunHelperProcess, error) {
	stdoutFile, err := os.CreateTemp("", "proxyseparator-tun-stdout-*.log")
	if err != nil {
		return nil, api.WrapError(api.ErrCodeTUNUnavailable, "无法创建 TUN helper 输出文件", err)
	}
	stdoutPath := stdoutFile.Name()
	_ = stdoutFile.Close()

	stderrFile, err := os.CreateTemp("", "proxyseparator-tun-stderr-*.log")
	if err != nil {
		_ = os.Remove(stdoutPath)
		return nil, api.WrapError(api.ErrCodeTUNUnavailable, "无法创建 TUN helper 错误输出文件", err)
	}
	stderrPath := stderrFile.Name()
	_ = stderrFile.Close()

	commandLine := buildShellCommand(executable, args...)
	launchCommand := fmt.Sprintf("%s > %s 2> %s & echo $!", commandLine, shellQuote(stdoutPath), shellQuote(stderrPath))
	output, err := runDarwinPrivilegedShell(ctx, launchCommand)
	if err != nil {
		_ = os.Remove(stdoutPath)
		_ = os.Remove(stderrPath)
		wrapped := wrapPrivilegedCommandError("未授予 macOS 管理员权限，无法启动 TUN helper", err, output)
		if api.ErrorCode(wrapped) == api.ErrCodePermissionDenied {
			return nil, wrapped
		}
		return nil, api.WrapError(api.ErrCodeTUNUnavailable, "启动 TUN helper 失败", wrapped)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil || pid <= 0 {
		_ = os.Remove(stdoutPath)
		_ = os.Remove(stderrPath)
		return nil, api.WrapError(api.ErrCodeTUNUnavailable, "解析 TUN helper 进程号失败", err)
	}

	process := &tunHelperProcess{
		logger:     logger,
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

	return process, nil
}

func (p *tunHelperProcess) WaitReady(timeout time.Duration) (string, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case name := <-p.readyCh:
		if strings.TrimSpace(name) == "" {
			return "", api.NewError(api.ErrCodeTUNUnavailable, "TUN helper 未返回接口名")
		}
		return name, nil
	case <-p.doneCh:
		return "", api.WrapError(api.ErrCodeTUNUnavailable, "TUN helper 提前退出", p.waitErrValue())
	case <-timer.C:
		return "", api.NewError(api.ErrCodeTUNUnavailable, "等待 TUN 接口就绪超时")
	}
}

func (p *tunHelperProcess) Stop(ctx context.Context) error {
	if p == nil || p.cmd == nil {
		if p == nil || !p.privileged || p.pid <= 0 {
			return nil
		}
		if !p.isDone() {
			_ = runDarwinPrivilegedKill(ctx, p.pid, syscall.SIGTERM)
		}
		select {
		case <-p.doneCh:
			p.cleanupTempFiles()
			err := p.waitErrValue()
			if err == nil || errors.Is(err, os.ErrProcessDone) {
				return nil
			}
			return err
		case <-ctx.Done():
			_ = runDarwinPrivilegedKill(context.Background(), p.pid, syscall.SIGKILL)
			<-p.doneCh
			p.cleanupTempFiles()
			return ctx.Err()
		}
	}
	if !p.isDone() && p.cmd.Process != nil {
		if runtime.GOOS == "windows" {
			_ = p.cmd.Process.Kill()
		} else {
			_ = p.cmd.Process.Signal(syscall.SIGTERM)
		}
	}

	select {
	case <-p.doneCh:
		p.cleanupTempFiles()
		err := p.waitErrValue()
		if err == nil || errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 0 {
			return nil
		}
		return err
	case <-ctx.Done():
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		<-p.doneCh
		p.cleanupTempFiles()
		return ctx.Err()
	}
}

func (p *tunHelperProcess) pump(reader io.Reader, stream string) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		p.handleLine(line, stream)
	}
	if err := scanner.Err(); err != nil && p.logger != nil {
		p.logger.Warn("platform.tun", "读取 TUN helper 输出失败", map[string]any{
			"stream": stream,
			"error":  err.Error(),
		})
	}
}

func (p *tunHelperProcess) pumpFile(path, stream string) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var offset int64
	remainder := ""
	for {
		offset, remainder = p.readLogChunk(path, stream, offset, remainder, false)
		if p.isDone() {
			_, _ = p.readLogChunk(path, stream, offset, remainder, true)
			return
		}
		select {
		case <-ticker.C:
		case <-p.doneCh:
		}
	}
}

func (p *tunHelperProcess) readLogChunk(path, stream string, offset int64, remainder string, final bool) (int64, string) {
	file, err := os.Open(path)
	if err != nil {
		return offset, remainder
	}
	defer file.Close()

	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return offset, remainder
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return offset, remainder
	}
	offset += int64(len(data))
	if len(data) == 0 {
		return offset, remainder
	}

	text := remainder + string(data)
	lines := strings.Split(text, "\n")
	if !final && !strings.HasSuffix(text, "\n") {
		remainder = lines[len(lines)-1]
		lines = lines[:len(lines)-1]
	} else {
		remainder = ""
	}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		p.handleLine(line, stream)
	}
	return offset, remainder
}

func (p *tunHelperProcess) handleLine(line, stream string) {
	if strings.HasPrefix(line, tunReadyPrefix) {
		select {
		case p.readyCh <- strings.TrimSpace(strings.TrimPrefix(line, tunReadyPrefix)):
		default:
		}
		return
	}
	if p.logger != nil {
		p.logger.Info("platform.tun", "TUN helper 输出", map[string]any{
			"stream": stream,
			"line":   line,
		})
	}
}

func (p *tunHelperProcess) isDone() bool {
	select {
	case <-p.doneCh:
		return true
	default:
		return false
	}
}

func (p *tunHelperProcess) waitErrValue() error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.waitErr
}

func (p *tunHelperProcess) setWaitErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.waitErr = err
}

func (p *tunHelperProcess) waitPrivilegedExit() {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		running, err := processRunning(p.pid)
		if !running {
			p.setWaitErr(err)
			close(p.doneCh)
			return
		}
		<-ticker.C
	}
}

func (p *tunHelperProcess) cleanupTempFiles() {
	if p.stdoutPath != "" {
		_ = os.Remove(p.stdoutPath)
	}
	if p.stderrPath != "" {
		_ = os.Remove(p.stderrPath)
	}
}

func processRunning(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	err := syscall.Kill(pid, syscall.Signal(0))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.EPERM):
		return true, nil
	case errors.Is(err, syscall.ESRCH):
		return false, nil
	default:
		return false, err
	}
}

func buildShellCommand(name string, args ...string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, shellQuote(name))
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func escapeAppleScriptString(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return value
}

func runDarwinPrivilegedShell(ctx context.Context, shellCommand string) ([]byte, error) {
	script := fmt.Sprintf(`do shell script "%s" with administrator privileges`, escapeAppleScriptString(shellCommand))
	return exec.CommandContext(ctx, "osascript", "-e", script).CombinedOutput()
}

func runDarwinPrivilegedKill(ctx context.Context, pid int, signal syscall.Signal) error {
	if pid <= 0 {
		return nil
	}
	command := fmt.Sprintf("kill -%d %d >/dev/null 2>&1 || true", signal, pid)
	output, err := runDarwinPrivilegedShell(ctx, command)
	if err == nil {
		return nil
	}
	return wrapCommandError(err, output)
}

func wrapCommandError(err error, output []byte) error {
	if trimmed := strings.TrimSpace(string(output)); trimmed != "" {
		return fmt.Errorf("%w: %s", err, trimmed)
	}
	return err
}

func wrapPrivilegedCommandError(message string, err error, output []byte) error {
	wrapped := wrapCommandError(err, output)
	if !isDarwinPermissionDenied(output) {
		return wrapped
	}
	return api.WrapError(api.ErrCodePermissionDenied, message, wrapped)
}

func isDarwinPermissionDenied(output []byte) bool {
	text := strings.ToLower(strings.TrimSpace(string(output)))
	switch {
	case strings.Contains(text, "user canceled"):
		return true
	case strings.Contains(text, "authorization was cancelled"):
		return true
	case strings.Contains(text, "not authorized"):
		return true
	case strings.Contains(text, "password was incorrect"):
		return true
	case strings.Contains(text, "authentication failed"):
		return true
	default:
		return false
	}
}
