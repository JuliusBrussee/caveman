package providers

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

// CanonicalToolOrderOptimizerID is the opt-in optimizer ID for deterministic
// alphabetical ordering of `tools[]` (`CAVEMAN_CANONICAL_TOOLS=1` /
// `optimizers.canonical-tool-order`). Sorting `tools[]` by stable tool name
// prevents prompt-cache busts when MCP servers reconnect or initialize in
// different orders across turns or resumes.
const CanonicalToolOrderOptimizerID = "canonical-tool-order"

// ExtractToolCatalog returns the exact serialized bytes of a request's top-level
// tool catalog plus a reassembler that splices replacement bytes back into that
// one field. Every byte outside the `tools` value is preserved, so a caller that
// rewrites only the catalog leaves the rest of the request — system prompt,
// messages, breakpoints — byte-identical.
//
// Supported providers carry a top-level JSON array `tools` whose envelopes are
// walked safely by StripToolSchemaAnnotations:
//   - anthropic / vertex (Claude): `tools[].input_schema`
//   - openai / azure_openai / openai_compatible: `tools[].function.parameters`
//     (Chat Completions) and `tools[].parameters` (Responses)
//   - gemini / vertex (Gemini): `tools[].functionDeclarations[].parameters` /
//     `parametersJsonSchema`
//
// Accounting endpoints (count_tokens, countTokens, count-tokens, input_tokens)
// and embeddings are excluded so measurement requests stay byte-identical.
// Bedrock stays excluded because Converse uses `toolConfig.tools` and Bedrock
// skips content transforms.
func ExtractToolCatalog(body []byte, meta RequestMetadata) ([]byte, func([]byte) ([]byte, error), bool) {
	switch meta.Provider {
	case "anthropic", "openai", "gemini", "azure_openai", "openai_compatible", "vertex":
	default:
		return nil, nil, false
	}
	ep := strings.ToLower(meta.Endpoint)
	if strings.Contains(ep, "count_tokens") || strings.Contains(ep, "counttokens") ||
		strings.Contains(ep, "count-tokens") || strings.Contains(ep, "input_tokens") ||
		strings.Contains(ep, "embeddings") {
		return nil, nil, false
	}
	root, ok := jsonsplice.Root(body)
	if !ok {
		return nil, nil, false
	}
	tools, found := jsonsplice.Field(body, root, "tools")
	if !found || tools.Start >= tools.End || body[tools.Start] != '[' {
		return nil, nil, false
	}
	raw := append([]byte(nil), body[tools.Start:tools.End]...)
	reassemble := func(replacement []byte) ([]byte, error) {
		return jsonsplice.ReplaceRaw(body, tools, replacement)
	}
	return raw, reassemble, true
}

