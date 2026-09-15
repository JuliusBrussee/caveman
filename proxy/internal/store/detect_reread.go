package store

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

const (
	rereadMinCalls      = 3
	rereadMinTokenFloor = 5_000
)

type normalizedReadCall struct {
	Path         string
	OutputTokens int
}

type rereadSession struct {
	PathFloors map[string]int
	Floor      int
}

type compactionSession struct {
	Compactions int
	PathFloors  map[string]int
	Floor       int
}

type readActivityTracker struct {
	calls           []normalizedReadCall
	seenPaths       map[string]bool
	preCompactPaths map[string]bool
	compactionPaths map[string]int
	compactionFloor int
	compactions     int
	afterCompaction bool
	markerRun       bool
}

func newReadActivityTracker() readActivityTracker {
	return readActivityTracker{seenPaths: map[string]bool{}, preCompactPaths: map[string]bool{}, compactionPaths: map[string]int{}}
}

func (t *readActivityTracker) observe(event turnEvent, fallbackRepo string) {
	if event.Compaction {
		if !t.markerRun {
			t.compactions++
			t.afterCompaction = true
			t.preCompactPaths = t.seenPaths
			t.seenPaths = map[string]bool{}
		}
		t.markerRun = true
	} else {
		t.markerRun = false
	}
	repo := event.Repo
	if repo == "" {
		repo = fallbackRepo
	}
	for _, call := range event.ToolCalls {
		path, ok := readLikePath(call.Name, call.InputSummary, repo)
		if !ok {
			continue
		}
		outputTokens := estimateTokens(call.OutputText)
		t.calls = append(t.calls, normalizedReadCall{Path: path, OutputTokens: outputTokens})
		if t.afterCompaction && t.preCompactPaths[path] {
			t.compactionPaths[path] += outputTokens
			t.compactionFloor += outputTokens
		}
		t.seenPaths[path] = true
	}
}

func (t readActivityTracker) rereadObservation() (rereadSession, bool) {
	grouped := map[string][]int{}
	for _, call := range t.calls {
		grouped[call.Path] = append(grouped[call.Path], call.OutputTokens)
	}
	observation := rereadSession{PathFloors: map[string]int{}}
	paths := make([]string, 0, len(grouped))
	for path := range grouped {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		costs := grouped[path]
		if len(costs) < rereadMinCalls {
			continue
		}
		floor := 0
		for _, cost := range costs[1:] {
			floor += max(0, cost)
		}
		observation.PathFloors[path] = floor
		observation.Floor += floor
	}
	return observation, len(observation.PathFloors) > 0
}

func (t readActivityTracker) compactionObservation() (compactionSession, bool) {
	if t.compactions == 0 || len(t.compactionPaths) == 0 {
		return compactionSession{}, false
	}
	paths := make(map[string]int, len(t.compactionPaths))
	for path, floor := range t.compactionPaths {
		paths[path] = floor
	}
	return compactionSession{Compactions: t.compactions, PathFloors: paths, Floor: t.compactionFloor}, true
}

func rereadWasteSink(sessions []rereadSession) []Sink {
	pathFloors := map[string]int{}
	sessionsAffected := 0
	tokensObserved := 0
	for _, session := range sessions {
		if len(session.PathFloors) == 0 {
			continue
		}
		sessionsAffected++
		tokensObserved += max(0, session.Floor)
		for path, floor := range session.PathFloors {
			pathFloors[path] += max(0, floor)
		}
	}
	if tokensObserved < rereadMinTokenFloor {
		return nil
	}
	topPaths := rankedPaths(pathFloors, 5)
	return []Sink{{
		SinkID: "reread_waste",
		Title:  fmt.Sprintf("Repeated full file reads consumed at least %d observed tokens", tokensObserved),
		Class:  classBehavioral, Basis: learnBasis, Framing: framingHistorical,
		TokensObserved: int64(tokensObserved),
		Evidence: map[string]any{
			"sessions_affected":     sessionsAffected,
			"distinct_paths":        len(pathFloors),
			"tokens_observed":       tokensObserved,
			"tokens_observed_basis": "bytes4_estimate",
			"overlap_note":          "May overlap compaction_churn when a repeated read follows compaction; totals must not be summed.",
			"top_paths":             topPaths,
		},
		Suggestion: "Repeated full re-reads of the same file are measured; the wrap's recovery/CCR path or narrower reads would cut this. Repetition alone is not proof any specific re-read was unneeded.",
	}}
}

