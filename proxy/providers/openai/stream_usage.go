package openai

// StreamUsageOptimizerID is the policy flag / x-cave-optimization label for the
// stream-usage optimizer.
const StreamUsageOptimizerID = "openai-stream-usage"

// applyStreamIncludeUsage asks the provider for a final usage chunk on a
// streamed OpenAI-shaped call by setting stream_options.include_usage = true.
// Returns true if it changed root.
//
// Why: streamed OpenAI-shaped calls return no usage block unless the request
// carries this flag, so without it the gateway has no provider-reported token
// counts for streamed traffic and must derive them instead — a worse money
// truth. This is envelope metadata the model never sees (it changes what the
// SSE stream ends with, not what the model reads or answers), so it does not
// violate byte-safe as that rule is written today (model-visible bytes only).
// But it DOES add one extra client-visible SSE chunk to the stream the caller
// receives (a final `data: {...,"usage":{...}}` event before `[DONE]`) — a
// change to the RESPONSE bytes the client sees, which sits outside a promise
// byte-safe doesn't currently make either way, not inside it. Every
// OpenAI-compatible client already knows to either consume or ignore an
// extra chunk shaped exactly like OpenAI's own documented include_usage
// behavior, so this is an acceptable, disclosed side effect — but "acceptable
// and disclosed" is a judgment call an operator makes, not something that
// follows from byte-safe alone. That is why this optimizer requires BOTH its
// policy flag AND a cleared eval gate (OptimizerActive, not just
// OptimizerEnabled) — the same bar every other optimizer with an
// observable effect on what the caller receives already has to clear.
//
// Conservative and idempotent: never overrides an explicit client choice.
//   - Only applies to bodies whose top-level shape is Chat Completions, the one
//     OpenAI API that documents stream_options.include_usage. A streamed
//     Responses request is left untouched (see isChatCompletionsShape).
//   - No stream_options at all: adds {"include_usage": true}.
//   - stream_options present without an include_usage key: merges the field in,
//     preserving every other key the caller set.
//   - stream_options.include_usage already set (true OR false): left untouched.
//     false is a deliberate client choice and must never be flipped to true.
//   - stream_options present but not a JSON object: left untouched rather than
//     guessed at.
//   - Only applies when the request is actually streaming ("stream": true);
//     non-streaming requests are untouched.
func applyStreamIncludeUsage(root map[string]any) bool {
	streaming, _ := root["stream"].(bool)
	if !streaming {
		return false
	}
	if !isChatCompletionsShape(root) {
		return false
	}

	existing, present := root["stream_options"]
	if !present {
		root["stream_options"] = map[string]any{"include_usage": true}
		return true
	}

	so, ok := existing.(map[string]any)
	if !ok {
		return false
	}
	if _, has := so["include_usage"]; has {
		return false
	}
	so["include_usage"] = true
	return true
}

// isChatCompletionsShape reports whether root is a Chat Completions body, using
// the same top-level discriminators applyOutputBrevity already uses: Responses
// carries `input` and/or `instructions`, Chat Completions carries `messages`.
//
// This gate exists because stream_options is a Chat-Completions-only parameter.
// The Responses API streams usage in its own terminal event and has no
// stream_options field at all, so injecting one there sends the provider a
// parameter it does not accept — turning an optimizer that is supposed to
// improve the money truth into a 400 on the caller's request. Unrecognized
// shapes fail closed (no injection) rather than being guessed at: the cost of
// not injecting is a request whose usage must be derived, which is exactly the
// state every non-Chat-Completions request is already in.
func isChatCompletionsShape(root map[string]any) bool {
	if _, hasInput := root["input"]; hasInput {
		return false
	}
	if _, hasInstructions := root["instructions"]; hasInstructions {
		return false
	}
	_, hasMessages := root["messages"]
	return hasMessages
}
