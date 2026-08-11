//go:build windows

package main

import (
	"os"
	"os/exec"
)

func configureDetachedProcess(_ *exec.Cmd) {}

func terminateDetachedProcess(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	defer process.Release()
	return process.Kill()
}
