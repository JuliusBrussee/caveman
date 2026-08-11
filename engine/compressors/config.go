package compressors

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/JuliusBrussee/caveman/engine/safety"
	"gopkg.in/yaml.v3"
)

var (
	yamlKeyRe          = regexp.MustCompile(`^\s*(?:-\s*)?[A-Za-z0-9_.-]+\s*:\s*`)
	configAssignmentRe = regexp.MustCompile(`^\s*[A-Za-z0-9_.-]+\s*=\s*\S`)
	configSectionRe    = regexp.MustCompile(`^\s*\[[A-Za-z0-9_.:"' -]+\]\s*(?:[#;].*)?$`)
	configImportantRe  = regexp.MustCompile(`(?i)\b(ERROR|FAIL|FAILED|FATAL|PANIC|EXCEPTION|WARNING|SECURITY|SECRET|TOKEN|PASSWORD|DENIED|REJECTED|ROLLBACK)\b`)
	configMarkerRe     = regexp.MustCompile(`config lines elided \(caveman\)`)
	yamlBlockScalarRe  = regexp.MustCompile(`:\s*[|>][0-9+-]*\s*(?:#.*)?$`)
)

type configKind uint8

const (
	configYAML configKind = iota + 1
	configTOML
)

type configCompressor struct {
	minLines     int
	keepHead     int
	keepTail     int
	queryLimit   int
	relevanceMin float64
}

// NewConfig returns the deterministic YAML, TOML, and INI compressor.
func NewConfig() Compressor {
	return &configCompressor{minLines: 14, keepHead: 3, keepTail: 2, queryLimit: 16, relevanceMin: 0.30}
}

func (c *configCompressor) ContentType() string       { return "config" }
func (c *configCompressor) SafetyClass() safety.Class { return safety.S4 }
func (c *configCompressor) Compress(input []byte) ([]byte, bool) {
	return c.compress(input, "")
}
func (c *configCompressor) CompressQuery(input []byte, query string) ([]byte, bool) {
	return c.compress(input, query)
}

// LooksConfig recognizes only dense YAML/TOML/INI payloads. Low-confidence
// content stays ordinary text.
func LooksConfig(input []byte) bool {
	_, lines, ok := parseConfig(input)
	return ok && len(lines) >= 12
}

func (c *configCompressor) compress(input []byte, query string) ([]byte, bool) {
	if configMarkerRe.Match(input) {
		return nil, false
	}
	kind, lines, ok := parseConfig(input)
	if !ok || len(lines) < c.minLines {
		return nil, false
	}
	keep := make([]bool, len(lines))
	for i := 0; i < c.keepHead && i < len(lines); i++ {
		keep[i] = true
	}
	for i := len(lines) - c.keepTail; i < len(lines); i++ {
		if i >= 0 {
			keep[i] = true
		}
	}
	docs := make([]string, len(lines))
	for i, line := range lines {
		docs[i] = string(line)
		if configImportantRe.Match(line) {
			keep[i] = true
		}
		if kind == configYAML && indentation(line) == 0 && yamlKeyRe.Match(line) {
			keep[i] = true
		}
	}
	keepQueryRelevant(keep, docs, query, c.queryLimit, c.relevanceMin)
	keepNonRedundant(lines, keep)
	if kind == configYAML {
		expandYAMLContext(lines, keep)
	} else {
		expandSectionContext(lines, keep)
	}

	out := make([][]byte, 0, len(lines))
	dropped := 0
	flush := func() {
		if dropped == 0 {
			return
		}
		out = append(out, []byte(fmt.Sprintf("# … %d config lines elided (caveman) …", dropped)))
		dropped = 0
	}
	for i, line := range lines {
		if keep[i] {
			flush()
			out = append(out, line)
		} else {
			dropped++
		}
	}
	flush()
	if len(out) >= len(lines) {
		return nil, false
	}
	sep := []byte("\n")
	if bytes.Contains(input, []byte("\r\n")) {
		sep = []byte("\r\n")
	}
	result := bytes.Join(out, sep)
	if bytes.HasSuffix(input, sep) {
		result = append(result, sep...)
	}
	if len(result) >= len(input) {
		return nil, false
	}
	if kind == configYAML && !validYAML(result) {
		return nil, false
	}
	return result, true
}

