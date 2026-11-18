package engine

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Codex in code mode runs commands from a JavaScript cell, and a command's
// result comes back to the model as one JSON line per call after a few header
// lines:
//
//	Total output lines: 2
//
//	{"chunk_id":"7ae69c","wall_time_seconds":0.0003,"exit_code":0,"original_token_count":18480,"output":"INFO …\nINFO …\n"}
//
// The command's output is the content; the JSON around it is presentation, like
// a file wrapper. Left in place, the whole result reads as text with one long
// line and nothing compresses. This unwraps the largest `output` string so the
// compressor sees the real lines, and puts the JSON back around what it returns.
type codexExecWrapper struct {
	prefix  []byte
	suffix  []byte
	present bool
}

var (
	codexOutputKey = []byte(`"output":"`)
	codexLineKeys  = [][]byte{[]byte(`"exit_code":`), []byte(`"chunk_id":`)}
)

// unwrapCodexExec finds the result line with the largest `output` string. A line
// qualifies only when it is a JSON object ending in its `output` string (Codex's
// field order), names `exit_code` or `chunk_id` before it, and that string decodes:
// Codex may cut a long result in the middle, so the rest of the line need not parse,
// but the string itself must. Anything else is left as it is.
func unwrapCodexExec(input []byte) ([]byte, codexExecWrapper) {
	var (
		best      []byte
		bestStart int
		bestEnd   int
	)
	for start := 0; start < len(input); {
		end := bytes.IndexByte(input[start:], '\n')
		if end < 0 {
			end = len(input)
		} else {
			end += start
		}
		line := bytes.TrimRight(input[start:end], " \t\r")
		if bytes.HasPrefix(line, []byte(`{"`)) && bytes.HasSuffix(line, []byte(`"}`)) {
			if key := bytes.LastIndex(line, codexOutputKey); key > 0 && hasCodexKey(line[:key]) {
				raw := line[key+len(codexOutputKey) : len(line)-2]
				if len(raw) > len(best) {
					var value string
					quoted := append(append([]byte{'"'}, raw...), '"')
					if json.Unmarshal(quoted, &value) == nil && value != "" && strings.TrimSpace(value) != "" {
						best, bestStart, bestEnd = []byte(value), start+key+len(codexOutputKey), start+len(line)-2
					}
				}
			}
		}
		start = end + 1
	}
	if best == nil {
		return input, codexExecWrapper{}
	}
	return best, codexExecWrapper{prefix: input[:bestStart], suffix: input[bestEnd:], present: true}
}

func hasCodexKey(head []byte) bool {
	for _, key := range codexLineKeys {
		if bytes.Contains(head, key) {
			return true
		}
	}
	return false
}

// rewrap encodes the compressed output back into the JSON string it came from,
// so the line stays valid JSON (no HTML escaping, as Codex writes it).
func (w codexExecWrapper) rewrap(out []byte) []byte {
	if !w.present {
		return out
	}
	var encoded bytes.Buffer
	enc := json.NewEncoder(&encoded)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(string(out)) // a string always encodes
	quoted := bytes.TrimSuffix(encoded.Bytes(), []byte("\n"))
	var b bytes.Buffer
	b.Grow(len(w.prefix) + len(quoted) + len(w.suffix))
	b.Write(w.prefix)
	b.Write(quoted[1 : len(quoted)-1])
	b.Write(w.suffix)
	return b.Bytes()
}
