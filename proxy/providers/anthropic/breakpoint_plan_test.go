package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

// The breakpoint planner places Anthropic prompt-cache metadata on the upstream
// request. cache_control is not model-visible, so this is byte-safe under honesty
// rule #2 — but it is still bounded by two hard contracts: never exceed the
// provider's four-breakpoint budget, and never move or remove a breakpoint the
// harness placed.

// planBlocks builds a request whose single user message carries `blocks` content
// blocks, with cache_control on each index named in marked.
func planBlocks(blocks int, marked ...int) string {
	isMarked := map[int]bool{}
	for _, i := range marked {
		isMarked[i] = true
	}
	parts := make([]string, 0, blocks)
	for i := 0; i < blocks; i++ {
		block := `{"type":"text","text":"block ` + itoaTest(i) + `"`
		if isMarked[i] {
			block += `,` + cacheControlField
		}
		parts = append(parts, block+`}`)
	}
	return `{"model":"claude-sonnet-4-6","max_tokens":1024,` +
		`"system":"You are a helpful assistant.",` +
		`"messages":[{"role":"user","content":[` + strings.Join(parts, ",") + `]}]}`
}

func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	var buf []byte
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}

func countBreakpoints(t *testing.T, body []byte) int {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatalf("planner produced invalid JSON: %v", err)
	}
	return countJSONKey(root, "cache_control")
}

// TestBreakpointPlanColdStartIsDeterministic pins the arm for agents that manage
// no caching at all: the planner places its own set (tools tail, system tail,
// frontier), inside the budget, and byte-identically on every call — a plan that
// moved between turns would bust the very cache it created.
func TestBreakpointPlanColdStartIsDeterministic(t *testing.T) {
	body := `{"model":"claude-sonnet-4-6","max_tokens":1024,` +
		`"tools":[{"name":"read","input_schema":{}},{"name":"write","input_schema":{}}],` +
		`"system":[{"type":"text","text":"sys a"},{"type":"text","text":"sys b"}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"one"}]},` +
		`{"role":"user","content":[{"type":"text","text":"two"},{"type":"text","text":"three"}]}]}`

	out, ok := planCacheBreakpoints([]byte(body), true)
	if !ok {
		t.Fatal("planner declined a request with no cache_control at all")
	}
	if got := countBreakpoints(t, out); got != 3 {
		t.Fatalf("placed %d breakpoints, want 3 (tools tail, system tail, frontier)", got)
	}

	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	tools := root["tools"].([]any)
	if _, marked := tools[1].(map[string]any)["cache_control"]; !marked {
		t.Error("the last tool carries no breakpoint")
	}
	if _, marked := tools[0].(map[string]any)["cache_control"]; marked {
		t.Error("only the LAST tool should carry the tools breakpoint")
	}
	system := root["system"].([]any)
	if _, marked := system[1].(map[string]any)["cache_control"]; !marked {
		t.Error("the system tail carries no breakpoint")
	}
	messages := root["messages"].([]any)
	live := messages[1].(map[string]any)["content"].([]any)
	if _, marked := live[1].(map[string]any)["cache_control"]; !marked {
		t.Error("the frontier block (last block of the newest message) carries no breakpoint")
	}
	if _, marked := live[0].(map[string]any)["cache_control"]; marked {
		t.Error("only the LAST block of the newest message is the frontier")
	}

	again, ok := planCacheBreakpoints([]byte(body), true)
	if !ok || string(again) != string(out) {
		t.Fatalf("plan is not deterministic:\n first %s\nsecond %s", out, again)
	}
}

// TestBreakpointPlanColdStartConvertsStringSystem covers the one reshape the cold
// arm performs: a string system prompt has no block that can hold a marker, so it
// becomes its equivalent single-text-block array. The text itself is untouched.
func TestBreakpointPlanColdStartConvertsStringSystem(t *testing.T) {
	body := `{"model":"claude-sonnet-4-6","system":"You are Claude.","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`

	out, ok := planCacheBreakpoints([]byte(body), true)
	if !ok {
		t.Fatal("planner declined")
	}
	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	system, isArray := root["system"].([]any)
	if !isArray || len(system) != 1 {
		t.Fatalf("system was not converted to a one-block array: %v", root["system"])
	}
	block := system[0].(map[string]any)
	if block["text"] != "You are Claude." || block["type"] != "text" {
		t.Fatalf("system text was altered: %v", block)
	}
	if _, marked := block["cache_control"]; !marked {
		t.Fatalf("converted system block carries no breakpoint: %v", block)
	}
}

