//go:build !linux && !windows && !js

package ccr

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func chmodSQLiteFile(path string, info os.FileInfo) error {
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !current.Mode().IsRegular() || !os.SameFile(info, current) {
		return fmt.Errorf("file changed while securing")
	}
	// Do not open/close the inode: close would discard SQLite's process-wide
	// POSIX locks. Native nofollow chmod cannot follow a swapped symlink; the
	// caller also checks inode identity afterwards. Unsupported systems fail
	// closed rather than using a path-following chmod or an ordinary descriptor.
	return unix.Fchmodat(unix.AT_FDCWD, path, 0o600, unix.AT_SYMLINK_NOFOLLOW)
}
