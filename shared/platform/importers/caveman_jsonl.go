package importers

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// parseCavemanJSONL parses one JSON span per line. Each line is already close
// to the Span shape (the same column names), so it unmarshals directly into a
// Span and then has the authenticated scope stamped over it. Blank lines are
// skipped. A malformed line aborts the whole import (no partial silent ingest).
func parseCavemanJSONL(data []byte, opts Options) ([]Span, error) {
	var rows []Span
	for i, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var sp Span
		if err := json.Unmarshal(line, &sp); err != nil {
			return nil, fmt.Errorf("caveman-jsonl line %d: %w", i+1, err)
		}
		applyScope(&sp, opts)
		rows = append(rows, sp)
	}
	return rows, nil
}
