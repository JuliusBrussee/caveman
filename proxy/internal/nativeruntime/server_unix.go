//go:build !windows

package nativeruntime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// SocketPath returns the user-local normalized hook socket. Host adapters never
// choose this path, preventing one integration from impersonating another
// user's runtime.
func SocketPath(home string) string { return filepath.Join(home, "run", "native.sock") }

func dialNativeRuntime(ctx context.Context, home string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", SocketPath(home))
}

// Serve binds runtime transport for current platform.
func Serve(ctx context.Context, home string, runtime *Runtime) error {
	return ServeUnix(ctx, SocketPath(home), runtime)
}

// ServeUnix exposes one-request-per-connection JSON over a user-only Unix
// socket. Runtime errors close or fail-open the individual call; they never stop
// the coding agent or the provider proxy.
func ServeUnix(ctx context.Context, path string, runtime *Runtime) error {
	if runtime == nil || runtime.store == nil {
		return errors.New("native runtime: store is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("native runtime mkdir: %w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("native runtime chmod dir: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		conn, dialErr := net.DialTimeout("unix", path, 50*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return errors.New("native runtime: socket already active")
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("native runtime remove stale socket: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("native runtime inspect socket: %w", err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("native runtime listen: %w", err)
	}
	defer listener.Close()
	defer os.Remove(path)
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("native runtime chmod socket: %w", err)
	}
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("native runtime accept: %w", err)
		}
		go serveConn(ctx, conn, runtime)
	}
}
