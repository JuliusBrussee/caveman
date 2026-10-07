// Package translate renders an LLM request written in one wire grammar for an
// upstream that speaks another, and turns the upstream's answer back into the
// caller's grammar, streamed or not.
//
// Three grammars: Anthropic Messages, OpenAI chat completions and OpenAI
// Responses, all nine directions (Supported): each grammar to itself (fitted:
// model id, effort, parameters, thinking hygiene), Messages to chat and to
// Responses (Claude Code on a model Anthropic never served, the ChatGPT plan
// included), Responses to Messages and to chat (Codex on a model OpenAI never
// served), chat to Messages and to Responses (OpenCode, Aider and other
// OpenAI-compatible clients on Claude or a Responses-only GPT). Documents,
// structured output, parallel-call switches and refusals cross every
// direction; what a target cannot take refuses the request (the pool falls
// back) instead of being dropped.
//
// Request bodies are read and written at the byte level (jsonfast.go): the
// conversation is copied as it came, never decoded and re-encoded, so an
// earlier turn renders the same bytes every time (prefix caches hold) and a
// 100k-token body costs a fraction of a millisecond.
//
// Thinking signatures. Anthropic rejects any thinking block it did not sign,
// and Claude Code answers that rejection by dropping thinking for the rest of
// the session. So the runtime marks every thinking block it hands a caller
// that Anthropic did not mint:
//
//   - a chat upstream's reasoning becomes a thinking block signed
//     "caveman:v1:<route>:<model>", replayed as reasoning_content only to that
//     route and model (Options.Replay);
//   - a Responses upstream's reasoning becomes a thinking block signed
//     "caveman:r1:<route>:<encrypted_content>", replayed as a reasoning item
//     only to that route;
//   - a non-Anthropic Messages host's own signatures come back as
//     "caveman:<route>:<signature>" and are restored for that route only;
//   - Anthropic thinking a Responses caller carries is stashed, signed, in a
//     reasoning item's encrypted_content under a "caveman" envelope;
//   - a chat caller gets the same envelope (signed thinking, or a Responses
//     host's encrypted_content tagged with its route) in reasoning_details,
//     next to the text as reasoning_content.
//
// Everything signed "caveman:" (and every unsigned block) is stripped before a
// body reaches Anthropic's own API (AnthropicNative), every envelope before a
// Responses body reaches OpenAI (OpenAINative) and before a chat body reaches
// any chat API (ChatNative, with the reasoning text it came with).
//
// Answers carry ids of the runtime's own (msg_…, chatcmpl-…, resp_…); the
// host's id stays in Reply.UpstreamID.
package translate
