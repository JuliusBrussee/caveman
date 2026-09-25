//go:build windows && !js

package ccr

import (
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

func inspectSQLiteFile(path string) (os.FileInfo, error) {
	// Go's Windows Lstat defers looking up the volume/file ID until SameFile.
	// Resolve it through a handle now, before the path can name a replacement.
	// Full sharing preserves SQLite access and rename semantics; opening the
	// reparse point itself preserves the caller's no-symlink security check.
	nativePath := path
	if !strings.HasPrefix(nativePath, `\\?\`) {
		if strings.HasPrefix(nativePath, `\\`) {
			nativePath = `\\?\UNC\` + strings.TrimPrefix(nativePath, `\\`)
		} else {
			nativePath = `\\?\` + nativePath
		}
	}
	name, err := windows.UTF16PtrFromString(nativePath)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, &os.PathError{Op: "inspect sqlite file identity", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	defer file.Close()
	return file.Stat()
}
