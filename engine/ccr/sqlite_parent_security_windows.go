//go:build windows && !js

package ccr

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func validateSQLiteParentSecurity(path string, _ os.FileInfo) error {
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("inspect sqlite parent ACL %q: %w", path, err)
	}
	if descriptor == nil {
		return fmt.Errorf("sqlite parent %q has no security descriptor", path)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("sqlite parent %q has no restrictive DACL", path)
	}
	broad, err := windowsACLGrantsBroadWrite(dacl)
	if err != nil {
		return fmt.Errorf("inspect sqlite parent ACL %q: %w", path, err)
	}
	if broad {
		return fmt.Errorf("sqlite parent %q grants broad Windows write access", path)
	}
	return nil
}

func windowsACLGrantsBroadWrite(dacl *windows.ACL) (bool, error) {
	broadSIDs := make([]*windows.SID, 0, 4)
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
		broadSIDs = append(broadSIDs, sid)
	}
	const deleteChild windows.ACCESS_MASK = 0x00000040
	writeMask := windows.ACCESS_MASK(
		windows.FILE_WRITE_DATA|
			windows.FILE_APPEND_DATA|
			windows.DELETE|
			windows.WRITE_DAC|
			windows.WRITE_OWNER|
			windows.GENERIC_WRITE|
			windows.GENERIC_ALL,
	) | deleteChild
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false, err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		if ace.Mask&writeMask == 0 {
			continue
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		for _, broadSID := range broadSIDs {
			if broadSID.Equals(aceSID) {
				return true, nil
			}
		}
	}
	return false, nil
}
