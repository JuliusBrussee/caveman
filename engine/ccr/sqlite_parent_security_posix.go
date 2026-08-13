//go:build !windows && !js

package ccr

import (
	"fmt"
	"os"
)

func validateSQLiteParentSecurity(path string, info os.FileInfo) error {
	// Sticky shared temp directories (/tmp, macOS /private/tmp) prevent another
	// user from replacing the 0600 file. Other group/world-writable parents do not.
	if info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
		return fmt.Errorf("sqlite parent %q is group/world writable", path)
	}
	return nil
}
