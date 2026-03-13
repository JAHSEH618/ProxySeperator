//go:build windows

package platform

import (
	"context"
	"errors"
	"os"
)

func signalProcess(proc *os.Process) error {
	return proc.Kill()
}

func processRunning(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false, nil
	}
	_ = proc
	return true, nil
}

func (p *tunHelperProcess) stopPrivileged(_ context.Context) error {
	// Windows does not use privileged osascript-based kill.
	// Simply wait for the process to exit.
	select {
	case <-p.doneCh:
		p.cleanupTempFiles()
		err := p.waitErrValue()
		if err == nil || errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	default:
		return nil
	}
}
