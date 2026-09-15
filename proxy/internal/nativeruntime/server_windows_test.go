//go:build windows

package nativeruntime

import (
	"strings"
	"testing"
)

func TestSocketPathWindowsIsPerInstall(t *testing.T) {
	one := SocketPath(`C:\Users\Alice\.caveman`)
	two := SocketPath(`C:\Users\Bob\.caveman`)
	if !strings.HasPrefix(one, `\\.\pipe\caveman-native-`) {
		t.Fatalf("SocketPath() = %q", one)
	}
	if one == two {
		t.Fatal("different installs must not share a pipe")
	}
	if one != SocketPath(`c:\users\alice\.caveman\.`) {
		t.Fatal("equivalent case-insensitive Windows paths must share a pipe")
	}
}