func parseConfig(input []byte) (configKind, [][]byte, bool) {
	if !utf8.Valid(input) || len(bytes.TrimSpace(input)) == 0 {
		return 0, nil, false
	}
	normalized := bytes.ReplaceAll(input, []byte("\r\n"), []byte("\n"))
	lines := bytes.Split(bytes.TrimSuffix(normalized, []byte("\n")), []byte("\n"))
	yamlKeys, assignments, sections, prose := 0, 0, 0, 0
	yamlBlockScalar := false
	for _, raw := range lines {
		line := bytes.TrimSpace(raw)
		if len(line) == 0 || bytes.HasPrefix(line, []byte("#")) || bytes.HasPrefix(line, []byte(";")) {
			continue
		}
		if yamlBlockScalarRe.Match(line) {
			yamlBlockScalar = true
		}
		switch {
		case configSectionRe.Match(line):
			sections++
		case configAssignmentRe.Match(line):
			assignments++
		case yamlKeyRe.Match(line):
			yamlKeys++
		case bytes.HasPrefix(line, []byte("- ")):
			// YAML sequence item. Parent key density establishes confidence.
		default:
			prose++
		}
	}
	significant := yamlKeys + assignments + sections + prose
	if significant == 0 {
		return 0, nil, false
	}
	if sections >= 2 && assignments >= 4 && prose == 0 &&
		!bytes.Contains(input, []byte(`"""`)) && !bytes.Contains(input, []byte(`'''`)) {
		return configTOML, lines, true
	}
	if yamlKeys >= 6 && yamlKeys*2 >= significant && prose*4 <= significant &&
		!yamlBlockScalar && validYAML(input) {
		return configYAML, lines, true
	}
	return 0, nil, false
}

func validYAML(input []byte) bool {
	decoder := yaml.NewDecoder(bytes.NewReader(input))
	documents := 0
	for {
		var document any
		err := decoder.Decode(&document)
		if err == io.EOF {
			return documents > 0
		}
		if err != nil {
			return false
		}
		documents++
	}
}

func indentation(line []byte) int {
	n := 0
	for _, ch := range line {
		if ch == ' ' {
			n++
			continue
		}
		if ch == '\t' {
			n += 2
			continue
		}
		break
	}
	return n
}

func yamlParentLine(line []byte) bool {
	trimmed := strings.TrimSpace(string(line))
	return yamlKeyRe.Match(line) && (strings.HasSuffix(trimmed, ":") || strings.HasPrefix(trimmed, "- "))
}

func expandYAMLContext(lines [][]byte, keep []bool) {
	original := append([]bool(nil), keep...)
	for i, selected := range original {
		if !selected {
			continue
		}
		indent := indentation(lines[i])
		for parentIndent := indent - 1; parentIndent >= 0; {
			found := false
			for j := i - 1; j >= 0; j-- {
				candidateIndent := indentation(lines[j])
				if candidateIndent < indent && yamlParentLine(lines[j]) {
					keep[j] = true
					indent = candidateIndent
					parentIndent = indent - 1
					found = true
					break
				}
			}
			if !found {
				break
			}
		}
	}
	// Parent discovery above may newly select a sequence item such as
	// `- name: settlement-writer`. Expand children from the updated keep set so
	// query-relevant nested values stay attached to that item and YAML remains
	// semantically useful, not merely parseable.
	for i, selected := range keep {
		if !selected || !yamlParentLine(lines[i]) {
			continue
		}
		base := indentation(lines[i])
		for j := i + 1; j < len(lines) && j <= i+8; j++ {
			if len(bytes.TrimSpace(lines[j])) == 0 {
				continue
			}
			if indentation(lines[j]) <= base {
				break
			}
			keep[j] = true
		}
	}
}

func expandSectionContext(lines [][]byte, keep []bool) {
	section := -1
	for i, line := range lines {
		if configSectionRe.Match(line) {
			section = i
		}
		if keep[i] && section >= 0 {
			keep[section] = true
		}
	}
	original := append([]bool(nil), keep...)
	for i, selected := range original {
		if !selected || !configSectionRe.Match(lines[i]) {
			continue
		}
		for j := i + 1; j < len(lines) && j <= i+8; j++ {
			if configSectionRe.Match(lines[j]) {
				break
			}
			keep[j] = true
		}
	}
}
