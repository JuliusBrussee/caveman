package providers

import (
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

// ExtractToolCatalog returns the exact serialized bytes of a request's top-level
// tool catalog plus a reassembler that splices replacement bytes back into that
// one field. Every byte outside the `tools` value is preserved, so a caller that
// rewrites only the catalog leaves the rest of the request — system prompt,
// messages, breakpoints — byte-identical.
//
// It covers the anthropic-messages wire shape only. openai-chat,
// openai-responses, and gemini-generatecontent carry their tool declarations in
// different envelopes (`tools[].function.parameters`,
// `tools[].functionDeclarations[]`), and guessing at a shape we have not pinned
// would risk splicing the wrong span, so they fail closed with ok=false until
// their own extraction is written and tested.
//
// count_tokens is excluded for the same reason the compression paths exclude it:
// that endpoint exists to measure a body, and rewriting the body it is asked to
// count would make the number answer a different question than the caller asked.
func ExtractToolCatalog(body []byte, meta RequestMetadata) ([]byte, func([]byte) ([]byte, error), bool) {
	if meta.Provider != "anthropic" || strings.Contains(meta.Endpoint, "count_tokens") {
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
