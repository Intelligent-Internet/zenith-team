//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package main

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestCommerceDevProcessUsesDedicatedKillableGroup(t *testing.T) {
	t.Parallel()

	command := exec.CommandContext(t.Context(), "sh", "-c", "sleep 30")
	configureCommerceDevProcess(command)
	if command.SysProcAttr == nil || !command.SysProcAttr.Setpgid {
		t.Fatal("Commerce child is not configured with a dedicated process group")
	}
	if err := command.Start(); err != nil {
		t.Fatalf("start Commerce child: %v", err)
	}
	t.Cleanup(func() {
		_ = killCommerceDevProcess(command)
		_ = command.Wait()
	})
	groupID, err := syscall.Getpgid(command.Process.Pid)
	if err != nil {
		t.Fatalf("get Commerce child process group: %v", err)
	}
	if groupID != command.Process.Pid {
		t.Fatalf("Commerce child process group = %d, want %d", groupID, command.Process.Pid)
	}
	if err := killCommerceDevProcess(command); err != nil {
		t.Fatalf("kill Commerce child process group: %v", err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("killed Commerce child exited successfully")
	}
}
