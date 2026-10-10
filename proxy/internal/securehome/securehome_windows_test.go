//go:build windows

package securehome

import (
	"errors"
	"os"
	"os/exec"
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
	if err := os.WriteFile(filepath.Join(home, "cloud.json"), []byte("{}"), 0o600); err != nil {
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

// CAVEMAN_HOME pointed at a folder the user keeps other things in, or a
// junction to one: the owner-only DACL would replace theirs for good, with no
// copy of the old one kept. Restrict says why it left it, which the proxy logs.
func TestRestrictLeavesAPopulatedFolderAndAJunctionAlone(t *testing.T) {
	broad := "D:P(A;OICI;0x1301bf;;;AU)(A;OICI;FA;;;" + tokenUser(t) + ")"
	populated := t.TempDir()
	setDACL(t, populated, broad)
	if err := os.WriteFile(filepath.Join(populated, "notes.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	setDACL(t, target, broad)
	// The DACL Restrict reads on a junction is the junction's own, and one that
	// grants no one else is left alone before the junction is even looked at.
	// Made under a broad folder, the junction inherits a broad DACL of its own,
	// so the junction branch is the one that answers.
	parent := t.TempDir()
	setDACL(t, parent, broad)
	link := filepath.Join(parent, "caveman")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v %s", err, out)
	}
	if !broadPath(t, link) {
		t.Fatal("fixture: the junction must start out granting Authenticated Users")
	}
	for _, tt := range []struct{ home, check, why string }{{populated, populated, "it holds notes.txt"}, {link, target, "it is a junction or link"}} {
		before := sddl(t, tt.check)
		if err := Restrict(tt.home); !errors.Is(err, ErrNotOurs) || err.Error() != ErrNotOurs.Error()+": "+tt.why {
			t.Fatalf("Restrict(%s) = %v, want %v: %s", tt.home, err, ErrNotOurs, tt.why)
		}
		if after := sddl(t, tt.check); after != before {
			t.Fatalf("Restrict(%s) rewrote %s:\nbefore %s\nafter  %s", tt.home, tt.check, before, after)
		}
	}
}

func TestLeaveAloneWindowsRootsAndShares(t *testing.T) {
	for home, want := range map[string]bool{
		`D:\`:                    true,
		`C:\`:                    true,
		`D:`:                     true,
		`\\server\share`:         true,
		`\\server\share\caveman`: true,
		`\\?\D:\caveman`:         true,
		`D:\caveman`:             false,
	} {
		if got := leaveAlone(home, nil); (got != "") != want {
			t.Errorf("leaveAlone(%q) = %q, want left alone %v", home, got, want)
		}
	}
}

// Anyone but this user, SYSTEM, Administrators, CREATOR OWNER and OWNER RIGHTS
// is broad. A denylist of Everyone, Authenticated Users, Users and Guests
// missed INTERACTIVE, NETWORK, Domain Users, other accounts and conditional
// (callback) entries.
func TestGrantsBroadAccessIsAnAllowlist(t *testing.T) {
	user := tokenUser(t)
	for sddl, want := range map[string]bool{
		"D:P(A;OICI;FA;;;" + user + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)": false,
		"D:P(A;OICIIO;FA;;;CO)(A;;FA;;;OW)(A;OICI;FA;;;" + user + ")":   false,
		"D:P(D;OICI;FA;;;WD)(A;OICI;FA;;;" + user + ")":                 false,
		"D:P(A;OICI;0x1301bf;;;AU)":                                     true,
		"D:P(A;OICI;FR;;;IU)":                                           true,
		"D:P(A;OICI;FR;;;NU)":                                           true,
		"D:P(A;OICI;FR;;;S-1-5-21-1-2-3-513)":                           true,
		"D:P(A;OICI;FR;;;S-1-5-21-1-2-3-1001)":                          true,
		"D:P(XA;OICI;FR;;;IU;(Member_of {SID(BA)}))":                    true,
	} {
		descriptor, err := windows.SecurityDescriptorFromString(sddl)
		if err != nil {
			t.Fatalf("%s: %v", sddl, err)
		}
		dacl, _, err := descriptor.DACL()
		if err != nil {
			t.Fatal(err)
		}
		sid, err := windows.StringToSid(user)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := grantsBroadAccess(dacl, sid); err != nil || got != want {
			t.Errorf("grantsBroadAccess(%s) = %v %v, want %v", sddl, got, err, want)
		}
	}
}

func tokenUser(t *testing.T) string {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return user.User.Sid.String()
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
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	broad, err := grantsBroadAccess(dacl, user.User.Sid)
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
