package openai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

// OptimizerID is the policy flag and x-cave-optimization label for this optimizer.
const OptimizerID = "openai-prompt-cache-key"

// Adapter embeds the shared Base and overrides ApplyProviderNativeTransforms to
// inject a stable OpenAI prompt_cache_key on the request's cacheable prefix.
type Adapter struct {
	providers.Base
}

// ApplyProviderNativeTransforms injects a top-level prompt_cache_key derived from
// the request's stable prefix (model + tools + leading system/instructions).
//
// OpenAI prompt caching is automatic for prefixes >= 1024 tokens; prompt_cache_key
// is combined with the prefix hash to increase routing affinity — requests that
// share the same prefix and key are more likely to land on the engine that
// already cached that prefix, raising cache-hit rate. It is non-semantic routing
// metadata the model never sees, so it is byte-safe at S1. It is an affinity
// key, not an explicit cache breakpoint: GPT-5.6's implicit breakpoint can still
// write a changing suffix, and those writes carry a provider price premium.
// Whether the key is net-positive therefore depends on later provider-observed
// reuse; applying it alone proves no savings.
//
// Conservative and idempotent, mirroring the Anthropic optimizer: runs only when
// policy enables it (the proxy already blocks transforms in record mode), never
// touches a request that already carries a prompt_cache_key, injects only when a
// stable prefix exists (tools or a system/instructions present), and passes the
// body through unchanged on any parse problem.
func (a Adapter) ApplyProviderNativeTransforms(ctx context.Context, body providers.BodyReader, meta providers.RequestMetadata, policy providers.TransformPolicy) (providers.TransformResult, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return providers.TransformResult{Body: data, OptimizerIDs: []string{}}, nil
	}
	passthrough := providers.TransformResult{Body: data, OptimizerIDs: []string{}}
	// Record mode is an unconditional wire-preservation boundary. The gateway
	// normally skips this adapter in record mode, but keep the adapter safe when
	// called directly (or by a future caller) as well.
	if policy.RuntimeMode == "record" {
		return passthrough, nil
	}

	cacheKeyOn := policy.OptimizerEnabled(OptimizerID)
	// Behavioral: output-brevity needs its flag AND a cleared eval gate.
	brevityOn := policy.OptimizerActive(BrevityOptimizerID)
	// Behavioral: reasoning-effort lowers the thinking budget, which can change
	// the answer, so it too needs its flag AND a cleared eval gate.
	reasoningOn := policy.OptimizerActive(ReasoningEffortOptimizerID)
	// Request envelope only, never touches model-visible content — but it DOES
	// add a client-visible SSE chunk to the response the caller receives (see
	// stream_usage.go), which is an observable effect outside what byte-safe
	// promises either way. Needs its flag AND a cleared eval gate, same as the
	// other optimizers in this list with any observable effect.
	streamUsageOn := policy.OptimizerActive(StreamUsageOptimizerID)
	if !cacheKeyOn && !brevityOn && !reasoningOn && !streamUsageOn {
		return passthrough, nil
	}

	var root map[string]any
	if json.Unmarshal(data, &root) != nil {
		return passthrough, nil
	}

	var ids []string
	if cacheKeyOn && applyPromptCacheKey(root) {
		ids = append(ids, OptimizerID)
	}
	if brevityOn && applyOutputBrevity(root) {
		ids = append(ids, BrevityOptimizerID)
	}
	if reasoningOn && applyReasoningEffort(root, meta.Endpoint) {
		ids = append(ids, ReasoningEffortOptimizerID)
	}
	if streamUsageOn && applyStreamIncludeUsage(root) {
		ids = append(ids, StreamUsageOptimizerID)
	}
	if len(ids) == 0 {
		return passthrough, nil
	}

	// Preferred path: splice the new top-level fields into the caller's ORIGINAL
	// bytes and leave every other byte alone, so the cache prefix stays stable.
	if out, ok := spliceTopLevelFields(data, root, ids, meta.Endpoint); ok {
		return providers.TransformResult{Body: out, OptimizerIDs: ids}, nil
	}
	// Fallback: re-marshal the mutated document. Reached when an applied
	// optimizer changed something a top-level splice cannot express — today only
	// stream_options.include_usage, which is nested. Byte-preservation is lost
	// for that request; the mutation is not.
	out, err := json.Marshal(root)
	if err != nil {
		return passthrough, nil
	}
	return providers.TransformResult{Body: out, OptimizerIDs: ids}, nil
}

