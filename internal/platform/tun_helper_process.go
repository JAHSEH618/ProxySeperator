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
	"strings"
	"sync"
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
		if p.isDone() {
			p.cleanupTempFiles()
			err := p.waitErrValue()
			if err == nil || errors.Is(err, os.ErrProcessDone) {
				return nil
			}
			return err
		}
		return p.stopPrivileged(ctx)
	}
	if !p.isDone() && p.cmd.Process != nil {
		_ = signalProcess(p.cmd.Process)
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