// TestBreakpointPlanColdStartIsPAYGOnly: a subscription/OAuth session's caching is
// its harness's business. With no cache_control to guard, the planner declines.
func TestBreakpointPlanColdStartIsPAYGOnly(t *testing.T) {
	body := planBlocks(3)
	if out, ok := planCacheBreakpoints([]byte(body), false); ok {
		t.Fatalf("non-payg request with no cache_control was rewritten: %s", out)
	}
}

// TestBreakpointPlanLookbackGuardIsDisabled pins the disablement, so a future
// change has to be deliberate rather than accidental.
//
// The guard that shipped in review computed its insertion position from the
// CURRENT body's markers. Anthropic's lookback only finds entries prior requests
// WROTE, so a position that moves as the conversation grows lands somewhere no
// earlier request ever wrote: it pays the 1.25x write every turn and never reads.
// The cases below are the reviewer's replayed doc example — consecutive turns of
// one growing conversation, each with a rolling tail breakpoint — and every one
// of them must produce no plan at all.
func TestBreakpointPlanLookbackGuardIsDisabled(t *testing.T) {
	for _, tc := range []struct {
		name   string
		blocks int
		mark   int
	}{
		{name: "turn2: 15 blocks, tail breakpoint at 14", blocks: 15, mark: 14},
		{name: "turn3: 35 blocks, tail breakpoint at 34", blocks: 35, mark: 34},
		{name: "gap of exactly the lookback", blocks: 21, mark: 0},
		{name: "gap one past the lookback", blocks: 22, mark: 0},
		{name: "gap far past the lookback", blocks: 61, mark: 0},
		{name: "two marks with a long trailing gap", blocks: 30, mark: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := planBlocks(tc.blocks, tc.mark)
			if out, ok := planCacheBreakpoints([]byte(body), true); ok {
				t.Fatalf("the disabled lookback guard produced a plan: %s", out)
			}
		})
	}
}

// TestBreakpointPlanNeverMovesHarnessBreakpoints is the arm's core promise, and
// with the guard disabled it holds in its strongest form: a request whose
// conversation the caller is already caching goes upstream byte-identically.
func TestBreakpointPlanNeverMovesHarnessBreakpoints(t *testing.T) {
	for _, body := range []string{
		planBlocks(30, 0, 1),
		planBlocks(60, 0, 30),
		`{"model":"claude-sonnet-4-6",` +
			`"tools":[{"name":"read","input_schema":{},` + cacheControlField + `}],` +
			`"system":[{"type":"text","text":"sys",` + cacheControlField + `}],` +
			`"messages":[{"role":"user","content":[` + blocksWithMarks(40, 0) + `]}]}`,
	} {
		if out, ok := planCacheBreakpoints([]byte(body), true); ok {
			t.Fatalf("a caller-cached conversation was rewritten:\n got %s\nwant %s", out, body)
		}
	}
}

// TestBreakpointPlanRespectsTheFourBudget: a request already carrying the
// provider's maximum is left completely alone.
func TestBreakpointPlanRespectsTheFourBudget(t *testing.T) {
	body := `{"model":"claude-sonnet-4-6",` +
		`"tools":[{"name":"read","input_schema":{},` + cacheControlField + `}],` +
		`"system":[{"type":"text","text":"sys",` + cacheControlField + `}],` +
		`"messages":[{"role":"user","content":[` + blocksWithMarks(60, 0, 1) + `]}]}`

	if got := countBreakpoints(t, []byte(body)); got != 4 {
		t.Fatalf("fixture carries %d breakpoints, want 4", got)
	}
	if out, ok := planCacheBreakpoints([]byte(body), true); ok {
		t.Fatalf("planner exceeded the four-breakpoint budget: %s", out)
	}
}

