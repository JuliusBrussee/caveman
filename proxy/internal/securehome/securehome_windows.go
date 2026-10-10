//go:build windows

// Package securehome keeps CAVEMAN_HOME private to the user who owns it.
package securehome

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Restrict gives home an owner-only DACL (this user, SYSTEM, Administrators)
// when it grants anything to anyone else. Windows ignores the 0700 the
// directory was made with: a home outside the profile (D:\caveman) inherits
// the drive root's "Authenticated Users: Modify", so other local accounts
// could read the 0600 credentials inside it and the CCR parent check refused
// compression. Setting the DACL propagates to files already there. A home that
// grants no one else, like the default one under %USERPROFILE%, is left alone,
// and so is one that is not Caveman's to rewrite (see ours): that returns
// ErrNotOurs, saying why, so the person learns why compression is refused.
func Restrict(home string) error {
	descriptor, err := windows.GetNamedSecurityInfo(home, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	if dacl, _, err := descriptor.DACL(); err == nil && dacl != nil {
		broad, err := grantsBroadAccess(dacl, user.User.Sid)
		if err != nil || !broad {
			return err
		}
	}
	if why := ours(home); why != "" {
		return fmt.Errorf("%w: %s", ErrNotOurs, why)
	}
	private, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		return err
	}
	dacl, _, err := private.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(home, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// ours says why home is not a plain local folder holding only what Caveman
// writes there, or "" when it is. A junction or symlink (the DACL would land
// on its target), a network drive, a volume root or a populated folder keeps
// its permissions.
func ours(home string) string {
	clean, err := filepath.Abs(home)
	if err != nil {
		return err.Error()
	}
	path, err := windows.UTF16PtrFromString(clean)
	if err != nil {
		return err.Error()
	}
	if attrs, err := windows.GetFileAttributes(path); err != nil {
		return err.Error()
	} else if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return "it is a junction or link"
	}
	root, err := windows.UTF16PtrFromString(filepath.VolumeName(clean) + `\`)
	if err != nil || windows.GetDriveType(root) == windows.DRIVE_REMOTE {
		return "it is on a network drive"
	}
	entries, err := os.ReadDir(clean)
	if err != nil {
		return err.Error()
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return leaveAlone(clean, names)
}

// Allow-type ACEs x/sys/windows has no names for.
const (
	accessAllowedCompoundACEType       = 0x4
	accessAllowedObjectACEType         = 0x5
	accessAllowedCallbackACEType       = 0x9
	accessAllowedCallbackObjectACEType = 0xB
)

// grantsBroadAccess reports whether any allow entry names someone other than
// this user, SYSTEM, Administrators, CREATOR OWNER or OWNER RIGHTS: Everyone,
// INTERACTIVE, Domain Users, another account. It counts inherit-only entries
// too: those are exactly what the files written into home later would carry.
func grantsBroadAccess(dacl *windows.ACL, user *windows.SID) (bool, error) {
	trusted := []*windows.SID{user}
	for _, sidType := range []windows.WELL_KNOWN_SID_TYPE{
		windows.WinLocalSystemSid,
		windows.WinBuiltinAdministratorsSid,
		windows.WinCreatorOwnerSid,
		windows.WinCreatorOwnerRightsSid,
	} {
		sid, err := windows.CreateWellKnownSid(sidType)
		if err != nil {
			return false, err
		}
		trusted = append(trusted, sid)
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false, err
		}
		if ace.Mask == 0 {
			continue
		}
		switch ace.Header.AceType {
		case windows.ACCESS_ALLOWED_ACE_TYPE, accessAllowedCallbackACEType:
			aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			if !slices.ContainsFunc(trusted, aceSID.Equals) {
				return true, nil
			}
		case accessAllowedCompoundACEType, accessAllowedObjectACEType, accessAllowedCallbackObjectACEType:
			// The SID sits behind object GUIDs here; an entry this unusual on a
			// folder is not one of the defaults, so count it as broad.
			return true, nil
		}
	}
	return false, nil
}