// CanonicalizeToolCatalog deterministically sorts a serialized `tools[]` array
// by stable tool name while preserving the exact inner bytes of every tool
// definition. Returns (canonicalBytes, true) only when the ordering actually
// changed; returns (nil, false) when already sorted or when the shape cannot be
// sorted safely.
//
// Anthropic cache_control safety: if ONLY the last tool element carried a
// top-level `cache_control` marker (standard Claude Code / Anthropic breakpoint
// placement), that exact `cache_control` value is migrated to the new last
// element after sorting so the cached tool prefix covers the entire catalog. If
// multiple tool elements carry `cache_control`, sorting bails out (returns false)
// rather than moving custom mid-catalog breakpoints.
func CanonicalizeToolCatalog(raw []byte, meta RequestMetadata) ([]byte, bool) {
	if len(raw) < 2 || raw[0] != '[' {
		return nil, false
	}
	arrSpan := jsonsplice.Span{Start: 0, End: len(raw)}
	elems, ok := jsonsplice.Elements(raw, arrSpan)
	if !ok || len(elems) == 0 {
		return nil, false
	}
	// Gemini wraps declarations inside `tools[i].functionDeclarations[]`.
	// Check Vertex publisher path explicitly (never bytes.Contains on descriptions).
	epLower := strings.ToLower(meta.Endpoint)
	if meta.Provider == "gemini" || (meta.Provider == "vertex" && strings.Contains(epLower, "/publishers/google/")) {
		return canonicalizeGeminiToolCatalog(raw, elems)
	}
	if len(elems) < 2 {
		return nil, false
	}

	type toolEntry struct {
		key      string
		origIdx  int
		deferred bool
		rawBytes []byte
	}

	// Detect Anthropic trailing cache_control marker on the last tool or last
	// non-deferred tool only. Anthropic rejects cache_control on a tool with
	// defer_loading: true, so we track deferred status and bail out if multiple
	// custom mid-catalog markers exist.
	var trailingCacheControl []byte
	cacheControlCount := 0
	cacheControlIdx := -1
	lastNonDeferredOrig := -1
	nestedChanged := false
	entries := make([]toolEntry, len(elems))
	for i, el := range elems {
		elBytes := append([]byte(nil), raw[el.Start:el.End]...)
		// Recursively sort nested `tools[]` inside OpenAI Responses `type: "namespace"` objects.
		if sortedNS, nsChanged := canonicalizeNamespaceTools(elBytes); nsChanged {
			elBytes = sortedNS
			nestedChanged = true
		}
		key := toolElementSortKey(elBytes)
		if key == "" {
			return nil, false
		}
		deferred := isDeferredToolObject(elBytes)
		if !deferred {
			lastNonDeferredOrig = i
		}
		if ccSpan, found := jsonsplice.Field(raw, el, "cache_control"); found {
			cacheControlCount++
			cacheControlIdx = i
			trailingCacheControl = append([]byte(nil), raw[ccSpan.Start:ccSpan.End]...)
		}
		entries[i] = toolEntry{key: key, origIdx: i, deferred: deferred, rawBytes: elBytes}
	}
	if cacheControlCount > 1 {
		return nil, false
	}
	if cacheControlCount == 1 && cacheControlIdx != len(elems)-1 && cacheControlIdx != lastNonDeferredOrig {
		return nil, false
	}

	alreadySorted := true
	for i := 1; i < len(entries); i++ {
		if entries[i-1].key > entries[i].key {
			alreadySorted = false
			break
		}
	}
	// Even when already sorted, normalize inter-element whitespace so Turn 1
	// (sorted with spaces) and Turn 2 (shuffled -> sorted compact) emit identical bytes.
	if alreadySorted && !nestedChanged {
		var compact bytes.Buffer
		compact.Grow(len(raw))
		compact.WriteByte('[')
		for i, e := range entries {
			if i > 0 {
				compact.WriteByte(',')
			}
			compact.Write(e.rawBytes)
		}
		compact.WriteByte(']')
		if bytes.Equal(compact.Bytes(), raw) {
			return nil, false
		}
		if !json.Valid(compact.Bytes()) {
			return nil, false
		}
		return compact.Bytes(), true
	}

	// If a trailing element carried cache_control, strip ONLY that member using
	// byte-exact jsonsplice (never json.Marshal, which reorders keys and escapes
	// HTML characters, which would cause self-inflicted cache busts across turns).
	if cacheControlCount == 1 {
		stripped, ok := removeTopLevelField(entries[cacheControlIdx].rawBytes, "cache_control")
		if !ok {
			return nil, false
		}
		entries[cacheControlIdx].rawBytes = stripped
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].key < entries[j].key
	})

	if cacheControlCount == 1 {
		targetIdx := -1
		for i := len(entries) - 1; i >= 0; i-- {
			if !entries[i].deferred {
				targetIdx = i
				break
			}
		}
		if targetIdx < 0 {
			// Every tool is deferred; Anthropic forbids cache_control on deferred tools.
			return nil, false
		}
		attached, ok := appendTopLevelField(entries[targetIdx].rawBytes, "cache_control", trailingCacheControl)
		if !ok {
			return nil, false
		}
		entries[targetIdx].rawBytes = attached
	}

	var out bytes.Buffer
	out.Grow(len(raw) + 16)
	out.WriteByte('[')
	for i, e := range entries {
		if i > 0 {
			out.WriteByte(',')
		}
		out.Write(e.rawBytes)
	}
	out.WriteByte(']')
	res := out.Bytes()
	if !json.Valid(res) {
		return nil, false
	}
	return res, true
}

