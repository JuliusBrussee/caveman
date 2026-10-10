//go:build windows

package gitsafe

import (
	"context"
	"testing"

	"golang.org/x/sys/windows"
)

// The detached proxy runs the repository map's git calls with no console, so
// without CREATE_NO_WINDOW each one flashes a console window on screen.
func TestCommandRunsGitWithoutAConsoleWindow(t *testing.T) {
	cmd := Command(context.Background(), `C:\repo`, "status")
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatalf("git must start with CREATE_NO_WINDOW: %+v", cmd.SysProcAttr)
	}
}
