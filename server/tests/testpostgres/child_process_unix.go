//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configureChildProcess puts go test and its package binaries in one process
// group. Cancellation kills the group before PostgreSQL cleanup begins.
func configureChildProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
}
