//go:build windows

package runstate

import (
	"golang.org/x/sys/windows"
)

// processExecutableWindows resolves a PID's executable path via the Win32
// process-query API; /proc and ps do not exist on Windows.
func processExecutableWindows(pid int) (string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(handle, 0, &buf[0], &size); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:size]), nil
}
