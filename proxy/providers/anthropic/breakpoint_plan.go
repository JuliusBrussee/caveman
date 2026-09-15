package anthropic

import (
	"encoding/json"
	"sort"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

// BreakpointPlanID labels the cache-breakpoint planner in x-cave-optimization and
// on the telemetry row.
//
// It is deliberately NOT in the gateway's cacheOptimizerIDs set. Anthropic-direct
// breakpoints ARE the provider_causal_cache method, so adding this id there would
// start minting cache savings for planner-placed breakpoints. Minting requires the
// exact provider/method/optimizer tuple discipline, and earning it is a
// deliberate later change with its own review. Until then the planner mints
// nothing: no tokens, no ratio, no dollars.
const BreakpointPlanID = "cache-breakpoint-plan"

// maxCacheBreakpoints is Anthropic's hard cap on cache_control markers per
// request. The planner counts what the caller already placed and never exceeds
// the cap; a request that already carries four is left alone entirely.
const maxCacheBreakpoints = 4

// breakpointLookbackBlocks is Anthropic's documented cache lookback: a cache read
// looks backward at most this many positions from a breakpoint, and it can only
// find entries PRIOR REQUESTS WROTE. See lookbackPlan for why that second half
// makes the lookback a cross-request property, and why the guard arm that
// modeled it inside a single body does not ship.
const breakpointLookbackBlocks = 20

// cacheControlField is the exact bytes every planned breakpoint inserts. One
// spelling, so a planned request is byte-identical on every later turn that
// carries the same content.
const cacheControlField = `"cache_control":{"type":"ephemeral"}`

// PlanCacheBreakpoints places Anthropic prompt-cache breakpoints on the upstream
// request. cache_control is provider metadata the model never sees, so this
// changes no model-visible bytes; on any parse anomaly the body is left unchanged.
//
// Two arms, and they never mix:
//
//   - No cache_control anywhere: the caller manages no caching at all (a custom
//     agent rather than a caching harness). The planner places its own
//     deterministic set — tools tail, system tail, and a frontier breakpoint on
//     the newest message — within the max-4 budget. This arm is payg-only, the
//     same restriction ApplyProviderNativeTransforms keeps, because a
//     subscription/OAuth session's caching is the harness's business.
//
//   - Existing cache_control present: the planner NEVER moves or removes what is
//     already there. The 20-block lookback guard that would add an intermediate
//     breakpoint is DISABLED (see lookbackPlan). The one thing this arm still
//     does is close the composition dead zone: when every existing breakpoint is
//     on the tool catalog and none is on a content block — the exact shape the
//     sibling anthropic-cache-breakpoints optimizer produces when it runs first —
//     the conversation itself is uncached, so the frontier breakpoint is placed as
//     it would have been in the cold arm, bounded by the same max-4 budget.
//
// payg carries the caller's classification; meta is unused today and is present
// so the planner reads the same way for every provider that has one.
func (a Adapter) PlanCacheBreakpoints(body []byte, meta providers.RequestMetadata, payg bool) ([]byte, bool) {
	return planCacheBreakpoints(body, payg)
}

func planCacheBreakpoints(data []byte, payg bool) ([]byte, bool) {
	// Decode once for the existence question: a valid request may spell the key
	// with an escape such as cache_control, and a raw byte search would then
	// read a caller-managed request as unmanaged and plant a competing plan.
	var root map[string]any
	if json.Unmarshal(data, &root) != nil {
		return nil, false
	}
	existing := countJSONKey(root, "cache_control")
	if existing >= maxCacheBreakpoints {
		return nil, false
	}
	object, ok := jsonsplice.Root(data)
	if !ok {
		return nil, false
	}

	var edits []spliceEdit
	if existing == 0 {
		if !payg {
			return nil, false
		}
		edits = coldStartPlan(data, object)
	} else {
		edits = managedPlan(data, object, maxCacheBreakpoints-existing, payg)
	}
	if len(edits) == 0 {
		return nil, false
	}
	out, ok := applySpliceEdits(data, edits)
	if !ok || !json.Valid(out) {
		return nil, false
	}
	return out, true
}

// coldStartPlan is the arm for requests that carry no cache_control at all. It
// places at most three breakpoints — well inside the max-4 budget — in a fixed
// order so the same conversation always produces the same upstream bytes.
func coldStartPlan(data []byte, root jsonsplice.Span) []spliceEdit {
	var edits []spliceEdit
	if edit, ok := toolsTailEdit(data, root); ok {
		edits = append(edits, edit)
	}
	if edit, ok := systemTailEdit(data, root); ok {
		edits = append(edits, edit)
	}
	if edit, ok := frontierEdit(data, root); ok {
		edits = append(edits, edit)
	}
	if len(edits) > maxCacheBreakpoints {
		edits = edits[:maxCacheBreakpoints]
	}
	return edits
}

// toolsTailEdit marks the last tool. Anthropic caches everything up to and
// including the marked block, so one marker on the last tool caches the whole
// tool catalog — the largest stable prefix in an agent request.
func toolsTailEdit(data []byte, root jsonsplice.Span) (spliceEdit, bool) {
	tools, ok := jsonsplice.Field(data, root, "tools")
	if !ok || !isJSONArray(data, tools) {
		return spliceEdit{}, false
	}
	elements, valid := jsonsplice.Elements(data, tools)
	if !valid {
		return spliceEdit{}, false
	}
	for i := len(elements) - 1; i >= 0; i-- {
		if isJSONObject(data, elements[i]) {
			return appendFieldEdit(data, elements[i])
		}
	}
	return spliceEdit{}, false
}

// systemTailEdit marks the tail of the system prompt. A string system prompt is
// rewritten into its equivalent single-text-block array form, which is the only
// shape that can carry a marker.
func systemTailEdit(data []byte, root jsonsplice.Span) (spliceEdit, bool) {
	system, ok := jsonsplice.Field(data, root, "system")
	if !ok || system.Start >= system.End {
		return spliceEdit{}, false
	}
	switch data[system.Start] {
	case '"':
		replacement := make([]byte, 0, system.End-system.Start+72)
		replacement = append(replacement, []byte(`[{"type":"text","text":`)...)
		replacement = append(replacement, data[system.Start:system.End]...)
		replacement = append(replacement, ',')
		replacement = append(replacement, []byte(cacheControlField)...)
		replacement = append(replacement, []byte(`}]`)...)
		return spliceEdit{start: system.Start, end: system.End, bytes: replacement}, true
	case '[':
		elements, valid := jsonsplice.Elements(data, system)
		if !valid {
			return spliceEdit{}, false
		}
		for i := len(elements) - 1; i >= 0; i-- {
			if isJSONObject(data, elements[i]) {
				return appendFieldEdit(data, elements[i])
			}
		}
	}
	return spliceEdit{}, false
}

// frontierEdit marks the last content block of the newest message — the rolling
// tail breakpoint. It bounds every later turn's rebuild to the tail rather than
// the whole conversation.
func frontierEdit(data []byte, root jsonsplice.Span) (spliceEdit, bool) {
	messages, ok := jsonsplice.Field(data, root, "messages")
	if !ok || !isJSONArray(data, messages) {
		return spliceEdit{}, false
	}
	elements, valid := jsonsplice.Elements(data, messages)
	if !valid || len(elements) == 0 {
		return spliceEdit{}, false
	}
	last := elements[len(elements)-1]
	if !isJSONObject(data, last) {
		return spliceEdit{}, false
	}
	content, ok := jsonsplice.Field(data, last, "content")
	if !ok || !isJSONArray(data, content) {
		// A string content block cannot carry a marker without reshaping the
		// message itself, which is model-visible. Leave it alone.
		return spliceEdit{}, false
	}
	blocks, valid := jsonsplice.Elements(data, content)
	if !valid {
		return spliceEdit{}, false
	}
	for i := len(blocks) - 1; i >= 0; i-- {
		if isJSONObject(data, blocks[i]) {
			return appendFieldEdit(data, blocks[i])
		}
	}
	return spliceEdit{}, false
}

// managedPlan is the arm for requests that already carry a breakpoint. The
// lookback guard is disabled, so the only thing it can still contribute is the
// composition dead-zone fall-through described on PlanCacheBreakpoints.
func managedPlan(data []byte, root jsonsplice.Span, budget int, payg bool) []spliceEdit {
	if budget <= 0 {
		return nil
	}
	if edits := lookbackPlan(); len(edits) > 0 {
		return edits
	}
	// The fall-through is a continuation of the cold arm — it places the frontier
	// breakpoint on a conversation nobody is caching — so it carries the cold
	// arm's payg restriction too.
	if !payg {
		return nil
	}
	marked, ok := contentBlocksCarryBreakpoint(data, root)
	if !ok || marked {
		// Either a shape we cannot walk exactly, or the caller IS caching the
		// conversation. Both mean: leave it alone.
		return nil
	}
	edit, ok := frontierEdit(data, root)
	if !ok {
		return nil
	}
	return []spliceEdit{edit}
}

// lookbackPlan is the 20-block lookback guard. It is DISABLED and returns no
// edits, because the version that shipped modeled the lookback as a property of
// one request body, and that is not what Anthropic documents.
//
// The documented semantics (platform.claude.com/docs/en/build-with-claude/prompt-caching):
// a cache read looks backward at most breakpointLookbackBlocks positions from a
// breakpoint, and it can only find entries PRIOR REQUESTS WROTE. So the gap that
// matters is never between two breakpoints inside the current body — it is
// between this request's breakpoint and the index the PREVIOUS request actually
// wrote. A breakpoint placed at a position computed relative to the current
// body's own markers moves forward as the conversation grows, so every turn lands
// it somewhere no earlier request ever wrote: it can never read, and it pays the
// 1.25x cache write every single turn. That is a pure cost, which is the exact
// failure mode the harm tripwire exists to catch.
//
// Follow-up: rebuild it as a CROSS-REQUEST anchor. The session ledger is already
// keyed on x-cave-session, so it can carry the last breakpoint index this session
// actually wrote; the guard then inserts at lastWrittenIndex+19 — an ABSOLUTE
// pinned position — and re-emits that same absolute index on every later turn, so
// the entry the first turn paid to write is the entry every later turn reads.
// Landing that needs three more things: a block view whose indices are stable
// across turns (append-only conversations are, compressed ones need the prefix
// cache), a placeability check on the target block, and the same max-4 budget cap.
func lookbackPlan() []spliceEdit {
	return nil
}

// contentBlocksCarryBreakpoint reports whether any CONTENT block carries a
// cache_control marker — the system blocks (when system is an array) plus every
// message's content blocks, in wire order. Tools are objects rather than content
// blocks and are deliberately excluded: a marker there says the tool catalog is
// cached, not the conversation.
//
// ok is false for any shape it cannot walk exactly, which the caller treats as
// "leave the body unchanged".
func contentBlocksCarryBreakpoint(data []byte, root jsonsplice.Span) (marked bool, ok bool) {
	if system, found := jsonsplice.Field(data, root, "system"); found && isJSONArray(data, system) {
		elements, valid := jsonsplice.Elements(data, system)
		if !valid {
			return false, false
		}
		for _, element := range elements {
			if blockIsMarked(data, element) {
				return true, true
			}
		}
	}

	messages, found := jsonsplice.Field(data, root, "messages")
	if !found || !isJSONArray(data, messages) {
		return false, false
	}
	elements, valid := jsonsplice.Elements(data, messages)
	if !valid {
		return false, false
	}
	for _, message := range elements {
		if !isJSONObject(data, message) {
			return false, false
		}
		content, found := jsonsplice.Field(data, message, "content")
		if !found {
			return false, false
		}
		if !isJSONArray(data, content) {
			// A string content is one block on the wire that cannot hold a marker.
			continue
		}
		blocks, valid := jsonsplice.Elements(data, content)
		if !valid {
			return false, false
		}
		for _, block := range blocks {
			if blockIsMarked(data, block) {
				return true, true
			}
		}
	}
	return false, true
}

func blockIsMarked(data []byte, span jsonsplice.Span) bool {
	if !isJSONObject(data, span) {
		return false
	}
	_, marked := jsonsplice.Field(data, span, "cache_control")
	return marked
}

// spliceEdit is one byte-range replacement. An insertion is the degenerate case
// where start == end.
type spliceEdit struct {
	start int
	end   int
	bytes []byte
}

// appendFieldEdit produces the insertion that adds cache_control immediately
// before an object's closing brace, leaving every other byte — whitespace, key
// order, escapes — exactly as the caller wrote it.
func appendFieldEdit(data []byte, object jsonsplice.Span) (spliceEdit, bool) {
	if !isJSONObject(data, object) {
		return spliceEdit{}, false
	}
	insertAt := object.End - 1
	for insertAt > object.Start+1 && isJSONSpaceByte(data[insertAt-1]) {
		insertAt--
	}
	addition := make([]byte, 0, len(cacheControlField)+1)
	if insertAt > object.Start+1 {
		addition = append(addition, ',')
	}
	addition = append(addition, []byte(cacheControlField)...)
	return spliceEdit{start: insertAt, end: insertAt, bytes: addition}, true
}

// applySpliceEdits rewrites data in one ascending pass. Edits must not overlap;
// an overlap is a planning bug and returns false so the caller forwards the
// original bytes.
func applySpliceEdits(data []byte, edits []spliceEdit) ([]byte, bool) {
	sorted := append([]spliceEdit(nil), edits...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].start < sorted[j].start })
	total := len(data)
	for _, edit := range sorted {
		total += len(edit.bytes)
	}
	out := make([]byte, 0, total)
	last := 0
	for _, edit := range sorted {
		if edit.start < last || edit.end < edit.start || edit.end > len(data) {
			return nil, false
		}
		out = append(out, data[last:edit.start]...)
		out = append(out, edit.bytes...)
		last = edit.end
	}
	return append(out, data[last:]...), true
}

func isJSONObject(data []byte, span jsonsplice.Span) bool {
	return span.Start >= 0 && span.Start < span.End && span.End <= len(data) &&
		data[span.Start] == '{' && data[span.End-1] == '}'
}

func isJSONArray(data []byte, span jsonsplice.Span) bool {
	return span.Start >= 0 && span.Start < span.End && span.End <= len(data) &&
		data[span.Start] == '[' && data[span.End-1] == ']'
}

func isJSONSpaceByte(b byte) bool {
	return b == ' ' || b == '\n' || b == '\r' || b == '\t'
}

// countJSONKey counts how many times a key appears anywhere in a decoded
// document. The planner uses it for the max-4 budget, so it must count every
// occurrence rather than stop at the first.
func countJSONKey(value any, key string) int {
	switch node := value.(type) {
	case map[string]any:
		count := 0
		for k, child := range node {
			if k == key {
				count++
			}
			count += countJSONKey(child, key)
		}
		return count
	case []any:
		count := 0
		for _, child := range node {
			count += countJSONKey(child, key)
		}
		return count
	}
	return 0
}
