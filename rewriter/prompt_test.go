package rewriter

import (
	"strings"
	"testing"
)

// The paper's prompt has four parts (§2.3.1). Losing one is losing the recipe,
// so each is asserted by name.
func TestSystemPromptHasAllFourParts(t *testing.T) {
	for _, part := range []string{promptPartJob, promptPartIOFormat, promptPartWaste, promptPartGuidelines} {
		if !strings.Contains(systemPrompt, part) {
			t.Errorf("system prompt is missing part %q", part)
		}
	}
	for _, category := range []string{"USELESS", "REDUNDANT", "EXPIRED"} {
		if !strings.Contains(systemPrompt, category) {
			t.Errorf("system prompt is missing waste category %q", category)
		}
	}
	// Rewrite-never-delete and the XML contract are the two instructions the
	// acceptance gate depends on the model having been told about.
	for _, phrase := range []string{"short takeaway", `<step id="…">`, "character for", "return it unchanged"} {
		if !strings.Contains(systemPrompt, phrase) {
			t.Errorf("system prompt is missing %q", phrase)
		}
	}
}

func TestBuildUserMessageWindow(t *testing.T) {
	target := []byte("TARGET BODY")
	msg := buildUserMessage(Request{
		StepBytes: target,
		ToolName:  "Bash",
		WindowBytes: [][]byte{
			[]byte("three back"),
			target,
			[]byte("one back"),
			[]byte("newest"),
		},
	})

	// A well-formed a=2, b=1 window puts the target at s-2 and the newest step
	// at s, with s-3 as the extra context step.
	for _, want := range []string{
		`<step id="s-3">`,
		`<step id="s-2">`,
		`<step id="s-1">`,
		`<step id="s">`,
		"TARGET BODY",
		"newest",
		`Target step: "s-2"`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("user message is missing %q\n---\n%s", want, msg)
		}
	}
	if !strings.Contains(msg, "Bash tool") {
		t.Errorf("user message does not name the producing tool\n---\n%s", msg)
	}
}

// A window that does not contain the target is a caller bug. The module drops
// the window rather than rewriting some other step.
func TestBuildUserMessageFallsBackWhenTargetIsNotInWindow(t *testing.T) {
	msg := buildUserMessage(Request{
		StepBytes:   []byte("TARGET BODY"),
		WindowBytes: [][]byte{[]byte("unrelated a"), []byte("unrelated b")},
	})

	if strings.Contains(msg, "unrelated") {
		t.Errorf("mismatched window was still sent as context\n---\n%s", msg)
	}
	if !strings.Contains(msg, "TARGET BODY") || !strings.Contains(msg, `Target step: "s"`) {
		t.Errorf("target step was not presented alone\n---\n%s", msg)
	}
	if !strings.Contains(msg, "unknown tool") {
		t.Errorf("missing tool name did not fall back\n---\n%s", msg)
	}
}
