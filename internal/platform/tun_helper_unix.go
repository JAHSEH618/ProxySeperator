//go:build !windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
)

func signalProcess(proc *os.Process) error {
	return proc.Signal(syscall.SIGTERM)
}

func (p *tunHelperProcess) stopPrivileged(ctx context.Context) error {
	script := buildPrivilegedKillScript(p.pid)
	_, _ = runPrivilegedScript(ctx, script)

	select {
	case <-p.doneCh:
		p.cleanupTempFiles()
		err := p.waitErrValue()
		if err == nil || errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	case <-ctx.Done():
		<-p.doneCh
		p.cleanupTempFiles()
		return ctx.Err()
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

func runPrivilegedScript(ctx context.Context, scriptContent string) ([]byte, error) {
	tmpFile, err := os.CreateTemp("", "proxyseparator-priv-*.sh")
	if err != nil {
		return nil, fmt.Errorf("create temp script: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.WriteString(scriptContent); err != nil {
		tmpFile.Close()
		return nil, fmt.Errorf("write temp script: %w", err)
	}
	tmpFile.Close()
	_ = os.Chmod(tmpPath, 0700)

	shellCmd := "/bin/bash " + shellQuote(tmpPath)
	return runDarwinPrivilegedShell(ctx, shellCmd)
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
