package compressors

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/JuliusBrussee/caveman/engine/safety"
)

var (
	markdownSeparatorCellRe = regexp.MustCompile(`^:?-{3,}:?$`)
	tabularImportantRe      = regexp.MustCompile(`(?i)\b(ERROR|FAIL|FAILED|FATAL|PANIC|EXCEPTION|WARNING|SECURITY|DENIED|REJECTED)\b`)
	tabularMarkerRe         = regexp.MustCompile(`rows elided \(caveman\)`)
)

// tabularMarker renders the elision marker cell for a run of dropped rows.
// summary is the class-invariant description of exactly those rows; when it is
// empty the marker is byte-identical to the pre-invariant one.
func tabularMarker(n int, summary string) string {
	if summary == "" {
		return fmt.Sprintf("… %d rows elided (caveman) …", n)
	}
	return fmt.Sprintf("… %d rows elided (caveman): %s …", n, summary)
}

// summarizeRowRun describes a run of dropped table rows by their header-named
// columns.
func summarizeRowRun(header []string, run [][]string) string {
	units := make([][]field, len(run))
	elidedBytes := 0
	for i, row := range run {
		units[i] = rowFields(header, row)
		elidedBytes += len(strings.Join(row, ",")) + 1
	}
	return summarizeElided(units, elidedBytes)
}

type tableKind uint8

const (
	tableCSV tableKind = iota + 1
	tableTSV
	tableMarkdown
)

type parsedTable struct {
	kind      tableKind
	rows      [][]string
	delimiter rune
	crlf      bool
}

type tabularCompressor struct {
	minRows      int
	keepFirst    int
	keepLast     int
	queryLimit   int
	relevanceMin float64
}

// NewTabular returns the deterministic CSV, TSV, and Markdown-table compressor.
func NewTabular() Compressor {
	return &tabularCompressor{minRows: 12, keepFirst: 3, keepLast: 2, queryLimit: 24, relevanceMin: 0.30}
}

func (c *tabularCompressor) ContentType() string       { return "tabular" }
func (c *tabularCompressor) SafetyClass() safety.Class { return safety.S4 }
func (c *tabularCompressor) Compress(input []byte) ([]byte, bool) {
	return c.compress(input, "")
}
func (c *tabularCompressor) CompressQuery(input []byte, query string) ([]byte, bool) {
	return c.compress(input, query)
}

// LooksTabular applies the same strict parser used by NewTabular. Detection may
// identify a small table that compression later passes through as not worth it.
func LooksTabular(input []byte) bool {
	table, ok := parseTable(input)
	return ok && len(table.rows) >= 8
}

func (c *tabularCompressor) compress(input []byte, query string) ([]byte, bool) {
	if tabularMarkerRe.Match(input) {
		return nil, false
	}
	table, ok := parseTable(input)
	if !ok {
		return nil, false
	}
	headerRows := 1
	if table.kind == tableMarkdown {
		headerRows = 2
	}
	if len(table.rows) < c.minRows || len(table.rows) <= headerRows+c.keepFirst+c.keepLast+1 {
		return nil, false
	}

	keep := make([]bool, len(table.rows))
	for i := 0; i < headerRows; i++ {
		keep[i] = true
	}
	for i := headerRows; i < headerRows+c.keepFirst && i < len(keep); i++ {
		keep[i] = true
	}
	for i := len(keep) - c.keepLast; i < len(keep); i++ {
		if i >= headerRows {
			keep[i] = true
		}
	}

	docs := make([]string, len(table.rows))
	for i, row := range table.rows {
		docs[i] = strings.Join(row, " ")
		if tabularImportantRe.MatchString(docs[i]) {
			keep[i] = true
		}
	}
	keepNumericExtrema(table.rows, headerRows, keep)
	keepQueryRelevant(keep, docs, query, c.queryLimit, c.relevanceMin)
	keepNonRedundant(docsAsUnits(docs), keep)

	shaped := make([][]string, 0, len(table.rows))
	var run [][]string
	elidedBytes := 0
	flush := func() {
		if len(run) == 0 {
			return
		}
		runBytes := 0
		for _, row := range run {
			runBytes += len(strings.Join(row, ",")) + 1
		}
		summary := summarizeRowRun(table.rows[0], run)
		text := tabularMarker(len(run), summary)
		if !worthEliding(len(run), len(text), runBytes, summary) {
			// Too small a run to describe and too small to be worth a recovery
			// handle: keep the rows themselves and claim nothing for them.
			shaped = append(shaped, run...)
			run = run[:0]
			return
		}
		marker := make([]string, len(table.rows[0]))
		marker[0] = text
		shaped = append(shaped, marker)
		elidedBytes += runBytes
		run = run[:0]
	}
	for i, row := range table.rows {
		if keep[i] {
			flush()
			shaped = append(shaped, row)
		} else {
			run = append(run, row)
		}
	}
	flush()
	if len(shaped) >= len(table.rows) {
		return nil, false
	}
	// The contract line rides as a final row so the table stays a table. Re-entry
	// is short-circuited by tabularMarkerRe above, so it can never be appended
	// twice.
	if wantsElisionNote(elidedBytes) {
		note := make([]string, len(table.rows[0]))
		note[0] = elisionNote("rows")
		shaped = append(shaped, note)
	}

	var out []byte
	if table.kind == tableMarkdown {
		var b strings.Builder
		for _, row := range shaped {
			b.WriteString("| ")
			b.WriteString(strings.Join(row, " | "))
			b.WriteString(" |\n")
		}
		out = []byte(b.String())
		if !bytes.HasSuffix(input, []byte("\n")) {
			out = bytes.TrimSuffix(out, []byte("\n"))
		}
		if table.crlf {
			out = bytes.ReplaceAll(out, []byte("\n"), []byte("\r\n"))
		}
	} else {
		var b bytes.Buffer
		writer := csv.NewWriter(&b)
		writer.Comma = table.delimiter
		writer.UseCRLF = table.crlf
		if err := writer.WriteAll(shaped); err != nil {
			return nil, false
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			return nil, false
		}
		out = b.Bytes()
		if !bytes.HasSuffix(input, []byte("\n")) {
			if table.crlf {
				out = bytes.TrimSuffix(out, []byte("\r\n"))
			} else {
				out = bytes.TrimSuffix(out, []byte("\n"))
			}
		}
	}
	if len(out) >= len(input) {
		return nil, false
	}
	return out, true
}

