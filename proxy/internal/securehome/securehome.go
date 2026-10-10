package securehome

import (
	"errors"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// ownedEntries are the names Caveman writes at the top of its home; a name
// continued by ownedSuffix is a temp, lock, rotated log or SQLite file beside
// one of them, and a dot-named temp prefix continued by "-" is a temp file.
// packages/cli/src/home-acl.ts keeps the same lists for the CLI: change both
// together.
var ownedEntries = []string{
	"bin", "browse-profile", "candidates", "cli", "exports", "hooks", "integrations", "mcp", "mem", "openclaw",
	"packs", "provider-logins", "recall-hooks", "receipts", "reports", "run", "runtime", "tmp", "usage",
	"browse-chrome.log", "browse-session.json", "cache-warm-gaps.json", "caveman.db", "caveman.yaml", "ccr.db",
	"chatgpt-host-id", "cloud.json", "credentials", "modules.lock.json", "provider-logins.json", "proxy.log",
	"route-state.json",
	".browse-session", ".cache-warm", ".caveman-sqlite", ".install.lock",
}

// homeMarkers are files no other program writes. bin, run, tmp or reports
// alone are any tools folder, so a home holding anything is Caveman's only
// with one of these in it.
var homeMarkers = []string{
	"caveman.db", "caveman.yaml", "ccr.db", "cloud.json", "modules.lock.json", "provider-logins.json", "route-state.json",
}

// ownedSuffix is what Caveman puts after one of its names: SQLite's -wal,
// -shm and -journal, a rotated log's .1, a temp (.tmp, .<pid>.<random>.tmp), a
// lock (.lock, .<purpose>.lock), a lock being broken (.lock.break.<token>) and
// a broken lock set aside (.<pid>.<ms>.stale).
var ownedSuffix = regexp.MustCompile(`^(?:-wal|-shm|-journal|\.\d+|(?:\.[^.]+)*\.tmp|(?:\.[^.]+)*\.stale|(?:\.[^.]+)?\.lock(?:\.break\..+)?)$`)

// ErrNotOurs is Restrict leaving a home anyone else may read as it is,
// because its permissions are not Caveman's to rewrite; the error says why.
var ErrNotOurs = errors.New("not a folder only Caveman writes to")

// leaveAlone says why home's permissions are not Caveman's to rewrite, or ""
// when they are: a volume root, a network path, or a folder holding anything
// Caveman did not write (CAVEMAN_HOME pointed at D:\work). The owner-only DACL
// replaces the old one and propagates to everything below with no copy kept,
// so on those it could not be undone and could lock the user out of a share.
func leaveAlone(home string, names []string) string {
	clean := filepath.Clean(home)
	if filepath.Dir(clean) == clean {
		return "it is a volume root"
	}
	if strings.HasPrefix(clean, `\\`) {
		return "it is a network path"
	}
	marked := len(names) == 0
	for _, name := range names {
		if !slices.ContainsFunc(ownedEntries, func(entry string) bool {
			rest, ok := strings.CutPrefix(name, entry)
			return ok && (rest == "" || ownedSuffix.MatchString(rest) || strings.HasPrefix(entry, ".") && strings.HasPrefix(rest, "-"))
		}) {
			return "it holds " + name
		}
		marked = marked || slices.Contains(homeMarkers, name)
	}
	if !marked {
		return "it holds none of Caveman's own files"
	}
	return ""
}
