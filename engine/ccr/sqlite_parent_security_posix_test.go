//go:build !windows && !js

package ccr

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPublishSQLiteFileFallsBackWhenHardLinksDenied(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "temp")
	target := filepath.Join(dir, "ccr.db")
	if err := os.WriteFile(source, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := func(string, string) error { return os.ErrPermission }
	if err := publishSQLiteFile(source, target, link); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("published file=%v, want regular 0600", info.Mode())
	}
	if err := publishSQLiteFile(source, target, link); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing target error=%v, want EEXIST", err)
	}
}
