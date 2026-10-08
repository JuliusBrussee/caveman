package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// cacheWarmSwitch is prompt-cache warming's on/off, asked before every arm and
// every warm. The CAVEMAN_CACHE_WARM kill switch (off, 0, false, no) wins for
// the process. Otherwise warming follows the waste-fixes module: on unless
// $CAVEMAN_HOME/cloud.json says `modules["waste-fixes"]: false`, read again
// whenever the file changes, as the route stage reads `modules.routing`, so
// `caveman off waste-fixes` stops warming without a restart.
func cacheWarmSwitch(home, kill string) func() bool {
	switch strings.ToLower(strings.TrimSpace(kill)) {
	case "off", "0", "false", "no":
		return func() bool { return false }
	}
	path := filepath.Join(home, "cloud.json")
	var mu sync.Mutex
	var seen time.Time
	var size int64 = -1
	on := true
	return func() bool {
		mu.Lock()
		defer mu.Unlock()
		info, err := os.Stat(path)
		if err != nil {
			seen, size, on = time.Time{}, -1, true
			return on
		}
		if info.ModTime().Equal(seen) && info.Size() == size {
			return on
		}
		seen, size, on = info.ModTime(), info.Size(), true
		var doc struct {
			Modules map[string]any `json:"modules"`
		}
		if raw, err := os.ReadFile(path); err == nil && json.Unmarshal(raw, &doc) == nil {
			on = doc.Modules["waste-fixes"] != false
		}
		return on
	}
}
