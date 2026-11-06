//go:build !windows

package nativeruntime

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/ccr"
)

func TestSocketPathUnixIsInstallLocal(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".caveman")
	if got, want := SocketPath(home), filepath.Join(home, "run", "native.sock"); got != want {
		t.Fatalf("SocketPath() = %q, want %q", got, want)
	}
}

func TestServeUnixRejectsOverlongSocketPathWithActionableError(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	path := filepath.Join(t.TempDir(), strings.Repeat("x", 150), "run", "native.sock")
	err = ServeUnix(context.Background(), path, New(store))
	if err == nil {
		t.Fatal("ServeUnix() succeeded with an overlong socket path")
	}
	if !strings.Contains(err.Error(), "Unix socket path too long") || !strings.Contains(err.Error(), "CAVEMAN_HOME") {
		t.Fatalf("ServeUnix() error = %q, want an actionable socket path length diagnostic", err)
	}
}
