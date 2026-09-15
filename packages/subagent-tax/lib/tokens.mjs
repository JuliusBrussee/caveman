// Token accounting. Two rungs, never blended:
//  - "est":   chars / ratio. Calibrated against provider-exact Claude Code runs
//             on 2026-08-07 (observed 5.9–6.9 chars/token on full harness
//             prefixes; 6.4 is the midpoint). One machine's calibration — a
//             labeled estimate, not a measurement.
//  - "exact": Anthropic's count_tokens endpoint (a free, separate rate bucket),
//             opt-in via --count-tokens + ANTHROPIC_API_KEY, and only for
//             anthropic-protocol captures (other tokenizers differ).

export const DEFAULT_CHARS_PER_TOKEN = 6.4;
export const CALIBRATION_NOTE =
  "chars/token calibrated 5.9–6.9 against provider-exact Claude Code prefixes (2026-08-07, one machine); 6.4 = midpoint";

export function estimateTokens(chars, ratio = DEFAULT_CHARS_PER_TOKEN) {
  if (!(ratio > 0)) throw new Error(`invalid chars-per-token ratio: ${ratio}`);
  return { tokens: Math.round(chars / ratio), basis: "est", ratio };
}

// Rebuild a count_tokens request from a captured anthropic-messages body,
// passing the captured fields through verbatim so the count is of what the
// harness actually sent.
export function buildCountTokensRequest(capturedBody) {
  const { model, system, tools, messages } = capturedBody;
  if (!model || !Array.isArray(messages)) return null;
  const req = { model, messages };
  if (system !== undefined) req.system = system;
  if (Array.isArray(tools) && tools.length > 0) req.tools = tools;
  return req;
}

export async function countTokensAnthropic(capturedBody, { apiKey, baseUrl = "https://api.anthropic.com", fetchImpl = fetch } = {}) {
  const req = buildCountTokensRequest(capturedBody);
  if (!req) return { error: "capture is not a countable anthropic-messages body" };
  const res = await fetchImpl(`${baseUrl}/v1/messages/count_tokens`, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      "x-api-key": apiKey,
      "anthropic-version": "2023-06-01",
    },
    body: JSON.stringify(req),
  });
  if (!res.ok) {
    const text = await res.text().catch(() => "");
    return { error: `count_tokens HTTP ${res.status}: ${text.slice(0, 200)}` };
  }
  const data = await res.json();
  if (typeof data.input_tokens !== "number") return { error: "count_tokens returned no input_tokens" };
  return { tokens: data.input_tokens, basis: "exact" };
}
