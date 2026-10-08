//go:build !windows && !js

package ccr

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func validateSQLiteParentSecurity(path string, info os.FileInfo) error {
	// Sticky shared temp directories (/tmp, macOS /private/tmp) prevent another
	// user from replacing the 0600 file. Other group/world-writable parents do not.
	if info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
		return fmt.Errorf("sqlite parent %q is group/world writable", path)
	}
	return nil
}

func createSQLiteFile(path string) error {
	// Close before publishing: another Store in this process may open the
	// database as soon as its name exists. Closing even a creation descriptor
	// after that point would discard SQLite's process-wide POSIX locks.
	file, err := os.CreateTemp(filepath.Dir(path), ".caveman-sqlite-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Close(); err != nil {
		return err
	}
	// Link is atomic and refuses to replace an existing file or symlink.
	return publishSQLiteFile(file.Name(), path, os.Link)
}

func publishSQLiteFile(source, target string, link func(string, string) error) error {
	err := link(source, target)
	if err == nil || !errors.Is(err, os.ErrPermission) {
		return err
	}
	// Android/Termux may reject hard links. O_EXCL still publishes an empty
	// regular file atomically without replacing an existing path. The temp
	// inode is disposable; SQLite has not opened it yet.
	published, createErr := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if createErr != nil {
		return createErr
	}
	return published.Close()
}
