//go:build !windows

package nativeruntime

import (
	"path/filepath"
	"testing"
)

func TestSocketPathUnixIsInstallLocal(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".caveman")
	if got, want := SocketPath(home), filepath.Join(home, "run", "native.sock"); got != want {
		t.Fatalf("SocketPath() = %q, want %q", got, want)
	}
}