// spliceTopLevelFields appends the fields named by ids to data, preserving every
// other byte of the original document.
//
// ok is false when the splice cannot represent what the applyX helpers did, and
// the caller must re-marshal instead. That is NOT a pass-through signal: an id
// this switch does not recognize must never abandon the whole transform, because
// doing so would silently drop prompt_cache_key whenever an unrelated optimizer
// happened to fire in the same request. This OpenAI affinity optimizer has no
// verified-savings method; its cache reads and writes are measured spend only.
func spliceTopLevelFields(data []byte, root map[string]any, ids []string, endpoint string) ([]byte, bool) {
	object, ok := jsonsplice.Root(data)
	if !ok {
		return nil, false
	}
	insertions := make([]jsonsplice.FieldInsertion, 0, len(ids))
	for _, id := range ids {
		var name string
		switch id {
		case OptimizerID:
			name = "prompt_cache_key"
		case BrevityOptimizerID:
			if _, responses := root["max_output_tokens"]; responses {
				name = "max_output_tokens"
			} else {
				name = "max_completion_tokens"
			}
		case ReasoningEffortOptimizerID:
			// Chat Completions carries this as a top-level field. Responses
			// carries it under reasoning.effort, which requires the fallback
			// re-marshal path below.
			if reasoningEndpointKind(endpoint) != reasoningEndpointChat {
				return nil, false
			}
			name = "reasoning_effort"
		case StreamUsageOptimizerID:
			// applyStreamIncludeUsage mutates stream_options.include_usage, a
			// NESTED field. There is no top-level insertion that describes it.
			return nil, false
		default:
			// Unknown id: fall back to the re-marshal, which is correct for any
			// mutation an applyX helper made to root.
			return nil, false
		}
		value, err := json.Marshal(root[name])
		if err != nil {
			return nil, false
		}
		insertions = append(insertions, jsonsplice.FieldInsertion{Name: name, Value: value})
	}
	out, err := jsonsplice.AppendObjectFields(data, object, insertions...)
	if err != nil {
		return nil, false
	}
	return out, true
}

// applyPromptCacheKey injects a stable prompt_cache_key on the request's
// cacheable prefix. Returns true if it changed root. Idempotent: skips a request
// that already carries a prompt_cache_key.
func applyPromptCacheKey(root map[string]any) bool {
	if _, exists := root["prompt_cache_key"]; exists {
		return false
	}
	sig, ok := prefixSignature(root)
	if !ok {
		return false
	}
	sum := sha256.Sum256(sig)
	root["prompt_cache_key"] = hex.EncodeToString(sum[:])[:32]
	return true
}

// prefixSignature builds a canonical, stable byte signature of the request's
// cacheable prefix. It re-marshals the extracted sub-values through encoding/json
// so two requests whose prefixes are logically identical but differ only in
// whitespace or key order produce the SAME signature (and thus the same key),
// while distinct prefix shapes produce distinct keys. Returns false when there is
// no stable prefix to key on (plain messages with no tools/system).
func prefixSignature(root map[string]any) ([]byte, bool) {
	var parts [][]byte
	stable := false

	if model, ok := root["model"].(string); ok && model != "" {
		parts = append(parts, []byte("model="+model))
	}

	// Tools sit before messages and are the most stable, largest reusable prefix.
	if tools, ok := root["tools"].([]any); ok && len(tools) > 0 {
		if b, err := json.Marshal(tools); err == nil {
			parts = append(parts, b)
			stable = true
		}
	}

	// Responses API: a top-level instructions string is the system equivalent.
	if instr, ok := root["instructions"].(string); ok && instr != "" {
		parts = append(parts, []byte(instr))
		stable = true
	}

	// Chat Completions: only the FIRST message can be the stable system prefix.
	if msgs, ok := root["messages"].([]any); ok && len(msgs) > 0 {
		if first, ok := msgs[0].(map[string]any); ok {
			if role, _ := first["role"].(string); role == "system" || role == "developer" {
				if b, err := json.Marshal(first["content"]); err == nil {
					parts = append(parts, b)
					stable = true
				}
			}
		}
	}

	if !stable {
		return nil, false
	}
	return bytes.Join(parts, []byte{0}), true
}
