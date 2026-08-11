//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

func configureDetachedProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func terminateDetachedProcess(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}
