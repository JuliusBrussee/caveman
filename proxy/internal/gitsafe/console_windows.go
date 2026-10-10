//go:build windows

package gitsafe

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// hideConsole stops git from opening a console window. The proxy runs
// detached with no console, so Windows hands every console program it starts
// (git.exe) a new, visible one; CREATE_NO_WINDOW makes that console hidden.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
}
