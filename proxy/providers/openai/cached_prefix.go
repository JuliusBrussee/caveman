package openai

import (
	"encoding/binary"
	"slices"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

// CachedPrefixComponents splits an OpenAI Responses or Chat Completions request
// into the components the provider's automatic prompt cache keys on:
//
//  0. system: top-level `instructions` (Responses) plus any leading
//     `system`/`developer` conversation items (Chat Completions or Responses).
//  1. tools: the raw `tools` JSON array (so prefixMonitor head-drift analysis
//     can inspect the tool-name multiset directly).
//  2..N. each remaining conversation item in `input[]` or `messages[]`.
//
// Non-prefix inference controls (`reasoning`, `reasoning_effort`,
// `max_output_tokens`, `max_tokens`, `max_completion_tokens`, `prompt_cache_key`,
// `stream`, `temperature`, etc.) are deliberately excluded — mirroring
// Anthropic, Bedrock, and Vertex — so turn-aware effort stepping or affinity
// keys never read as false caveman cache busts, while any mutation inside
// `instructions`, `tools`, or conversation items is caught immediately.
//
// Matches the gateway's `sharedComponents(adapter) == 2` invariant for
// `CachedPrefixInspector`.
func CachedPrefixComponents(body []byte) (components [][]byte, cached int, ok bool) {
	root, ok := jsonsplice.Root(body)
	if !ok {
		return nil, 0, false
	}
	var items []jsonsplice.Span
	foundList := false
	for _, field := range []string{"input", "messages"} {
		list, found := jsonsplice.Field(body, root, field)
		if !found || list.Start >= list.End || body[list.Start] != '[' {
			continue
		}
		elems, valid := jsonsplice.Elements(body, list)
		if !valid {
			return nil, 0, false
		}
		items = elems
		foundList = true
		break
	}
	if !foundList {
		return nil, 0, false
	}

	var sysBuf []byte
	if instrSpan, found := jsonsplice.Field(body, root, "instructions"); found {
		sysBuf = append(sysBuf, framePrefixComponent("instructions", body[instrSpan.Start:instrSpan.End])...)
	}
	var extraToolsBuf []byte
	firstConv := 0
	for firstConv < len(items) {
		if isLeadingSystemOrDeveloperItem(body, items[firstConv]) {
			item := items[firstConv]
			sysBuf = append(sysBuf, framePrefixComponent("system_item", body[item.Start:item.End])...)
			firstConv++
			continue
		}
		if isAdditionalToolsItem(body, items[firstConv]) {
			item := items[firstConv]
			extraToolsBuf = append(extraToolsBuf, body[item.Start:item.End]...)
			firstConv++
			continue
		}
		break
	}

	var toolsVal []byte
	if toolsSpan, found := jsonsplice.Field(body, root, "tools"); found {
		toolsVal = body[toolsSpan.Start:toolsSpan.End]
	} else if len(extraToolsBuf) > 0 {
		toolsVal = extraToolsBuf
	}

	components = make([][]byte, 0, len(items))
	components = append(components, framePrefixComponent("system", sysBuf))
	components = append(components, framePrefixComponent("tools", toolsVal))
	for _, item := range items[firstConv:] {
		components = append(components, framePrefixComponent("item", body[item.Start:item.End]))
	}
	return components, len(components), true
}

func isAdditionalToolsItem(body []byte, span jsonsplice.Span) bool {
	if span.Start >= span.End || body[span.Start] != '{' {
		return false
	}
	t, ok := jsonsplice.StringField(body, span, "type")
	return ok && t == "additional_tools"
}

// CachedPrefixComponents is the adapter form of the package function. Token
// count and embedding endpoints do not write prompt-cache entries.
func (a Adapter) CachedPrefixComponents(body []byte, meta providers.RequestMetadata) ([][]byte, int, bool) {
	if isInputTokenCountEndpoint(meta.Endpoint) || strings.HasSuffix(meta.Endpoint, "/embeddings") {
		return nil, 0, false
	}
	return CachedPrefixComponents(body)
}

func isLeadingSystemOrDeveloperItem(body []byte, span jsonsplice.Span) bool {
	if span.Start >= span.End || body[span.Start] != '{' {
		return false
	}
	if typeSpan, found := jsonsplice.Field(body, span, "type"); found {
		t, ok := decodeJSONString(body[typeSpan.Start:typeSpan.End])
		if !ok || (t != "" && t != "message") {
			return false
		}
	}
	roleSpan, found := jsonsplice.Field(body, span, "role")
	if !found {
		return false
	}
	role, ok := decodeJSONString(body[roleSpan.Start:roleSpan.End])
	return ok && (role == "system" || role == "developer")
}

func framePrefixComponent(name string, value []byte) []byte {
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(len(name)))
	binary.BigEndian.PutUint32(hdr[4:], uint32(len(value)))
	return slices.Concat(hdr[:], []byte(name), value)
}
