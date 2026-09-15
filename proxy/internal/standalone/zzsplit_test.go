package standalone

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/tokens"
)

// TestZZBodySplit reports where a Claude Code request's tokens actually sit, so
// the achievable ceiling of live-zone compression is a measurement rather than
// a guess. Diagnostic only.
func TestZZBodySplit(t *testing.T) {
	dir := os.Getenv("PROBE_DIR")
	if dir == "" {
		t.Skip("PROBE_DIR not set")
	}
	counter := tokens.Default()
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			System   json.RawMessage   `json:"system"`
			Tools    json.RawMessage   `json:"tools"`
			Messages []json.RawMessage `json:"messages"`
		}
		if json.Unmarshal(raw, &body) != nil {
			continue
		}
		count := func(b []byte) int { return counter.Count(b) }
		total := count(raw)
		system := count(body.System)
		toolsCount := count(body.Tools)
		messages := 0
		for _, m := range body.Messages {
			messages += count(m)
		}
		fmt.Printf("%s total=%6d  system=%6d (%4.1f%%)  tools=%6d (%4.1f%%)  messages=%6d (%4.1f%%)\n",
			filepath.Base(file), total,
			system, pct(system, total),
			toolsCount, pct(toolsCount, total),
			messages, pct(messages, total))
	}
}

func pct(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return 100 * float64(part) / float64(whole)
}
