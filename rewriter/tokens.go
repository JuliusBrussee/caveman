package rewriter

import "github.com/JuliusBrussee/caveman/engine/tokens"

// countTokens is the same inferred estimate the replay bench prices context
// policies with: `internal/replaybench/cost.go` takes an engine
// `tokens.Counter` and `tools/cavebench-replay/main.go:45` supplies
// `tokens.Default()` (the offline o200k_base BPE codec, with a chars/4 fallback
// when the embedded vocab cannot load). Both θ gates and the acceptance gate
// are measured on it, so a rewrite the bench prices as net-positive is the same
// rewrite this gate accepts. It is an estimate, never a provider count — the
// only measured numbers in Result are InputTokens/OutputTokens, which come from
// the rewriter model's own usage block.
func countTokens(b []byte) int { return tokens.Default().Count(b) }
