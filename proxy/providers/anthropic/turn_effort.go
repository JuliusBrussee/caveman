package anthropic

// NOTE: Turn-aware reasoning effort stepping (`providers.TurnEffortOptimizerID`,
// `CAVEMAN_TURN_EFFORT=1`) is intentionally NOT applied inside the stateless
// Anthropic adapter because mutating top-level `output_config.effort`
// mid-conversation invalidates Anthropic's prompt cache.
//
// Instead, the gateway (`proxy/internal/gateway/local_turn_effort.go` +
// `route.go`) applies stateful, append-only per-message effort marks
// (`{"role":"system","content":[],"output_config":{"effort":...}}` with
// `anthropic-beta: mid-conversation-output-config-2026-07-01`) on supported
// Claude 5+ models (Opus 5.5, Sonnet 5.5, Haiku 5.5, Opus 5, Fable 5/5.1,
// Mythos 5/5.1), keeping top-level `output_config.effort` frozen and preserving
// 100% of the prompt cache.
