//go:build windows

package securehome

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// A home under a drive root inherits "Authenticated Users: Modify" (AU,
// 0x1301bf), so the credentials inside it were readable by every local
// account and the CCR parent check refused compression.
func TestRestrictMakesABroadHomeOwnerOnlyAndLeavesItAloneAfter(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	setDACL(t, home, "D:P(A;OICI;0x1301bf;;;AU)(A;OICI;FA;;;"+user.User.Sid.String()+")")
	secret := filepath.Join(home, "credentials")
	if err := os.WriteFile(secret, []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !broadPath(t, secret) {
		t.Fatal("fixture: the secret must start out readable by Authenticated Users")
	}
	if err := Restrict(home); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{home, secret} {
		if broadPath(t, path) {
			t.Fatalf("%s still grants access to a broad group", path)
		}
	}
	if raw, err := os.ReadFile(secret); err != nil || string(raw) != "token" {
		t.Fatalf("owner lost access to its own secret: %q %v", raw, err)
	}
	before := sddl(t, home)
	if err := Restrict(home); err != nil {
		t.Fatal(err)
	}
	if after := sddl(t, home); after != before {
		t.Fatalf("an owner-only home must be left alone:\nbefore %s\nafter  %s", before, after)
	}
}

func setDACL(t *testing.T, path, sddl string) {
	t.Helper()
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

func broadPath(t *testing.T, path string) bool {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	broad, err := grantsBroadAccess(dacl)
	if err != nil {
		t.Fatal(err)
	}
	return broad
}

func sddl(t *testing.T, path string) string {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	return descriptor.String()
}
