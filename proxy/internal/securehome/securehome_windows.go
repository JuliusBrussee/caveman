//go:build windows

// Package securehome keeps CAVEMAN_HOME private to the user who owns it.
package securehome

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Restrict gives home an owner-only DACL (this user, SYSTEM, Administrators)
// when it grants anything to Everyone, Authenticated Users, Users or Guests.
// Windows ignores the 0700 the directory was made with: a home outside the
// profile (D:\caveman) inherits the drive root's "Authenticated Users: Modify",
// so other local accounts could read the 0600 credentials inside it and the
// CCR parent check refused compression. Setting the DACL propagates to files
// already there. A home that grants none of those, like the default one under
// %USERPROFILE%, is left alone.
func Restrict(home string) error {
	descriptor, err := windows.GetNamedSecurityInfo(home, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	if dacl, _, err := descriptor.DACL(); err == nil && dacl != nil {
		broad, err := grantsBroadAccess(dacl)
		if err != nil || !broad {
			return err
		}
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
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

// grantsBroadAccess counts inherit-only entries too: those are exactly what
// the files written into home later would carry.
func grantsBroadAccess(dacl *windows.ACL) (bool, error) {
	broad := make([]*windows.SID, 0, 4)
	for _, sidType := range []windows.WELL_KNOWN_SID_TYPE{
		windows.WinWorldSid,
		windows.WinAuthenticatedUserSid,
		windows.WinBuiltinUsersSid,
		windows.WinBuiltinGuestsSid,
	} {
		sid, err := windows.CreateWellKnownSid(sidType)
		if err != nil {
			return false, err
		}
		broad = append(broad, sid)
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false, err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask == 0 {
			continue
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		for _, sid := range broad {
			if sid.Equals(aceSID) {
				return true, nil
			}
		}
	}
	return false, nil
}
