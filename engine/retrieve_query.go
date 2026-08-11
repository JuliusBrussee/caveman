package engine

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/JuliusBrussee/caveman/engine/contextwindow"
)

// maxRetrieveSections caps a query-targeted recovery at the top-N most relevant
// sections, with a bounded top-k (k=20).
const maxRetrieveSections = 20

// nonAdjacentMarker stands between two returned units that were not neighbours in
// the original, and at either end when the view starts or stops short of it.
//
// It exists because of wrong answers, not wasted tokens. A ranked line view of
// pretty-printed JSON can place a field from one record beside an identifier from
// another. Without an explicit gap marker, an agent can attribute the field to the
// wrong record and report a result the source never contained.
//
// A retrieval view therefore never emits a fragment of a unit, and never lets two
// units touch unless they touched in the original.
const nonAdjacentMarker = "… [caveman: non-adjacent] …"

// RetrieveQuery recovers the content behind a CCR handle, narrowed to the sections
// most relevant to query (BM25-rank the stored content's sections, keep the
// matches). With an empty query it is identical
// to Retrieve (byte-exact recovery). It never drops detail it cannot rank: if the
// stored content yields no rankable units, or nothing matches the query, it
// returns the full original.
func (e *Engine) RetrieveQuery(handle, query string) ([]byte, error) {
	original, err := e.Retrieve(handle)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(query) == "" {
		return original, nil
	}
	if narrowed, ok := narrowToQuery(original, query); ok && len(narrowed) < len(original) {
		return narrowed, nil
	}
	return original, nil
}

// narrowToQuery splits stored content into whole units, BM25-ranks them against
// the query, and returns the relevant ones in their original order, marking every
// gap. ok is false when the content cannot be decomposed into units or nothing
// matched, so the caller recovers the full original: over-returning is safe,
// returning a fragment is not.
func narrowToQuery(content []byte, query string) ([]byte, bool) {
	units, prelude := retrievalUnits(content)
	if len(units) == 0 {
		return nil, false
	}
	scores := contextwindow.BM25(query, units)
	type ranked struct {
		idx   int
		score float64
	}
	hits := make([]ranked, 0, len(units))
	for i, s := range scores {
		if s > 0 {
			hits = append(hits, ranked{idx: i, score: s})
		}
	}
	if len(hits) == 0 {
		return nil, false
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if len(hits) > maxRetrieveSections {
		hits = hits[:maxRetrieveSections]
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].idx < hits[j].idx })

	var b strings.Builder
	if prelude != "" {
		b.WriteString(prelude)
		b.WriteString("\n")
	}
	previous := -1
	for _, h := range hits {
		switch {
		case previous < 0 && h.idx > 0:
			// The view starts short of the content: say so before the first unit,
			// or a reader counts what is shown as everything there was.
			b.WriteString(nonAdjacentMarker)
			b.WriteString("\n\n")
		case previous >= 0 && h.idx == previous+1:
			b.WriteString("\n\n")
		case previous >= 0:
			b.WriteString("\n\n")
			b.WriteString(nonAdjacentMarker)
			b.WriteString("\n\n")
		}
		b.WriteString(units[h.idx])
		previous = h.idx
	}
	if previous < len(units)-1 {
		b.WriteString("\n\n")
		b.WriteString(nonAdjacentMarker)
	}
	return []byte(b.String()), true
}

