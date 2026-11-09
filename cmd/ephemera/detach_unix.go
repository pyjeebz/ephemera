//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// detach makes cmd outlive this process and the terminal.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
