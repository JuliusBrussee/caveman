package vertex

import (
	"context"
	"encoding/binary"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/gemini"
	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

// Shared pure wire-grammar adapters for Vertex AI's two supported publisher
// families: Anthropic Claude (rawPredict / streamRawPredict) and Google Gemini
// (generateContent / streamGenerateContent).
var (
	anthropicGrammar = anthropic.Adapter{}
	geminiGrammar    = gemini.Adapter{}
)

// vertexPublisherGrammar classifies a Vertex request endpoint into its wire
// grammar ("anthropic" or "google"), failing closed ("") for accounting routes
// (countTokens / count-tokens) and any unknown publisher or method.
func vertexPublisherGrammar(endpoint string) string {
	publisher, model, method := parsePredictPath(endpoint)
	if publisher == "" || model == "" || method == "" {
		return ""
	}
	if method == "countTokens" || anthropicCountTokensPath(publisher, model, method) {
		return ""
	}
	switch publisher {
	case "anthropic":
		if method == "rawPredict" || method == "streamRawPredict" {
			return "anthropic"
		}
	case "google":
		if method == "generateContent" || method == "streamGenerateContent" {
			return "google"
		}
	}
	return ""
}

func (a Adapter) ExtractCompressible(body []byte, meta providers.RequestMetadata) ([][]byte, func([][]byte) ([]byte, error), bool) {
	switch vertexPublisherGrammar(meta.Endpoint) {
	case "anthropic":
		return anthropicGrammar.ExtractCompressible(body, meta)
	case "google":
		return geminiGrammar.ExtractCompressible(body, meta)
	default:
		return nil, nil, false
	}
}

func (a Adapter) ExtractStabilizable(body []byte, meta providers.RequestMetadata) ([]providers.RewritableBlock, func([][]byte) ([]byte, error), bool) {
	switch vertexPublisherGrammar(meta.Endpoint) {
	case "anthropic":
		return anthropicGrammar.ExtractStabilizable(body, meta)
	case "google":
		return geminiGrammar.ExtractStabilizable(body, meta)
	default:
		return nil, nil, false
	}
}

func (a Adapter) FrozenPrefixComponents(body []byte, meta providers.RequestMetadata) ([][]byte, bool) {
	if vertexPublisherGrammar(meta.Endpoint) == "anthropic" {
		return anthropicGrammar.FrozenPrefixComponents(body, meta)
	}
	return nil, false
}

// CachedPrefixComponents splits a Vertex request into the components its
// upstream prompt cache keys on. Both Anthropic Claude and Google Gemini
// families produce two shared leading components (system + tools) followed by
// each conversation turn, matching the gateway's sharedComponents(adapter) == 2
// invariant for CachedPrefixInspector.
func (a Adapter) CachedPrefixComponents(body []byte, meta providers.RequestMetadata) ([][]byte, int, bool) {
	switch vertexPublisherGrammar(meta.Endpoint) {
	case "anthropic":
		return anthropicGrammar.CachedPrefixComponents(body, meta)
	case "google":
		return vertexGeminiCachedPrefixComponents(body)
	default:
		return nil, 0, false
	}
}

func (a Adapter) ApplyProviderNativeTransforms(ctx context.Context, body providers.BodyReader, meta providers.RequestMetadata, policy providers.TransformPolicy) (providers.TransformResult, error) {
	if vertexPublisherGrammar(meta.Endpoint) == "anthropic" {
		return anthropicGrammar.ApplyProviderNativeTransforms(ctx, body, meta, policy)
	}
	return a.Base.ApplyProviderNativeTransforms(ctx, body, meta, policy)
}

func (a Adapter) PlanCacheBreakpoints(body []byte, meta providers.RequestMetadata, payg bool) ([]byte, bool) {
	if vertexPublisherGrammar(meta.Endpoint) == "anthropic" {
		return anthropicGrammar.PlanCacheBreakpoints(body, meta, payg)
	}
	return nil, false
}

func vertexGeminiCachedPrefixComponents(body []byte) ([][]byte, int, bool) {
	root, ok := jsonsplice.Root(body)
	if !ok {
		return nil, 0, false
	}
	contentsSpan, exists := jsonsplice.Field(body, root, "contents")
	if !exists || contentsSpan.Start >= contentsSpan.End || body[contentsSpan.Start] != '[' {
		return nil, 0, false
	}
	items, valid := jsonsplice.Elements(body, contentsSpan)
	if !valid {
		return nil, 0, false
	}
	out := make([][]byte, 0, 2+len(items))
	for _, name := range []string{"systemInstruction", "tools"} {
		var value []byte
		if span, found := jsonsplice.Field(body, root, name); found {
			value = body[span.Start:span.End]
		}
		out = append(out, vertexFrameComponent(name, value))
	}
	for _, item := range items {
		out = append(out, vertexFrameComponent("content", body[item.Start:item.End]))
	}
	return out, len(out), true
}

func vertexFrameComponent(name string, value []byte) []byte {
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(len(name)))
	binary.BigEndian.PutUint32(hdr[4:], uint32(len(value)))
	out := make([]byte, 0, 8+len(name)+len(value))
	out = append(out, hdr[:]...)
	out = append(out, name...)
	out = append(out, value...)
	return out
}
