package rewriter

import (
	"regexp"
	"strings"
)

// The deterministic acceptance gate. A rewrite that fails any check is
// rejected and the caller keeps the original block verbatim, forever
// eligible-but-unflipped — rejections cost nothing, so every check here is
// allowed to be over-inclusive. The only unsafe direction is accepting a
// rewrite that dropped a failure the agent still has to act on: that turns a
// token saving into a silent wrong turn, which is the failure class this whole
// component exists to avoid.
//
// The gate is deliberately strict. A 200-warning compiler log becomes
// permanently unflippable under the unconditional source-location rule; that
// cost is accepted rather than carved out, because a carve-out is only
// defensible once the replay grid prices that block class as material.
const (
	reasonBelowTheta    = "below_theta"
	reasonAPIError      = "api_error"
	reasonTruncated     = "gate_failure_truncated"
	reasonStepWrapper   = "gate_failure_step_wrapper"
	reasonFence         = "gate_failure_fence"
	reasonEmpty         = "gate_failure_empty"
	reasonTokens        = "gate_failure_tokens"
	reasonFailureSignal = "gate_failure_failure_signal"
	reasonFailureDetail = "gate_failure_failure_detail"
	reasonCount         = "gate_failure_count"
	reasonExitCode      = "gate_failure_exit_code"
	reasonReference     = "gate_failure_reference"
)

// failureNeedles are matched case-insensitively, so "fail" also covers FAILED,
// Failure and failing, and "panic" covers Rust's "panicked at". Over-matching
// only makes the gate stricter: it widens both the set of tokens that must
// survive and the set of lines whose references are protected.
var failureNeedles = []string{
	"fail", "error", "exception", "traceback", "panic", "fatal",
	"warn", "err!", "not found", "no such file", "timed out", "timeout",
	"segmentation", "core dumped", "killed", "aborted",
	"conflict", "denied", "refused", "unable to", "cannot ",
}

// exitStatusPattern catches the two spellings a shell/toolchain uses for a
// process result. Only non-zero codes are load-bearing; "exit code 0" carries
// no failure the agent must recover from.
var exitStatusPattern = regexp.MustCompile(`(?i)exit (?:code|status) (\d+)`)

// countPattern catches tallies an agent reasons about directly. Rewriting
// "5 failed" as "1 failed" keeps every signal word and still lies about the
// size of the problem, so the number must survive with its noun.
//
// Two shapes are deliberately excluded, both of which produced false tallies in
// probe corpora: whitespace is horizontal only, so a version line ending in a
// digit followed by a newline and "error:" is not a tally; and the digit may
// not be preceded by a colon or a dot, so eslint's "8:1   warning" column and a
// decimal are not tallies either. The tally itself is submatch 1.
var countPattern = regexp.MustCompile(`(?i)(?:^|[^\w:.])(\d+[ \t]+(?:failed|failures?|errors?|warnings?|problems?))\b`)

// sourceLocationPatterns are protected UNCONDITIONALLY — anywhere in the
// original, not only on lines carrying a failure word. Most toolchains
// (go build, tsc, gcc, rustc, eslint) emit the location on its own line with no
// signal word on it, so failure-line scoping left them unprotected.
var sourceLocationPatterns = []*regexp.Regexp{
	// path.ext:line[:col]. The :line suffix is mandatory here; a bare path with
	// no location stays failure-line-scoped, or every mentioned filename would
	// be frozen into the block.
	regexp.MustCompile(`[\w./\\+@-]*[\w-]{2,}\.[A-Za-z][A-Za-z0-9]{0,7}:\d+(?::\d+)?`),
	// Extensionless path:line[:col] ("Makefile:12"). A letter is required before
	// the colon so a clock time ("14:03:22") is not mistaken for a location.
	regexp.MustCompile(`[\w./\\+@-]*[A-Za-z][\w./\\+@-]*:\d+(?::\d+)?`),
	// tsc / MSVC: path.ext(line,col).
	regexp.MustCompile(`[\w./\\+@-]*[\w-]{2,}\.[A-Za-z][A-Za-z0-9]{0,7}\(\d+(?:,\d+)?\)`),
	// CPython traceback frames: File "…", line N.
	regexp.MustCompile(`(?i)file "[^"]+", line \d+`),
}

// bareReferencePattern matches a path with no location suffix. Protected only
// on failure lines.
var bareReferencePattern = regexp.MustCompile(`[\w./\\+@-]*[\w-]{2,}\.[A-Za-z][A-Za-z0-9]{0,7}`)

// bareLineColPattern matches eslint-style indented "12:5" coordinates, which
// carry no filename of their own. Protected only on failure lines, because
// unconditionally they would freeze every ratio and timestamp in a block.
var bareLineColPattern = regexp.MustCompile(`\b\d+:\d+\b`)

// Accept reports whether rewritten may replace original under threshold theta,
// and the reject reason when it may not. It is a pure function of its
// arguments: no clock, no network, no state. The replay grid re-scores a
// captured corpus through this same function, so an offline verdict and a live
// verdict cannot diverge.
func Accept(original, rewritten []byte, theta int) (bool, string) {
	reason := accept(original, rewritten, theta)
	return reason == "", reason
}

