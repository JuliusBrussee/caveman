package securehome

import (
	"path/filepath"
	"strings"
)

// ownedEntries are the names Caveman writes at the top of its home; a name
// continued by "." or "-" is a temp, lock, rotated log or SQLite file beside
// one of them. packages/cli/src/home-acl.ts keeps the same list for the CLI:
// change both together.
var ownedEntries = []string{
	"bin", "browse-profile", "candidates", "cli", "exports", "hooks", "integrations", "mcp", "mem", "openclaw",
	"packs", "provider-logins", "recall-hooks", "receipts", "reports", "run", "runtime", "tmp", "usage",
	"browse-chrome.log", "browse-session.json", "cache-warm-gaps.json", "caveman.db", "caveman.yaml", "ccr.db",
	"chatgpt-host-id", "cloud.json", "credentials", "modules.lock.json", "provider-logins.json", "proxy.log",
	"route-state.json",
	".browse-session", ".cache-warm", ".caveman-sqlite",
}

// leaveAlone reports whether home's permissions are not Caveman's to rewrite:
// a volume root, a network path, or a folder holding anything Caveman did not
// write (CAVEMAN_HOME pointed at D:\work). The owner-only DACL replaces the
// old one and propagates to everything below with no copy kept, so on those
// it could not be undone and could lock the user out of a share.
func leaveAlone(home string, names []string) bool {
	clean := filepath.Clean(home)
	if filepath.Dir(clean) == clean || strings.HasPrefix(clean, `\\`) {
		return true
	}
	for _, name := range names {
		owned := false
		for _, entry := range ownedEntries {
			if name == entry || strings.HasPrefix(name, entry+".") || strings.HasPrefix(name, entry+"-") {
				owned = true
				break
			}
		}
		if !owned {
			return true
		}
	}
	return false
}