// TestBreakpointPlanClosesTheCompositionDeadZone is F5: when the sibling
// anthropic-cache-breakpoints optimizer has already put a breakpoint on the tool
// catalog, the conversation itself is still uncached. Returning nil there would
// leave the request strictly worse off than if the planner had run alone, so the
// frontier breakpoint is placed — without displacing the tools one.
func TestBreakpointPlanClosesTheCompositionDeadZone(t *testing.T) {
	body := `{"model":"claude-sonnet-4-6",` +
		`"tools":[{"name":"read","input_schema":{},` + cacheControlField + `}],` +
		`"system":"You are an agent.",` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"one"},{"type":"text","text":"two"}]}]}`

	out, ok := planCacheBreakpoints([]byte(body), true)
	if !ok {
		t.Fatal("the composition dead zone was not closed: a tools-only breakpoint left the conversation uncached")
	}
	if got := countBreakpoints(t, out); got != 2 {
		t.Fatalf("breakpoints = %d, want 2 (the tools one plus the frontier)", got)
	}
	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, marked := root["tools"].([]any)[0].(map[string]any)["cache_control"]; !marked {
		t.Fatal("the existing tools breakpoint was displaced")
	}
	blocks := root["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if _, marked := blocks[1].(map[string]any)["cache_control"]; !marked {
		t.Fatalf("the frontier breakpoint was not placed: %s", out)
	}
	if _, marked := blocks[0].(map[string]any)["cache_control"]; marked {
		t.Fatal("only the last block of the newest message is the frontier")
	}
	// Nothing but the one insertion: the tools breakpoint keeps its exact bytes.
	marker := `,` + cacheControlField
	if got, want := strings.ReplaceAll(string(out), marker, ""), strings.ReplaceAll(body, marker, ""); got != want {
		t.Fatalf("the fall-through changed bytes outside its insertion:\n got %s\nwant %s", got, want)
	}

	// Same shape, but non-payg: the fall-through carries the cold arm's payg
	// restriction, because it IS the cold arm's placement.
	if out, ok := planCacheBreakpoints([]byte(body), false); ok {
		t.Fatalf("non-payg request took the fall-through: %s", out)
	}

	// One marked content block anywhere means the caller IS caching the
	// conversation, and the fall-through must not fire.
	managed := `{"model":"claude-sonnet-4-6",` +
		`"tools":[{"name":"read","input_schema":{},` + cacheControlField + `}],` +
		`"messages":[{"role":"user","content":[` + blocksWithMarks(3, 0) + `]}]}`
	if out, ok := planCacheBreakpoints([]byte(managed), true); ok {
		t.Fatalf("the fall-through fired on a caller-cached conversation: %s", out)
	}
}

// TestBreakpointPlanLeavesShapesItCannotWalk covers the byte-safe rule: anything
// the planner cannot parse exactly forwards unchanged.
func TestBreakpointPlanLeavesShapesItCannotWalk(t *testing.T) {
	for name, body := range map[string]string{
		"not json":          `{"model":`,
		"string content":    `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"plain string"}]}`,
		"no messages":       `{"model":"claude-sonnet-4-6"}`,
		"marked but no gap": planBlocks(5, 0),
		"marked string bodies": `{"model":"claude-sonnet-4-6","system":[{"type":"text","text":"s",` + cacheControlField +
			`}],"messages":[{"role":"user","content":"plain"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if out, ok := planCacheBreakpoints([]byte(body), true); ok {
				t.Fatalf("planner rewrote a body it should have left alone: %s", out)
			}
		})
	}
}

// TestBreakpointPlanSystemBlocksCountAsContent: a marker on the system tail says
// the caller IS caching content blocks, so the dead-zone fall-through must not
// fire. Only a TOOLS-only marker leaves the conversation genuinely uncached.
func TestBreakpointPlanSystemBlocksCountAsContent(t *testing.T) {
	body := `{"model":"claude-sonnet-4-6",` +
		`"system":[{"type":"text","text":"sys",` + cacheControlField + `}],` +
		`"messages":[{"role":"user","content":[` + blocksWithMarks(21) + `]}]}`

	if out, ok := planCacheBreakpoints([]byte(body), true); ok {
		t.Fatalf("a system-tail breakpoint was treated as an uncached conversation: %s", out)
	}
}

// TestBreakpointPlanAdapterSeam pins the capability method the gateway calls.
func TestBreakpointPlanAdapterSeam(t *testing.T) {
	adapter := New("http://upstream").(Adapter)
	out, ok := adapter.PlanCacheBreakpoints([]byte(planBlocks(3)), providers.RequestMetadata{Provider: "anthropic"}, true)
	if !ok {
		t.Fatal("adapter seam declined a cold-start payg request")
	}
	if got := countBreakpoints(t, out); got == 0 {
		t.Fatal("adapter seam placed no breakpoint")
	}
}

func blocksWithMarks(count int, marked ...int) string {
	isMarked := map[int]bool{}
	for _, i := range marked {
		isMarked[i] = true
	}
	parts := make([]string, 0, count)
	for i := 0; i < count; i++ {
		block := `{"type":"text","text":"b` + itoaTest(i) + `"`
		if isMarked[i] {
			block += `,` + cacheControlField
		}
		parts = append(parts, block+`}`)
	}
	return strings.Join(parts, ",")
}
