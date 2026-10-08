package nativehook

import (
	"os"
	"path/filepath"
	"testing"
)

// The CLI moved its config to $CAVEMAN_HOME/cloud.json; the hook must follow,
// or `think.mode=record` written there is ignored and compression keeps running.
func TestConfiguredModeReadsTheNewConfigHomeFirst(t *testing.T) {
	user := t.TempDir()
	t.Setenv("HOME", user)
	home := filepath.Join(user, ".caveman")
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	legacy := filepath.Join(user, ".caveman-cloud", "config.json")
	current := filepath.Join(home, "cloud.json")

	if got := configuredMode(home); got != "compress" {
		t.Fatalf("no config: got %q, want compress", got)
	}
	write(legacy, `{"think":{"mode":"record"}}`)
	if got := configuredMode(home); got != "record" {
		t.Fatalf("old config only: got %q, want record", got)
	}
	write(current, `{"think":{"mode":"pixel"}}`)
	if got := configuredMode(home); got != "pixel" {
		t.Fatalf("new config wins over the old one: got %q, want pixel", got)
	}
	write(current, `{"think":{"mode":"record"}}`)
	write(legacy, `{"think":{"mode":"compress"}}`)
	if got := configuredMode(home); got != "record" {
		t.Fatalf("record in the new config: got %q, want record", got)
	}
}