func rankedPaths(floors map[string]int, limit int) []string {
	paths := make([]string, 0, len(floors))
	for path := range floors {
		paths = append(paths, path)
	}
	sort.Slice(paths, func(i, j int) bool {
		if floors[paths[i]] != floors[paths[j]] {
			return floors[paths[i]] > floors[paths[j]]
		}
		return paths[i] < paths[j]
	})
	if len(paths) > limit {
		paths = paths[:limit]
	}
	return paths
}

func readLikePath(toolName, input, repo string) (string, bool) {
	name := strings.ToLower(strings.TrimSpace(toolName))
	candidate := ""
	switch name {
	case "read", "read_file":
		candidate = strings.TrimSpace(input)
	case "bash":
		words, ok := simpleShellWords(input)
		if !ok {
			return "", false
		}
		candidate, ok = bashReadPath(words)
		if !ok {
			return "", false
		}
	default:
		return "", false
	}
	if candidate == "" || strings.ContainsAny(candidate, "\x00\r\n") || strings.HasPrefix(candidate, "{") {
		return "", false
	}
	cleaned := logPathClean(candidate)
	if cleaned == "." {
		return "", false
	}
	if !logPathAbs(cleaned) && logPathAbs(repo) {
		cleaned = logPathClean(logPathClean(repo) + "/" + cleaned)
	}
	return cleaned, true
}

// logPathClean and logPathAbs normalize paths read out of session logs. Those
// paths carry the separator convention of the machine that WROTE the log, not
// the one scanning it, so OS-dependent filepath semantics mis-key them (on
// Windows, filepath.IsAbs("/repo") is false and Clean flips separators, so
// "/repo/a.go" and a repo-joined "a.go" never collide). Slash-normalized
// path-package semantics plus a drive-letter check are deterministic on every
// scanner platform.
func logPathClean(p string) string {
	return path.Clean(strings.ReplaceAll(p, `\`, "/"))
}

func logPathAbs(p string) bool {
	p = strings.ReplaceAll(p, `\`, "/")
	if path.IsAbs(p) {
		return true
	}
	return len(p) >= 3 && p[1] == ':' && p[2] == '/' &&
		('a' <= p[0]|0x20 && p[0]|0x20 <= 'z')
}

// simpleShellWords intentionally accepts only inert quoting/escaping. Shell
// operators and expansion make "exactly one path" unprovable, so they fail closed.
func simpleShellWords(command string) ([]string, bool) {
	if strings.ContainsAny(command, "\n\r;&|><`$") {
		return nil, false
	}
	var words []string
	var current strings.Builder
	quote := rune(0)
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			words = append(words, current.String())
			current.Reset()
		}
	}
	for _, r := range command {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if r == ' ' || r == '\t' {
			flush()
			continue
		}
		current.WriteRune(r)
	}
	if escaped || quote != 0 {
		return nil, false
	}
	flush()
	return words, len(words) > 0
}

func bashReadPath(words []string) (string, bool) {
	if len(words) < 2 {
		return "", false
	}
	command := strings.ToLower(words[0])
	if command != "cat" && command != "sed" && command != "head" && command != "tail" {
		return "", false
	}
	i := 1
	if command == "sed" {
		if i >= len(words) || words[i] != "-n" {
			return "", false
		}
		i++
		if i >= len(words) {
			return "", false
		}
		i++ // sed program
	}
	var positional []string
	for i < len(words) {
		word := words[i]
		if word == "--" {
			positional = append(positional, words[i+1:]...)
			break
		}
		if command != "sed" && strings.HasPrefix(word, "-") {
			if (word == "-n" || word == "--lines") && i+1 < len(words) {
				i += 2
				continue
			}
			i++
			continue
		}
		positional = append(positional, word)
		i++
	}
	return firstStringFromSlice(positional), len(positional) == 1
}

func firstStringFromSlice(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
