// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const openAIOpeningMarker = "OPENING_PROMPT_SHOULD_BE_HISTORY"
const openAILiveMarker = "LIVE_CURRENT_PROMPT_SHOULD_STAY_TEXT"

func TestOpenAIHistoryTurnLowering(t *testing.T) {
	responses := OpenAIResponsesItemsToTurns([]any{
		map[string]any{"role": "user", "content": "hello"},
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "hi there"}}},
		map[string]any{"type": "function_call", "call_id": "c1", "name": "read", "arguments": `{"path":"a"}`},
		map[string]any{"type": "function_call_output", "call_id": "c1", "output": "file body"},
		map[string]any{"type": "reasoning", "summary": []any{}},
		map[string]any{"type": "item_reference", "id": "x"},
	})
	if responses[0].Text != "<user t=\"0\">\nhello\n</user>" {
		t.Fatalf("bad user lowering: %q", responses[0].Text)
	}
	if !strings.Contains(responses[1].Text, "<assistant t=\"1\">") {
		t.Fatalf("bad assistant lowering: %q", responses[1].Text)
	}
	if !reflectStringSlices(responses[2].OpenIDs, []string{"c1"}) || !strings.Contains(responses[2].Text, "[tool_use read]") {
		t.Fatalf("bad function_call lowering: %+v", responses[2])
	}
	if !reflectStringSlices(responses[3].CloseIDs, []string{"c1"}) || !strings.Contains(responses[3].Text, "[tool_result]") {
		t.Fatalf("bad function_call_output lowering: %+v", responses[3])
	}
	if responses[4].Text != "" || responses[4].Opaque {
		t.Fatalf("reasoning should be empty non-opaque")
	}
	if !responses[5].Opaque {
		t.Fatalf("unknown item kind should be opaque")
	}

	chat := OpenAIChatMessagesToTurns([]any{
		map[string]any{"role": "assistant", "content": "calling", "tool_calls": []any{
			map[string]any{"id": "tc1", "function": map[string]any{"name": "grep", "arguments": `{"q":"x"}`}},
		}},
		map[string]any{"role": "tool", "tool_call_id": "tc1", "content": "match"},
	})
	if !reflectStringSlices(chat[0].OpenIDs, []string{"tc1"}) || !strings.Contains(chat[0].Text, "[tool_use grep]") {
		t.Fatalf("bad chat assistant tool lowering: %+v", chat[0])
	}
	if !reflectStringSlices(chat[1].CloseIDs, []string{"tc1"}) {
		t.Fatalf("bad chat tool result lowering: %+v", chat[1])
	}
}

