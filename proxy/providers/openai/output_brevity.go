package openai

// BrevityOptimizerID is the policy flag / x-cave-optimization label for the
// output-brevity optimizer.
const BrevityOptimizerID = "output-brevity"

// brevityMaxOutputTokens is the provider-native output cap this optimizer applies
// when the caller has not set one. It bounds runaway generations without
// truncating typical answers; because it CAN change output it is opt-in and
// eval-gated (see ApplyProviderNativeTransforms / docs/optimizations.md).
const brevityMaxOutputTokens = 512

// applyOutputBrevity adds a provider-native output cap when the caller hasn't
// set one. Responses API uses max_output_tokens; Chat Completions uses
// max_completion_tokens. Returns true if it changed root. It respects any cap the
// caller already set (never raises or lowers it).
//
// Unlike the cache optimizers this is NOT byte-safe — it can shorten model
// output — which is exactly why it runs only behind both a policy flag and a
// cleared eval gate.
func applyOutputBrevity(root map[string]any) bool {
	// Responses API: top-level input or instructions present.
	_, hasInput := root["input"]
	_, hasInstr := root["instructions"]
	if hasInput || hasInstr {
		if _, set := root["max_output_tokens"]; set {
			return false
		}
		root["max_output_tokens"] = brevityMaxOutputTokens
		return true
	}
	// Chat Completions: messages present.
	if _, hasMsgs := root["messages"]; hasMsgs {
		if _, set := root["max_completion_tokens"]; set {
			return false
		}
		if _, set := root["max_tokens"]; set {
			return false
		}
		root["max_completion_tokens"] = brevityMaxOutputTokens
		return true
	}
	return false
}
