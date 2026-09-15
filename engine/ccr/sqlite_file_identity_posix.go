//go:build !windows && !js

package ccr

import "os"

// Lstat captures device/inode immediately on POSIX. Do not open/close another
// database descriptor here: closing it could drop this process's SQLite locks.
func inspectSQLiteFile(path string) (os.FileInfo, error) {
	return os.Lstat(path)
}
