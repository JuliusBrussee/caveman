// Package translate renders an LLM request written in one wire grammar for an
// upstream that speaks another, and turns the upstream's answer back into the
// caller's grammar, streamed or not.
//
// These translators were moved from the private caveman-ai/router repository
// (its engine/ package and the daemon's wire fitting) when that daemon was
// retired per Caveman Cloud ADR 0085. Same author; they are now Apache-2.0
// with the rest of this repository.
//
// Three grammars: Anthropic Messages, OpenAI chat completions and OpenAI
// Responses. Seven directions are supported (Supported): each grammar to
// itself (fitted: model id, effort, parameters, thinking hygiene), Messages
// to chat and Messages to Responses (Claude Code on a model Anthropic never
// served, the ChatGPT plan included), Responses to Messages and Responses to
// chat (Codex on a model OpenAI never served). Chat callers reach chat only.
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
//     reasoning item's encrypted_content under a "caveman" envelope.
//
// Everything signed "caveman:" (and every unsigned block) is stripped before a
// body reaches Anthropic's own API (AnthropicNative), and every envelope is
// stripped before a Responses body reaches OpenAI (OpenAINative).
package translate
