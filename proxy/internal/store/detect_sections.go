package store

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	sectionEchoMinSessions       = 5
	sectionDistinctiveLineLength = 40
	sectionDistinctiveLineCap    = 8
)

var markdownSectionHeading = regexp.MustCompile(`^#{2,}\s+(.+?)\s*#*\s*$`)

type sessionTextObservation struct {
	Repo string
	Text string
}

type markdownSection struct {
	Heading string
	Raw     string
	Lines   int
}

type claudeMDSectionEvidence struct {
	Heading string `json:"heading"`
	Tokens  int    `json:"tokens"`
	Lines   int    `json:"lines"`
}

func normalizeEchoText(text string) string {
	return strings.TrimSpace(loopWhitespace.ReplaceAllString(strings.ToLower(text), " "))
}

func claudeMDSectionSinks(cfg configScan, sessions []sessionTextObservation) []Sink {
	var sinks []Sink
	for _, file := range []struct {
		scope string
		snap  *ConfigSnapshot
	}{
		{scope: "user", snap: cfg.ClaudeMDUser},
		{scope: "project", snap: cfg.ClaudeMDProject},
	} {
		if sink, ok := claudeMDSectionSink(file.snap, file.scope, sessions); ok {
			sinks = append(sinks, sink)
		}
	}
	return sinks
}

func claudeMDSectionSink(snap *ConfigSnapshot, scope string, sessions []sessionTextObservation) (Sink, bool) {
	if snap == nil || snap.Path == "" {
		return Sink{}, false
	}
	selected := selectSectionSessions(scope, snap.Path, sessions)
	if len(selected) < sectionEchoMinSessions {
		return Sink{}, false
	}
	raw, err := os.ReadFile(snap.Path)
	if err != nil {
		return Sink{}, false
	}
	var unechoed []claudeMDSectionEvidence
	sectionsEchoed := 0
	tokensPerTurn := 0
	for _, section := range segmentMarkdownSections(string(raw)) {
		lines := distinctiveSectionLines(section.Raw)
		if len(lines) == 0 {
			continue // no eligible line means no echo measurement, not a zero echo
		}
		echoed := false
		for _, line := range lines {
			for _, session := range selected {
				if strings.Contains(session.Text, line) {
					echoed = true
					break // first hit bounds sessions checked for this line
				}
			}
			if echoed {
				break
			}
		}
		if echoed {
			sectionsEchoed++
			continue
		}
		tokens, _ := configTokenCount(section.Raw)
		if tokens <= 0 {
			continue
		}
		tokensPerTurn += tokens
		unechoed = append(unechoed, claudeMDSectionEvidence{
			Heading: section.Heading, Tokens: tokens, Lines: section.Lines,
		})
	}
	// Visible transcript blocks prove this corpus can observe config echoes only
	// after at least one section echoes. Without that positive control, silence is
	// indistinguishable from Claude's system-prompt-only CLAUDE.md injection.
	if sectionsEchoed == 0 || len(unechoed) == 0 || tokensPerTurn <= 0 {
		return Sink{}, false
	}
	return Sink{
		SinkID: "claude_md_sections:" + scope,
		Title:  "CLAUDE.md sections had no distinctive-line echo in the scanned session window",
		Class:  classReducible, Basis: learnBasis, Framing: framingForward,
		TokensPerTurn: int64(tokensPerTurn),
		Evidence: map[string]any{
			"sections":         unechoed,
			"sections_echoed":  sectionsEchoed,
			"sessions_checked": len(selected),
			"method":           "distinctive_line_echo",
			"session_text_cap": maxSectionSessions,
			"session_byte_cap": maxSectionTextBytes,
		},
		Suggestion: "Consider these sections as consent-gated trim candidates. Distinctive-line echo sees only visible transcript text, not CLAUDE.md system-prompt injection; zero echo is window-bounded and not proof a section is unneeded.",
	}, true
}

func selectSectionSessions(scope, configPath string, sessions []sessionTextObservation) []sessionTextObservation {
	if scope != "project" {
		return sessions
	}
	repo := filepath.Clean(filepath.Dir(configPath))
	knownRepo := false
	for _, session := range sessions {
		if filepath.IsAbs(session.Repo) {
			knownRepo = true
			break
		}
	}
	if !knownRepo {
		return sessions
	}
	var selected []sessionTextObservation
	for _, session := range sessions {
		if filepath.IsAbs(session.Repo) && filepath.Clean(session.Repo) == repo {
			selected = append(selected, session)
		}
	}
	return selected
}

func segmentMarkdownSections(text string) []markdownSection {
	lines := strings.Split(text, "\n")
	var sections []markdownSection
	start := 0
	heading := "(preamble)"
	flush := func(end int) {
		if end <= start {
			return
		}
		raw := strings.Join(lines[start:end], "\n")
		if strings.TrimSpace(raw) == "" {
			return
		}
		sections = append(sections, markdownSection{Heading: heading, Raw: raw, Lines: end - start})
	}
	for i, line := range lines {
		match := markdownSectionHeading.FindStringSubmatch(strings.TrimSpace(line))
		if len(match) != 2 {
			continue
		}
		flush(i)
		start = i
		heading = strings.TrimSpace(match[1])
	}
	flush(len(lines))
	return sections
}

func distinctiveSectionLines(raw string) []string {
	seen := map[string]bool{}
	lines := make([]string, 0, sectionDistinctiveLineCap)
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") || utf8.RuneCountInString(trimmed) < sectionDistinctiveLineLength {
			continue
		}
		normalized := normalizeEchoText(trimmed)
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		lines = append(lines, normalized)
		if len(lines) == sectionDistinctiveLineCap {
			break
		}
	}
	return lines
}