// accept returns the reject reason, or "" when the rewrite may be used.
//
// Check order is: shape, then saving, then fidelity. The saving check runs
// before the fidelity checks because a rewrite that does not clear θ is
// rejected whatever it preserved (AgentDiet's Algorithm 1 line 22), and
// reporting reasonTokens for it is more useful than reporting whichever
// fidelity check happened to trip first.
func accept(original, rewritten []byte, theta int) string {
	if len(strings.TrimSpace(string(rewritten))) == 0 {
		return reasonEmpty
	}
	if countTokens(original)-countTokens(rewritten) <= theta {
		return reasonTokens
	}

	orig := string(original)
	rew := string(rewritten)
	lowerOrig := strings.ToLower(orig)
	lowerRew := strings.ToLower(rew)

	for _, needle := range failureNeedles {
		if strings.Contains(lowerOrig, needle) && !strings.Contains(lowerRew, needle) {
			return reasonFailureSignal
		}
	}

	// Tallies must survive with their number attached, matched on word
	// boundaries so "1 failed" is not satisfied by "21 failed".
	for _, count := range failureCounts(orig) {
		if !survivesVerbatim(rew, count) {
			return reasonCount
		}
	}

	// Word-boundary matched, so "exit code 1" is not satisfied by "exit code 12".
	for _, status := range nonZeroExitStatuses(orig) {
		if !survivesFold(rew, status) {
			return reasonExitCode
		}
	}

	// Locations are the coordinates the agent needs to act on a failure, so they
	// survive byte-for-byte or not at all — paraphrase does not count.
	for _, loc := range sourceLocations(orig) {
		if !strings.Contains(rew, loc) {
			return reasonReference
		}
	}
	for _, ref := range failureReferences(orig) {
		if !strings.Contains(rew, ref) {
			return reasonReference
		}
	}

	// A generic word such as "error" cannot stand in for several distinct
	// failures. Keep each original failure-bearing line verbatim (ignoring only
	// surrounding horizontal whitespace), so a rewrite cannot preserve one
	// error and silently erase another with different corrective information.
	rewrittenFailures := failureDetails(rew)
	for _, failure := range failureDetails(orig) {
		if !failureDetailSurvives(rewrittenFailures, failure) {
			return reasonFailureDetail
		}
	}
	return ""
}

// survivesVerbatim reports whether phrase appears in text, case-sensitively and
// on word boundaries.
func survivesVerbatim(text, phrase string) bool {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(phrase) + `\b`).MatchString(text)
}

// survivesFold is survivesVerbatim, case-insensitively.
func survivesFold(text, phrase string) bool {
	return regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(phrase) + `\b`).MatchString(text)
}

// failureCounts returns each distinct "<n> failed/errors/warnings/problems"
// tally in text, in first-seen order.
func failureCounts(text string) []string {
	var out []string
	for _, m := range countPattern.FindAllStringSubmatch(text, -1) {
		out = append(out, m[1])
	}
	return distinct(out)
}

// nonZeroExitStatuses returns each distinct non-zero "exit code N" / "exit
// status N" phrase in text, in first-seen order.
func nonZeroExitStatuses(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range exitStatusPattern.FindAllStringSubmatch(text, -1) {
		if strings.Trim(m[1], "0") == "" {
			continue
		}
		key := strings.ToLower(m[0])
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, m[0])
	}
	return out
}

// sourceLocations returns every distinct file-and-line reference anywhere in
// text, in first-seen order.
func sourceLocations(text string) []string {
	var all []string
	for _, pattern := range sourceLocationPatterns {
		all = append(all, pattern.FindAllString(text, -1)...)
	}
	return distinct(all)
}

// failureReferences returns each distinct bare path and bare line:col
// coordinate that appears on a line carrying a failure signal, in first-seen
// order. References on non-failure lines are not protected: eliding
// "ok  pkg/foo 0.01s" is the saving this component is for.
func failureReferences(text string) []string {
	var all []string
	for _, line := range strings.Split(text, "\n") {
		if !isFailureLine(line) {
			continue
		}
		for _, ref := range bareReferencePattern.FindAllString(line, -1) {
			// Trailing sentence punctuation is not part of the path; leading
			// characters are ("./foo.py" must survive as written).
			all = append(all, strings.TrimRight(ref, ".,;:"))
		}
		all = append(all, bareLineColPattern.FindAllString(line, -1)...)
	}
	return distinct(all)
}

// failureDetails returns each distinct non-empty failure-bearing line with
// surrounding whitespace removed. Internal content remains byte-for-byte.
func failureDetails(text string) []string {
	var failures []string
	for _, line := range strings.Split(text, "\n") {
		if !isFailureLine(line) {
			continue
		}
		detail := strings.TrimSpace(line)
		if strings.EqualFold(strings.Trim(detail, "=-_*# "), "failures") {
			continue
		}
		failures = append(failures, detail)
	}
	return distinct(failures)
}

func failureDetailSurvives(rewrittenFailures []string, original string) bool {
	for _, rewritten := range rewrittenFailures {
		// A rewriter may prefix a standalone diagnostic with its preserved
		// source location. Suffix matching permits that useful compaction while
		// preventing a shorter error from being satisfied by a longer one.
		if rewritten == original || strings.HasSuffix(rewritten, original) {
			return true
		}
	}
	return false
}

func isFailureLine(line string) bool {
	lower := strings.ToLower(line)
	for _, needle := range failureNeedles {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func distinct(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
