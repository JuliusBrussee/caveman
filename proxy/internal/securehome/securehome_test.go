package securehome

import (
	"path/filepath"
	"testing"
)

// Restrict rewrites a home's permissions for good. Only a plain folder holding
// nothing but Caveman's own files, one of them a file no other program
// writes, is Caveman's to rewrite: bin, run, tmp or reports.xlsx is anyone's.
func TestLeaveAloneAnythingCavemanDoesNotOwn(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".caveman")
	ours := []string{"bin", "run", "cloud.json", "cloud.json.123.tmp", "cloud.json.telemetry.lock", "credentials",
		"caveman.db", "caveman.db-wal", "ccr.db-shm", "proxy.log.1", "provider-logins.json.lock.break.ab",
		".caveman-sqlite-123", ".cache-warm-9", "integrations", "mem", "modules.lock.json.123.a1b2c3.tmp",
		"cloud.json.123.a1b2c3.tmp", ".install.lock.123.1700000000000.stale", "cloud.json.telemetry.lock.123.1700000000000.stale"}
	for _, tt := range []struct {
		name  string
		home  string
		names []string
		want  bool
	}{
		{"fresh home", home, nil, false},
		{"only caveman's files", home, ours, false},
		{"a user's file", home, append(ours, "notes.txt"), true},
		{"a user's folder", home, []string{"src"}, true},
		{"a name that only starts like ours", home, []string{"binaries"}, true},
		{"a tools folder", home, []string{"bin", "reports.xlsx", "run.bat", "credentials.txt", "usage-notes.md", "tmp"}, true},
		{"names that only continue like ours", home, []string{"cloud.json", "cli-tools", "hooks-backup", "mcp-servers"}, true},
		{"generic names alone", home, []string{"bin", "run", "tmp", "reports"}, true},
		{"the install lock", home, []string{"cloud.json", ".install.lock"}, false},
		{"a name that only ends like ours", home, []string{"cloud.json", "cloud.json.stale.txt"}, true},
		{"volume root", string(filepath.Separator), nil, true},
		{"network share", `\\server\share\caveman`, nil, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := leaveAlone(tt.home, tt.names); (got != "") != tt.want {
				t.Fatalf("leaveAlone(%q, %q) = %q, want left alone %v", tt.home, tt.names, got, tt.want)
			}
		})
	}
}