func parseTable(input []byte) (parsedTable, bool) {
	if !utf8.Valid(input) || len(bytes.TrimSpace(input)) == 0 {
		return parsedTable{}, false
	}
	if table, ok := parseMarkdownTable(input); ok {
		return table, true
	}
	firstLine := string(bytes.SplitN(bytes.TrimSpace(input), []byte("\n"), 2)[0])
	delimiter := ','
	kind := tableCSV
	if strings.Count(firstLine, "\t") >= 1 && strings.Count(firstLine, "\t") > strings.Count(firstLine, ",") {
		delimiter = '\t'
		kind = tableTSV
	}
	if strings.Count(firstLine, string(delimiter)) < 1 {
		return parsedTable{}, false
	}
	reader := csv.NewReader(bytes.NewReader(input))
	reader.Comma = delimiter
	reader.FieldsPerRecord = 0
	reader.ReuseRecord = false
	rows, err := reader.ReadAll()
	if err != nil || len(rows) < 2 {
		return parsedTable{}, false
	}
	fields := len(rows[0])
	if fields < 2 || fields > 128 {
		return parsedTable{}, false
	}
	for _, row := range rows {
		if len(row) != fields {
			return parsedTable{}, false
		}
	}
	return parsedTable{kind: kind, rows: rows, delimiter: delimiter, crlf: bytes.Contains(input, []byte("\r\n"))}, true
}

func parseMarkdownTable(input []byte) (parsedTable, bool) {
	crlf := bytes.Contains(input, []byte("\r\n"))
	normalized := bytes.ReplaceAll(input, []byte("\r\n"), []byte("\n"))
	raw := bytes.Split(bytes.TrimSuffix(normalized, []byte("\n")), []byte("\n"))
	if len(raw) < 3 {
		return parsedTable{}, false
	}
	rows := make([][]string, 0, len(raw))
	for _, line := range raw {
		text := strings.TrimSpace(string(line))
		if !strings.HasPrefix(text, "|") || !strings.HasSuffix(text, "|") {
			return parsedTable{}, false
		}
		parts := strings.Split(strings.Trim(text, "|"), "|")
		row := make([]string, len(parts))
		for i, part := range parts {
			row[i] = strings.TrimSpace(part)
		}
		rows = append(rows, row)
	}
	if len(rows[0]) < 2 || len(rows[1]) != len(rows[0]) {
		return parsedTable{}, false
	}
	for _, cell := range rows[1] {
		if !markdownSeparatorCellRe.MatchString(cell) {
			return parsedTable{}, false
		}
	}
	for _, row := range rows[2:] {
		if len(row) != len(rows[0]) {
			return parsedTable{}, false
		}
	}
	return parsedTable{kind: tableMarkdown, rows: rows, delimiter: '|', crlf: crlf}, true
}

func keepNumericExtrema(rows [][]string, start int, keep []bool) {
	if len(rows) == 0 || start >= len(rows) {
		return
	}
	for column := range rows[0] {
		numeric := 0
		minIndex, maxIndex := -1, -1
		minValue, maxValue := 0.0, 0.0
		for row := start; row < len(rows); row++ {
			value, err := strconv.ParseFloat(strings.TrimSpace(rows[row][column]), 64)
			if err != nil {
				continue
			}
			numeric++
			if minIndex == -1 || value < minValue {
				minIndex, minValue = row, value
			}
			if maxIndex == -1 || value > maxValue {
				maxIndex, maxValue = row, value
			}
		}
		if numeric*10 < (len(rows)-start)*7 {
			continue
		}
		keep[minIndex] = true
		keep[maxIndex] = true
	}
}
