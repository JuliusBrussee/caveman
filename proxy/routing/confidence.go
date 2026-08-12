package routing

import "strings"

// ConfidenceScore is a CHEAP, PURE heuristic that scores how confident a cascade
// rung's answer text looks, in [0,1]. Lower = less confident = should escalate.
//
// HONESTY NOTE — this is deliberately a cheap fallback, not a quality verdict:
//   - In SHADOW mode the real escalation signal is the eval grader's score; this
//     heuristic is never the source of truth there.
//   - In ACTIVE mode it is the fallback used to decide whether to spend a second,
//     more-expensive call — but the heuristic NEVER triggers that call by itself.
//     The proxy owns the escalation decision and the upstream request; this
//     function only reads bytes that already came back.
//   - It scores surface signals only (refusal/hedge phrasing, length, obvious
//     truncation). It cannot judge correctness, so it stays conservative: a
//     normal, substantive answer scores high and is left alone.
//
// f is accepted for future feature-aware scoring (e.g. JSON-mode answers) and is
// intentionally not yet consulted — keeping the heuristic simple and predictable.
func ConfidenceScore(answer string, f Features) float64 {
	trimmed := strings.TrimSpace(answer)
	if trimmed == "" {
		// Empty / whitespace-only answers carry no signal at all.
		return 0
	}
	nonSpace := countNonSpace(trimmed)
	if nonSpace == 0 {
		return 0
	}

	// Start from full confidence and subtract independent surface penalties.
	score := 1.0

	// Refusal / hedge near the START is the strongest escalate signal we have.
	head := strings.ToLower(trimmed)
	if len(head) > refusalScanLen {
		head = head[:refusalScanLen]
	}
	for _, marker := range refusalMarkers {
		if strings.Contains(head, marker) {
			score -= refusalPenalty
			break
		}
	}

	// Very short answers (absolute floor) carry little information.
	if nonSpace < shortAnswerFloor {
		score -= shortPenalty
	}

	// Obvious truncation: a long answer that ends mid-thought, with no terminal
	// punctuation. Short answers are exempt — brevity alone is not truncation.
	if nonSpace >= truncationMinLen && !endsWithTerminalPunct(trimmed) {
		score -= truncationPenalty
	}

	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	return score
}

// ShouldEscalate reports whether an answer's confidence is below the escalation
// threshold tau (escalate when strictly under).
//
// tau is tuned PER-TENANT via the experiment machinery — never a global default.
// Different traffic mixes tolerate different cheap-rung error rates, so a single
// repo-wide tau would over- or under-escalate for everyone.
func ShouldEscalate(score, tau float64) bool {
	return score < tau
}

const (
	shortAnswerFloor = 16  // non-space chars below which an answer is "very short"
	refusalScanLen   = 64  // only scan the answer's head for refusal markers
	truncationMinLen = 200 // only long answers are eligible for the truncation penalty

	refusalPenalty    = 0.6
	shortPenalty      = 0.6
	truncationPenalty = 0.25
)

// refusalMarkers are lowercased substrings whose appearance near the start of an
// answer signals a refusal or hedge rather than a real attempt.
var refusalMarkers = []string{
	"i cannot",
	"i can't",
	"i can not",
	"i'm sorry",
	"i am sorry",
	"i'm unable",
	"i am unable",
	"as an ai",
	"i don't have",
	"i do not have",
}

// countNonSpace counts bytes that are not ASCII whitespace. Multibyte runes count
// per byte, which only ever inflates the length — safe for a length floor.
func countNonSpace(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			continue
		default:
			n++
		}
	}
	return n
}

// endsWithTerminalPunct reports whether s ends with a character that plausibly
// closes a complete thought (sentence punctuation or a common closer).
func endsWithTerminalPunct(s string) bool {
	if s == "" {
		return false
	}
	switch s[len(s)-1] {
	case '.', '!', '?', '"', '\'', ')', ']', '}', '`':
		return true
	}
	return false
}
