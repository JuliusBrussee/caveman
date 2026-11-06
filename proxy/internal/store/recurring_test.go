package store

import (
	"strings"
	"testing"
)

// filler pads a payload past minBlockTokens (estimateTokens is bytes/4, so a
// block needs ~720 bytes to be a candidate at all).
func filler(seed string) string {
	return strings.Repeat(seed+" ", 120)
}

// #1459: the repaste miner mined every text payload in a transcript turn, so
// context the HOST and caveman's own hook re-inject each session — Claude
// Code's CLAUDE.md / memory system-reminder, a SessionStart hook result, the
// caveman ruleset — was reported as text the user "pasted again in N
// sessions", with fix_kind cavemem_offload. Nothing can be saved there: the
// host and the hook re-inject it whatever cavemem holds, and acting on the
// recommendation the other way (deleting the source after offloading) stops
// the agent receiving its instructions. Those bytes are already accounted as
// always-loaded setup (claude_md_weight), so mining them double-counts them
// and crowds genuine repeated pastes out of a capped report.
func TestRecurringMinerIgnoresHostAndHookInjectedContext(t *testing.T) {
	reminder := "<system-reminder>\nCodebase and user instructions are shown below. Be sure to adhere to these instructions. IMPORTANT: These instructions OVERRIDE any default behavior.\n\n" + filler("claude md line") + "\n</system-reminder>"
	hookResult := "SessionStart:startup hook success: CAVEMAN MODE ACTIVE — mode: caveman. " + filler("ruleset body")
	ruleset := "Respond terse like smart caveman. All technical substance stay. Only fluff die.\n\n" + filler("rule headline")
	human := "Here is the architecture note I keep re-explaining.\n\n" + filler("human context")

	miner := newRecurringMiner()
	for _, session := range []string{"a.jsonl", "b.jsonl", "c.jsonl"} {
		// Document order matters: the injected payloads sit BEFORE the human
		// one, so a fix that drops them from segmentation would renumber the
		// human block and break the segmenter-v1 locator contract the editing
		// skill re-derives a body with.
		miner.observeTextPayloads("claude", session, 7, "2026-11-04T00:00:00Z",
			[]string{reminder, hookResult, ruleset, human}, false)
	}

	res := miner.result()
	if len(res.Repaste) != 1 {
		var got []string
		for _, e := range res.Repaste {
			got = append(got, e.Locators[0].ContentSHA)
		}
		t.Fatalf("repaste entries = %d, want only the human block: %v", len(res.Repaste), got)
	}
	entry := res.Repaste[0]
	if entry.Sessions != 3 || entry.Occurrences != 3 {
		t.Fatalf("entry = %+v, want the human block across 3 sessions", entry)
	}
	// The locator carries the SHA of the segmented BLOCK, not of the payload:
	// the human payload's heavy second block is what qualified.
	if want := contentSHA256(filler("human context")); entry.Locators[0].ContentSHA != want {
		t.Fatalf("locator content_sha256 = %q, want the human block's %q", entry.Locators[0].ContentSHA, want)
	}
	// The locator addresses a block by (jsonl_line, block_index) and the skill
	// re-segments the line itself, so the index must still count the injected
	// payloads' blocks: reminder (2), hookResult (1), ruleset (2), then the
	// human payload's own short lead-in (1) before its heavy block.
	if entry.Locators[0].BlockIndex != 6 {
		t.Fatalf("locator block_index = %d, want 6 (injected payloads still segmented)", entry.Locators[0].BlockIndex)
	}
}

// A payload is injected or it is not; the guard must not spread across a turn.
// A user's own paste in the same turn as a system-reminder still counts, and a
// block that merely mentions caveman is not caveman's ruleset.
func TestRecurringMinerKeepsHumanBlocksThatOnlyMentionInjectedText(t *testing.T) {
	mentions := "I read the <system-reminder> docs and want the same shape here; CAVEMAN MODE ACTIVE is what the hook prints.\n\n" + filler("my own notes")

	miner := newRecurringMiner()
	for _, session := range []string{"a.jsonl", "b.jsonl", "c.jsonl"} {
		miner.observeTextPayloads("claude", session, 1, "2026-11-04T00:00:00Z", []string{mentions}, false)
	}

	if got := len(miner.result().Repaste); got != 1 {
		t.Fatalf("repaste entries = %d, want 1: a human block is not injected context", got)
	}
}

// The predicate's contract, stated directly: every rule is anchored at the
// payload start, so a human block that quotes one of these is still mined.
func TestInjectedContextPayload(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"system-reminder envelope", "<system-reminder>\nmemory\n</system-reminder>", true},
		{"claude md preamble", "Codebase and user instructions are shown below. Be sure to adhere to these instructions.", true},
		{"caveman banner", "CAVEMAN MODE ACTIVE — mode: ultracave. Behavior defined by /caveman-ultracave skill.", true},
		{"caveman sentinel", "Respond terse like smart caveman. All technical substance stay. Only fluff die.", true},
		{"session start hook result", "SessionStart:startup hook success: rules", true},
		{"pre tool use hook result", "PreToolUse:Bash hook failed: boom", true},
		{"hook result with no source", "Stop hook completed: nothing to do", true},
		{"leading blank lines", "\n\n  <system-reminder>\nmemory\n</system-reminder>", true},
		{"crlf payload", "<system-reminder>\r\nmemory\r\n</system-reminder>", true},
		{"quoted reminder mid-sentence", "The <system-reminder> block is what I mean.", false},
		{"quoted banner mid-sentence", "It prints CAVEMAN MODE ACTIVE at startup.", false},
		{"prose about hooks", "The hook success path is the one I care about.", false},
		{"hook words without the colon", "Stop hook completed, finally, after three tries.", false},
		{"lower-case event word", "sessionstart:startup hook success: rules", false},
		{"ordinary paste", "Here is the architecture note I keep re-explaining.", false},
		{"empty", "", false},
		{"whitespace only", "   \n\t", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := injectedContextPayload(c.in); got != c.want {
				t.Fatalf("injectedContextPayload(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
