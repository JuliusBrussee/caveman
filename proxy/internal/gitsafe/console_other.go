//go:build !windows

package gitsafe

import "os/exec"

func hideConsole(*exec.Cmd) {}
