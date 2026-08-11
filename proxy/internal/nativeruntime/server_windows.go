//go:build windows

package nativeruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"

	winio "github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// SocketPath returns a per-install named-pipe path shared with Node adapters.
func SocketPath(home string) string {
	absolute, err := filepath.Abs(home)
	if err != nil {
		absolute = home
	}
	normalized := strings.ToLower(filepath.Clean(absolute))
	sum := sha256.Sum256([]byte(normalized))
	return `\\.\pipe\caveman-native-` + hex.EncodeToString(sum[:8])
}

func dialNativeRuntime(ctx context.Context, home string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, SocketPath(home))
}

// Serve exposes the same bounded JSON protocol over a user-only Windows named
// pipe. go-winio rejects remote clients at pipe creation; explicit owner SID
// ACL prevents another local user from attaching.
func Serve(ctx context.Context, home string, runtime *Runtime) error {
	if runtime == nil || runtime.store == nil {
		return errors.New("native runtime: store is required")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("native runtime current user SID: %w", err)
	}
	if user == nil || user.User.Sid == nil {
		return errors.New("native runtime current user SID: unavailable")
	}
	sddl := "D:P(A;;GA;;;" + user.User.Sid.String() + ")"
	listener, err := winio.ListenPipe(SocketPath(home), &winio.PipeConfig{
		SecurityDescriptor: sddl,
		InputBufferSize:    maxRequestBytes,
		OutputBufferSize:   maxRequestBytes,
	})
	if err != nil {
		return fmt.Errorf("native runtime named-pipe listen: %w", err)
	}
	defer listener.Close()
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("native runtime named-pipe accept: %w", err)
		}
		go serveConn(ctx, conn, runtime)
	}
}
