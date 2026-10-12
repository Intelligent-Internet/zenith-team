//go:build windows

package main

import "os/exec"

func configureChildProcess(_ *exec.Cmd) {}