func canonicalizeGeminiToolCatalog(raw []byte, elems []jsonsplice.Span) ([]byte, bool) {
	changed := false
	var out bytes.Buffer
	out.Grow(len(raw) + 16)
	out.WriteByte('[')
	for i, el := range elems {
		if i > 0 {
			out.WriteByte(',')
		}
		elBytes := raw[el.Start:el.End]
		elRoot, ok := jsonsplice.Root(elBytes)
		if !ok {
			out.Write(elBytes)
			continue
		}
		declSpan, found := jsonsplice.Field(elBytes, elRoot, "functionDeclarations")
		if !found || declSpan.Start >= declSpan.End || elBytes[declSpan.Start] != '[' {
			out.Write(elBytes)
			continue
		}
		decls, ok := jsonsplice.Elements(elBytes, declSpan)
		if !ok || len(decls) < 2 {
			out.Write(elBytes)
			continue
		}
		type declEntry struct {
			name string
			raw  []byte
		}
		entries := make([]declEntry, len(decls))
		valid := true
		for j, d := range decls {
			name, ok := jsonsplice.StringField(elBytes, d, "name")
			if !ok || name == "" {
				valid = false
				break
			}
			entries[j] = declEntry{name: name, raw: elBytes[d.Start:d.End]}
		}
		if !valid {
			out.Write(elBytes)
			continue
		}
		sorted := true
		for j := 1; j < len(entries); j++ {
			if entries[j-1].name > entries[j].name {
				sorted = false
				break
			}
		}
		if sorted {
			out.Write(elBytes)
			continue
		}
		sort.SliceStable(entries, func(a, b int) bool {
			return entries[a].name < entries[b].name
		})
		var arr bytes.Buffer
		arr.WriteByte('[')
		for j, e := range entries {
			if j > 0 {
				arr.WriteByte(',')
			}
			arr.Write(e.raw)
		}
		arr.WriteByte(']')
		spliced, err := jsonsplice.ReplaceRaw(elBytes, declSpan, arr.Bytes())
		if err != nil || !json.Valid(spliced) {
			out.Write(elBytes)
			continue
		}
		changed = true
		out.Write(spliced)
	}
	out.WriteByte(']')
	if !changed {
		return nil, false
	}
	res := out.Bytes()
	if !json.Valid(res) {
		return nil, false
	}
	return res, true
}

// ToolCatalogNames extracts tool names from a serialized `tools[]` array or
// Bedrock `toolConfig` object in wire order (used by prefix_monitor to
// distinguish reordered vs changed tools across Anthropic, OpenAI, Gemini, and Bedrock).
func ToolCatalogNames(raw []byte) []string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 {
		return nil
	}
	// Bedrock Converse `toolConfig`: {"tools":[{"toolSpec":{"name":...}}]}
	if trimmed[0] == '{' {
		root, ok := jsonsplice.Root(trimmed)
		if !ok {
			return nil
		}
		toolsSpan, found := jsonsplice.Field(trimmed, root, "tools")
		if !found || toolsSpan.Start >= toolsSpan.End || trimmed[toolsSpan.Start] != '[' {
			return nil
		}
		trimmed = trimmed[toolsSpan.Start:toolsSpan.End]
	}
	if trimmed[0] != '[' {
		return nil
	}
	elems, ok := jsonsplice.Elements(trimmed, jsonsplice.Span{Start: 0, End: len(trimmed)})
	if !ok || len(elems) == 0 {
		return nil
	}
	names := make([]string, 0, len(elems))
	for _, el := range elems {
		elBytes := trimmed[el.Start:el.End]
		elRoot, rok := jsonsplice.Root(elBytes)
		if rok {
			// Gemini `functionDeclarations[]`
			if declSpan, found := jsonsplice.Field(elBytes, elRoot, "functionDeclarations"); found && declSpan.Start < declSpan.End && elBytes[declSpan.Start] == '[' {
				if decls, dok := jsonsplice.Elements(elBytes, declSpan); dok {
					for _, d := range decls {
						if n, nok := jsonsplice.StringField(elBytes, d, "name"); nok && n != "" {
							names = append(names, "g:"+n)
						}
					}
					continue
				}
			}
			// Bedrock Converse `toolSpec.name`
			if specSpan, found := jsonsplice.Field(elBytes, elRoot, "toolSpec"); found {
				if n, nok := jsonsplice.StringField(elBytes, specSpan, "name"); nok && n != "" {
					names = append(names, "b:"+n)
					continue
				}
			}
		}
		if k := toolElementSortKey(elBytes); k != "" {
			names = append(names, k)
		}
	}
	return names
}

