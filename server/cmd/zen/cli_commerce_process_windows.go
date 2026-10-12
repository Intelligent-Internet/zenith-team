//go:build windows

package main

import "os/exec"

func configureCommerceDevProcess(*exec.Cmd) {}

func killCommerceDevProcess(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	return command.Process.Kill()
}