// retrievalUnits decomposes stored content into the smallest pieces that are
// self-delimiting — a piece a reader can interpret without its neighbours. prelude
// is emitted once above the units (a table's header row) or empty.
//
// It returns no units at all rather than a decomposition it does not trust; the
// caller then recovers the whole original.
func retrievalUnits(content []byte) ([]string, string) {
	var root any
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err == nil && !decoder.More() {
		// JSON units are complete object records, never bare values taken from a
		// field named content/text. Those names are ordinary tool-output fields;
		// extracting their strings would orphan them from record ids and recreate
		// the wrong-attribution bug this path exists to prevent.
		var groups [][]string
		collectJSONRecordGroups(root, &groups)
		if len(groups) > 0 {
			// Empty sentinel units preserve source gaps without entering the ranked
			// output: one before/after the extracted records marks omitted envelope
			// bytes, and one between arrays prevents records from separate arrays
			// being rendered as adjacent. BM25 assigns empty units no score.
			units := []string{""}
			for i, group := range groups {
				if i > 0 {
					units = append(units, "")
				}
				units = append(units, group...)
			}
			units = append(units, "")
			return units, ""
		}
		// JSON we cannot decompose into records. Its source is NOT line-splittable
		// — `"status": "unfulfilled",` is a fragment, not a unit — so claim nothing
		// and let the caller return the whole thing.
		return nil, ""
	}
	if rows, header, ok := tabularRetrievalUnits(content); ok {
		return rows, header
	}
	// Line-oriented content: logs, NDJSON, plain text. A line (or a paragraph,
	// where blank lines separate them) is already a whole unit.
	return appendSections(nil, string(content)), ""
}

// collectJSONRecordGroups walks the decoded payload and takes each array that
// holds nothing but objects as a separate group — an orders page's `orders`, a
// report's `rows`. It does not descend into a group it has taken: each object is
// one unit, and group boundaries preserve source non-adjacency.
func collectJSONRecordGroups(node any, out *[][]string) {
	switch v := node.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys) // map order is random; a recovery view must not be
		for _, key := range keys {
			collectJSONRecordGroups(v[key], out)
		}
	case []any:
		objects := 0
		for _, element := range v {
			if _, isObject := element.(map[string]any); isObject {
				objects++
			}
		}
		if objects > 0 && objects == len(v) {
			group := make([]string, 0, len(v))
			for _, element := range v {
				if encoded, err := json.MarshalIndent(element, "", "  "); err == nil {
					group = append(group, string(encoded))
				}
			}
			if len(group) > 0 {
				*out = append(*out, group)
			}
			return
		}
		for _, element := range v {
			collectJSONRecordGroups(element, out)
		}
	}
}

// tabularRetrievalUnits splits a CSV/TSV payload into whole records, with the
// header returned separately so it can head the view exactly once. Records are
// sliced out of the ORIGINAL bytes by input offset, so quoting, spacing, and
// embedded newlines survive untouched.
func tabularRetrievalUnits(content []byte) ([]string, string, bool) {
	trimmed := bytes.TrimSpace(content)
	if !utf8.Valid(content) || len(trimmed) == 0 || trimmed[0] == '{' || trimmed[0] == '[' {
		return nil, "", false
	}
	firstLine, _, _ := bytes.Cut(trimmed, []byte("\n"))
	delimiter := ','
	if bytes.Count(firstLine, []byte("\t")) > bytes.Count(firstLine, []byte(",")) {
		delimiter = '\t'
	}
	if !bytes.ContainsRune(firstLine, delimiter) {
		return nil, "", false
	}

	reader := csv.NewReader(bytes.NewReader(content))
	reader.Comma = delimiter
	reader.FieldsPerRecord = 0 // every record must match the header's field count
	reader.ReuseRecord = true
	var (
		units  []string
		header string
		offset int64
		fields int
	)
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", false
		}
		next := reader.InputOffset()
		text := strings.TrimRight(string(content[offset:next]), "\r\n")
		offset = next
		if header == "" && units == nil {
			header, fields = text, len(record)
			continue
		}
		units = append(units, text)
	}
	if fields < 2 || len(units) < 2 {
		return nil, "", false // not a table worth splitting
	}
	return units, header, true
}

func appendSections(into []string, text string) []string {
	for _, sec := range splitSections(text) {
		if trimmed := strings.TrimSpace(sec); trimmed != "" {
			into = append(into, trimmed)
		}
	}
	return into
}

func splitSections(text string) []string {
	if strings.Contains(text, "\n\n") {
		return strings.Split(text, "\n\n")
	}
	return strings.Split(text, "\n")
}
