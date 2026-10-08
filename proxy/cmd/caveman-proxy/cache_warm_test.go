package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCacheWarmSwitchFollowsWasteFixesModule(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "cloud.json")
	on := cacheWarmSwitch(home, "")
	if !on() {
		t.Fatal("no cloud.json: warming is on by default")
	}
	write := func(body string, at time.Time) {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(path, at, at)
	}
	now := time.Now()
	write(`{"modules":{"waste-fixes":false}}`, now)
	if on() {
		t.Fatal("caveman off waste-fixes did not stop warming")
	}
	write(`{"modules":{"waste-fixes":true}}`, now.Add(time.Second))
	if !on() {
		t.Fatal("caveman on waste-fixes did not restart warming")
	}
	write(`{"modules":{"routing":true}}`, now.Add(2*time.Second))
	if !on() {
		t.Fatal("an unset module is on by default")
	}
	for _, kill := range []string{"off", "0", "false", "NO"} {
		if cacheWarmSwitch(home, kill)() {
			t.Fatalf("CAVEMAN_CACHE_WARM=%s did not switch warming off", kill)
		}
	}
}
