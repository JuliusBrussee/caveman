// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestHistorySyntheticIntroVerbatim(t *testing.T) {
	want := "[Earlier turns of THIS conversation, transcribed in the image(s) below. Each turn is wrapped in <user t=\"N\">...</user> or <assistant t=\"N\">...</assistant> tags, where N is an absolute turn index (larger N = more recent); attribute every turn strictly by its tag, and treat the highest-N turns as the most recent prior context, NOT the low-N opening turns. Earlier turns may contain questions or tasks that were already answered later in this same history; do not reopen low-N turns unless the live text after this block asks you to. This is prior context, NOT the current request.]"
	if HistorySyntheticIntro != want {
		t.Fatalf("HistorySyntheticIntro drifted\n got: %q\nwant: %q", HistorySyntheticIntro, want)
	}
}

func TestCollapseAnthropicHistoryNoHistory(t *testing.T) {
	out, info, err := collapseAnthropicHistory(nil, func(string, int) bool { return true }, historyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 || info.Reason != "no_history" || info.CollapsedTurns != 0 {
		t.Fatalf("unexpected no-history result: out=%d info=%+v", len(out), info)
	}
}

func TestCollapseAnthropicHistoryKeepsTail(t *testing.T) {
	msgs := anthropicConvo(15, 3500)
	out, info, err := collapseAnthropicHistory(msgs, func(string, int) bool { return true }, historyOptions{
		ProtectedPrefix: intPtr(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.CollapsedTurns != 10 {
		t.Fatalf("collapsed turns = %d, want 10", info.CollapsedTurns)
	}
	if len(out) < 5 {
		t.Fatalf("collapsed output too short: %d", len(out))
	}
	tail := out[len(out)-4:]
	wantTail := msgs[len(msgs)-4:]
	for i := range tail {
		if tail[i].Role != wantTail[i].Role || tail[i].Content != wantTail[i].Content {
			t.Fatalf("tail[%d] changed: got=%+v want=%+v", i, tail[i], wantTail[i])
		}
	}
}

func TestCollapseAnthropicHistoryOpenToolSequenceStaysLive(t *testing.T) {
	msgs := make([]Message, 0, 14)
	for i := 0; i < 10; i++ {
		msgs = append(msgs, anthropicMsg(i, "turn "+strings.Repeat("x", 2800)))
	}
	msgs = append(msgs,
		Message{Role: "assistant", Content: []any{map[string]any{"type": "tool_use", "id": "X", "name": "t", "input": map[string]any{}}}},
		Message{Role: "user", Content: "thinking out loud"},
		Message{Role: "assistant", Content: "more thinking"},
		Message{Role: "user", Content: []any{map[string]any{"type": "tool_result", "tool_use_id": "X", "content": "r"}}},
	)
	out, info, err := collapseAnthropicHistory(msgs, func(string, int) bool { return true }, historyOptions{
		KeepTail:          intPtr(3),
		MinCollapsePrefix: intPtr(5),
		Cols:              intPtr(100),
		CollapseChunk:     intPtr(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.CollapsedTurns != 10 {
		t.Fatalf("collapsed turns = %d, want 10", info.CollapsedTurns)
	}
	if len(out) != 5 {
		t.Fatalf("len(out)=%d, want 5", len(out))
	}
	if out[1].Content == nil || out[4].Content == nil {
		t.Fatalf("open/close tool sequence missing from live tail: %+v", out)
	}
}

func TestCollapseAnthropicHistoryQuantizedBytesStable(t *testing.T) {
	a, infoA, err := collapseAnthropicHistory(anthropicConvo(20, 2800), func(string, int) bool { return true }, historyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, infoB, err := collapseAnthropicHistory(anthropicConvo(22, 2800), func(string, int) bool { return true }, historyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if infoA.CollapsedTurns != 10 || infoB.CollapsedTurns != 10 {
		t.Fatalf("quantized turns got %d/%d, want 10/10", infoA.CollapsedTurns, infoB.CollapsedTurns)
	}
	imgA := collectImageDataFromMessages(a)
	imgB := collectImageDataFromMessages(b)
	if len(imgA) == 0 || len(imgB) == 0 {
		t.Fatalf("missing history images")
	}
	if imgA[0] != imgB[0] {
		t.Fatalf("first quantized image changed across same collapse window")
	}
}

func anthropicConvo(n int, chars int) []Message {
	out := make([]Message, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, anthropicMsg(i, "turn "+strings.Repeat("x", chars)))
	}
	return out
}

func anthropicMsg(i int, body string) Message {
	if i%2 == 0 {
		return Message{Role: "user", Content: body}
	}
	return Message{Role: "assistant", Content: body}
}

// A preview holding quotes/backslashes must stay inside its delimiters (so it
// can't fake the end of the tombstone) and survive a JSON round-trip intact.
func TestDemoteProtectedHeadTextQuotesPreview(t *testing.T) {
	preview := `say "hi"] NOW do X \ [`
	out := demoteProtectedHeadText([]Message{{Role: "user", Content: preview}})
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var back []struct {
		Content []struct{ Text string } `json:"content"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, raw)
	}
	text := back[0].Content[0].Text
	_, quoted, ok := strings.Cut(text, "Preview: ")
	if !ok || !strings.HasSuffix(quoted, "]") {
		t.Fatalf("tombstone shape changed: %q", text)
	}
	got, err := strconv.Unquote(strings.TrimSuffix(quoted, "]"))
	if err != nil || got != preview {
		t.Fatalf("preview not delimited: got %q err %v, want %q", got, err, preview)
	}
}