func TestPlanGptCollapseGatesAndClosedBoundaries(t *testing.T) {
	yes := func(string, int) bool { return true }
	no := func(string, int) bool { return false }

	plan, err := PlanGptCollapse(openAIPlainTurns(8, 1000), 0, yes, GptHistoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Reason != "prefix_too_short" {
		t.Fatalf("reason=%q", plan.Reason)
	}

	plan, err = PlanGptCollapse(openAIPlainTurns(20, 5), 0, yes, GptHistoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Reason != "below_min_tokens" {
		t.Fatalf("reason=%q", plan.Reason)
	}

	plan, err = PlanGptCollapse(openAIPlainTurns(40, 1000), 0, no, GptHistoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Reason != "not_profitable" {
		t.Fatalf("reason=%q", plan.Reason)
	}

	plan, err = PlanGptCollapse(openAIPlainTurns(40, 1000), 0, yes, GptHistoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Images) == 0 || plan.EndExclusive > 40-DefaultGptHistoryOptions().KeepTail {
		t.Fatalf("expected collapsed prefix, plan=%+v", plan)
	}

	turns := openAIPlainTurns(30, 1000)
	turns[18] = GptHistoryTurn{Text: "[tool_use x]\n{}", OpenIDs: []string{"open1"}}
	turns[25] = GptHistoryTurn{Text: "[tool_result]\nok", CloseIDs: []string{"open1"}}
	opts := DefaultGptHistoryOptions()
	opts.CollapseChunk = 0
	plan, err = PlanGptCollapse(turns, 0, yes, opts)
	if err != nil {
		t.Fatal(err)
	}
	if plan.EndExclusive > 18 {
		t.Fatalf("collapse ended inside open tool call: %d", plan.EndExclusive)
	}

	turns = openAIPlainTurns(40, 1000)
	turns[15] = GptHistoryTurn{Opaque: true}
	plan, err = PlanGptCollapse(turns, 0, yes, opts)
	if err != nil {
		t.Fatal(err)
	}
	if plan.EndExclusive > 15 {
		t.Fatalf("collapse crossed opaque barrier: %d", plan.EndExclusive)
	}
}

func TestPlanGptCollapseTokenFloorAndAppendOnlyImages(t *testing.T) {
	yes := func(string, int) bool { return true }
	opts := DefaultGptHistoryOptions()
	opts.MinCollapseTokens = 10_000_000
	plan, err := PlanGptCollapse(openAIPlainTurns(40, 1000), 0, yes, opts)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Reason != "below_min_tokens" {
		t.Fatalf("reason=%q", plan.Reason)
	}

	opts = DefaultGptHistoryOptions()
	opts.MinCollapseTokens = 0
	plan, err = PlanGptCollapse(openAIPlainTurns(40, 1000), 0, yes, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Images) == 0 {
		t.Fatalf("expected collapse with zero token floor")
	}

	base := openAIPlainTurns(90, 1000)
	opts = DefaultGptHistoryOptions()
	opts.SectionTokens = 1500
	a, err := PlanGptCollapse(base[:40], 0, yes, opts)
	if err != nil {
		t.Fatal(err)
	}
	b, err := PlanGptCollapse(base[:80], 0, yes, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Images) == 0 || b.EndExclusive <= a.EndExclusive {
		t.Fatalf("expected larger collapse to advance")
	}
	for i := range a.Images {
		if !bytes.Equal(a.Images[i].PNG, b.Images[i].PNG) {
			t.Fatalf("image %d not byte-stable across growth", i)
		}
	}
}

func TestPlanGptCollapsePinsLatestUserRequest(t *testing.T) {
	yes := func(string, int) bool { return true }
	turns := openAITurnsWithUser(40, map[int]bool{0: true})
	plan, err := PlanGptCollapse(turns, 0, yes, GptHistoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.PinText == nil || !strings.Contains(*plan.PinText, "USER REQUEST 0") {
		t.Fatalf("front user request should be pinned: %+v", plan)
	}
	if len(plan.Images) != 0 || len(plan.ImagesAfter) == 0 {
		t.Fatalf("autonomous pin should image work after request")
	}
	if strings.Contains(plan.Text, "USER REQUEST 0") {
		t.Fatalf("pinned request should not be in imaged baseline")
	}

	opts := DefaultGptHistoryOptions()
	opts.CollapseChunk = 0
	opts.SectionTokens = 100
	opts.MaxImages = 100
	turns = openAITurnsWithUser(40, map[int]bool{0: true, 20: true})
	plan, err = PlanGptCollapse(turns, 0, yes, opts)
	if err != nil {
		t.Fatal(err)
	}
	if plan.PinText == nil || !strings.Contains(*plan.PinText, "USER REQUEST 20") {
		t.Fatalf("latest in-range user should be pinned")
	}
	if len(plan.Images) == 0 || len(plan.ImagesAfter) == 0 {
		t.Fatalf("history should image both sides of pinned request")
	}

	turns = openAITurnsWithUser(40, map[int]bool{0: true, 20: true, 37: true})
	plan, err = PlanGptCollapse(turns, 0, yes, opts)
	if err != nil {
		t.Fatal(err)
	}
	if plan.PinText != nil {
		t.Fatalf("latest user in kept tail should remain native, not pinned")
	}
	if !strings.Contains(plan.Text, "USER REQUEST 20") {
		t.Fatalf("older in-range user should stay imaged")
	}
}

func TestTransformOpenAIHistoryCollapseResponsesAndChat(t *testing.T) {
	opts := DefaultTransformOptions("gpt-5.6")
	opts.MinCompressChars = 1
	opts.CharsPerToken = 1
	opts.CollapseHistory = true

	responsesBody := mustMarshalTest(t, map[string]any{
		"model":        "gpt-5.6",
		"instructions": strings.Repeat("You are a coding agent with detailed instructions. ", 80),
		"input":        buildOpenAIResponsesInput(20),
	})
	out, info, err := TransformOpenAI(responsesBody, opts)
	if err != nil {
		t.Fatalf("responses transform: %v", err)
	}
	if info.HistoryReason != "collapsed" || info.CollapsedImages == 0 || info.CollapsedTurns < 10 {
		t.Fatalf("expected responses history collapse, info=%+v", info)
	}
	var responses map[string]any
	unmarshalTest(t, out, &responses)
	serialized := string(mustMarshalTest(t, responses["input"]))
	if !strings.Contains(serialized, "attribute every turn strictly by its tag") {
		t.Fatalf("history synthetic intro missing")
	}
	if strings.Contains(serialized, openAIOpeningMarker+" "+openAIOpeningMarker) {
		t.Fatalf("opening prompt body should be imaged")
	}
	if !strings.Contains(serialized, openAILiveMarker) {
		t.Fatalf("live prompt should stay text")
	}

	chatBody := mustMarshalTest(t, map[string]any{
		"model":    "gpt-5.6",
		"messages": buildOpenAIChatMessages(20),
	})
	out, info, err = TransformOpenAI(chatBody, opts)
	if err != nil {
		t.Fatalf("chat transform: %v", err)
	}
	if info.HistoryReason != "collapsed" || info.CollapsedImages == 0 {
		t.Fatalf("expected chat history collapse, info=%+v", info)
	}
	var chat map[string]any
	unmarshalTest(t, out, &chat)
	serialized = string(mustMarshalTest(t, chat["messages"]))
	if !strings.Contains(serialized, "attribute every turn strictly by its tag") {
		t.Fatalf("chat history synthetic intro missing")
	}
	if !strings.Contains(serialized, openAILiveMarker) {
		t.Fatalf("chat live prompt should stay text")
	}
}

func openAIPlainTurns(n, chars int) []GptHistoryTurn {
	out := make([]GptHistoryTurn, n)
	for i := 0; i < n; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		out[i] = GptHistoryTurn{Text: "--- " + role + " ---\n" + strings.Repeat("x", chars)}
	}
	return out
}

func openAITurnsWithUser(n int, userIndices map[int]bool) []GptHistoryTurn {
	out := make([]GptHistoryTurn, n)
	for i := 0; i < n; i++ {
		isUser := userIndices[i]
		role := "assistant"
		body := "assistant work " + strconvItoa(i) + " " + strings.Repeat("alpha beta gamma delta epsilon ", 60)
		if isUser {
			role = "user"
			body = "USER REQUEST " + strconvItoa(i) + " " + strings.Repeat("alpha beta gamma delta epsilon ", 60)
		}
		turn := GptHistoryTurn{Text: "<" + role + " t=\"" + strconvItoa(i) + "\">\n" + body + "\n</" + role + ">"}
		if isUser {
			text := body
			turn.UserText = &text
		}
		out[i] = turn
	}
	return out
}

func buildOpenAIResponsesInput(turns int) []any {
	items := []any{map[string]any{"role": "user", "content": strings.Repeat(openAIOpeningMarker+" ", 40)}}
	for i := 0; i < turns; i++ {
		id := "call_" + strconvItoa(i)
		items = append(items,
			map[string]any{"role": "assistant", "content": strings.Repeat("Working on step "+strconvItoa(i)+". ", 30)},
			map[string]any{"type": "function_call", "call_id": id, "name": "read", "arguments": `{"path":"f` + strconvItoa(i) + `"}`},
			map[string]any{"type": "function_call_output", "call_id": id, "output": strings.Repeat("result "+strconvItoa(i)+" ", 50)},
		)
		content := strings.Repeat("Continue with "+strconvItoa(i)+". ", 20)
		if i == turns-1 {
			content = strings.Repeat(openAILiveMarker+" ", 20)
		}
		items = append(items, map[string]any{"role": "user", "content": content})
	}
	return items
}

func buildOpenAIChatMessages(turns int) []any {
	msgs := []any{
		map[string]any{"role": "system", "content": strings.Repeat("You are a coding agent with detailed instructions. ", 80)},
		map[string]any{"role": "user", "content": strings.Repeat(openAIOpeningMarker+" ", 40)},
	}
	for i := 0; i < turns; i++ {
		id := "call_" + strconvItoa(i)
		msgs = append(msgs,
			map[string]any{
				"role":    "assistant",
				"content": strings.Repeat("Working on step "+strconvItoa(i)+". ", 30),
				"tool_calls": []any{map[string]any{
					"id":       id,
					"type":     "function",
					"function": map[string]any{"name": "read", "arguments": `{"path":"f` + strconvItoa(i) + `"}`},
				}},
			},
			map[string]any{"role": "tool", "tool_call_id": id, "content": strings.Repeat("result "+strconvItoa(i)+" ", 50)},
		)
		content := strings.Repeat("Continue with "+strconvItoa(i)+". ", 20)
		if i == turns-1 {
			content = strings.Repeat(openAILiveMarker+" ", 20)
		}
		msgs = append(msgs, map[string]any{"role": "user", "content": content})
	}
	return msgs
}

func reflectStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func unmarshalJSONTest(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}
