package agent

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func hideAgentWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
}

func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

// Windows has no POSIX group signal or group-existence probe. Until tree
// cleanup can be confirmed, an initialize timeout must not launch a retry.
func codexInitializeRetrySupported() bool { return false }

func signalProcessGroup(p *os.Process, _ syscall.Signal) {
	if p == nil {
		return
	}
	// taskkill /T includes descendants. Windows cannot gracefully signal this
	// headless tree, so force termination and bound the helper itself.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "taskkill", "/PID", strconv.Itoa(p.Pid), "/T", "/F")
	hideAgentWindow(cmd)
	if err := cmd.Run(); err != nil {
		_ = p.Kill()
	}
}

// Reaping the leader does not prove its descendants are gone on Windows.
func waitProcessGroupGone(_ *os.Process, _ time.Duration) bool { return false }
