//go:build windows && !js

package ccr

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsACLRejectsBroadWriteGrant(t *testing.T) {
	descriptor, err := windows.SecurityDescriptorFromString("D:(A;OICI;GA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	broad, err := windowsACLGrantsBroadWrite(dacl)
	if err != nil {
		t.Fatal(err)
	}
	if !broad {
		t.Fatal("Everyone generic-all ACE was accepted")
	}
}

func TestWindowsACLAllowsBroadReadOnlyGrant(t *testing.T) {
	descriptor, err := windows.SecurityDescriptorFromString("D:(A;OICI;GRGX;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	broad, err := windowsACLGrantsBroadWrite(dacl)
	if err != nil {
		t.Fatal(err)
	}
	if broad {
		t.Fatal("Everyone read/execute ACE was rejected")
	}
}
