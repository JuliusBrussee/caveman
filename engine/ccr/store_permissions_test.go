//go:build !js

package ccr

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOpenSecuresDatabaseAndSidecarPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}
	path := filepath.Join(t.TempDir(), "ccr.db")
	if err := os.WriteFile(path, []byte{}, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(candidate)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("%s permissions = %04o, want 0600", candidate, got)
		}
	}
}

func TestOpenRejectsDatabaseSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "ccr.db")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link); err == nil {
		t.Fatal("symlink database accepted")
	}
}

func TestOpenRejectsWritableParentDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX writable-parent setup does not model a Windows DACL")
	}
	dir := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(dir, "ccr.db")); err == nil {
		t.Fatal("database in group/world-writable parent accepted")
	}
}

func TestOpenAllowsStickyTemporaryDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sticky")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(dir, "ccr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
}

func TestOpenAllowsResolvedStickyParentSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	root := t.TempDir()
	target := filepath.Join(root, "sticky")
	if err := os.Mkdir(target, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "tmp-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(link, "ccr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
}

func TestOpenUsesCanonicalParentAfterSymlinkSwap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	root := t.TempDir()
	validated := filepath.Join(root, "validated")
	redirected := filepath.Join(root, "redirected")
	if err := os.Mkdir(validated, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(redirected, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "db-parent")
	if err := os.Symlink(validated, link); err != nil {
		t.Fatal(err)
	}
	store, err := openWithBudget(filepath.Join(link, "ccr.db"), DefaultMaxStorageBytes, func() {
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(redirected, link); err != nil {
			t.Fatal(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := os.Stat(filepath.Join(validated, "ccr.db")); err != nil {
		t.Fatalf("validated database missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(redirected, "ccr.db")); !os.IsNotExist(err) {
		t.Fatalf("redirected database created: %v", err)
	}
}