func canonicalizeNamespaceTools(elBytes []byte) ([]byte, bool) {
	root, ok := jsonsplice.Root(elBytes)
	if !ok {
		return nil, false
	}
	typ, _ := jsonsplice.StringField(elBytes, root, "type")
	if typ != "namespace" {
		return nil, false
	}
	nestedSpan, found := jsonsplice.Field(elBytes, root, "tools")
	if !found || nestedSpan.Start >= nestedSpan.End || elBytes[nestedSpan.Start] != '[' {
		return nil, false
	}
	sortedNested, changed := CanonicalizeToolCatalog(elBytes[nestedSpan.Start:nestedSpan.End], RequestMetadata{Provider: "openai"})
	if !changed {
		return nil, false
	}
	out, err := jsonsplice.ReplaceRaw(elBytes, nestedSpan, sortedNested)
	if err != nil || !json.Valid(out) {
		return nil, false
	}
	return out, true
}

func toolElementSortKey(elBytes []byte) string {
	root, ok := jsonsplice.Root(elBytes)
	if !ok {
		return ""
	}
	typ, _ := jsonsplice.StringField(elBytes, root, "type")
	// Keep Anthropic / OpenAI tool search tools pinned first deterministically.
	if typ == "tool_search" || strings.HasPrefix(typ, "tool_search_") {
		return "\x00:" + typ
	}
	if name, ok := jsonsplice.StringField(elBytes, root, "name"); ok && name != "" {
		if strings.HasPrefix(name, "tool_search_tool") {
			return "\x00:" + name
		}
		return "n:" + name
	}
	// OpenAI Responses remote MCP tools carry `server_label` instead of `name`.
	if typ == "mcp" {
		if label, ok := jsonsplice.StringField(elBytes, root, "server_label"); ok && label != "" {
			return "mcp:" + label
		}
	}
	if fnSpan, found := jsonsplice.Field(elBytes, root, "function"); found {
		if name, ok := jsonsplice.StringField(elBytes, fnSpan, "name"); ok && name != "" {
			return "f:" + name
		}
	}
	if typ != "" {
		return "t:" + typ
	}
	return ""
}

func isDeferredToolObject(objBytes []byte) bool {
	root, ok := jsonsplice.Root(objBytes)
	if !ok {
		return false
	}
	defSpan, found := jsonsplice.Field(objBytes, root, "defer_loading")
	if !found {
		return false
	}
	return bytes.Equal(bytes.TrimSpace(objBytes[defSpan.Start:defSpan.End]), []byte("true"))
}

func removeTopLevelField(objBytes []byte, fieldName string) ([]byte, bool) {
	root, ok := jsonsplice.Root(objBytes)
	if !ok {
		return nil, false
	}
	return jsonsplice.DeleteObjectField(objBytes, root, fieldName)
}

func appendTopLevelField(objBytes []byte, fieldName string, rawVal []byte) ([]byte, bool) {
	trimmed := bytes.TrimSpace(objBytes)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return nil, false
	}
	keyJSON, err := json.Marshal(fieldName)
	if err != nil {
		return nil, false
	}
	inner := bytes.TrimSpace(trimmed[1 : len(trimmed)-1])
	var out bytes.Buffer
	out.Grow(len(trimmed) + len(keyJSON) + len(rawVal) + 4)
	out.Write(trimmed[:len(trimmed)-1])
	if len(inner) > 0 {
		out.WriteByte(',')
	}
	out.Write(keyJSON)
	out.WriteByte(':')
	out.Write(rawVal)
	out.WriteByte('}')
	res := out.Bytes()
	if !json.Valid(res) {
		return nil, false
	}
	return res, true
}
