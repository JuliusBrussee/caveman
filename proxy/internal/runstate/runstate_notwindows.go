//go:build !windows

package runstate

import "fmt"

// processExecutableWindows is only reachable behind a runtime.GOOS check; this
// stub keeps non-Windows builds compiling without the x/sys/windows import.
func processExecutableWindows(int) (string, error) {
	return "", fmt.Errorf("windows-only process query on %s build", "non-windows")
}
