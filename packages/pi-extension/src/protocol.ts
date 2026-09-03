// Bounded shared types for the Pi ⇄ Caveman native-runtime bridge. Mirrors the
// caps enforced by the CLI (validNativeRuntimeResponse) and the Go runtime so an
// oversized field degrades to "absent" instead of crossing the boundary.

export const MAX_CONTEXT_BYTES = 64 * 1024;
export const MAX_MESSAGE_BYTES = 4 * 1024;
export const MAX_OUTPUT_REPLACEMENT_BYTES = 2 * 1024 * 1024;
export const MAX_RECOVERY_REF_BYTES = 1024;
export const MAX_DECISION_ID_BYTES = 256;
// Hook stdin is capped at 2 MiB on the CLI side; tool output beyond this is not
// truncated (a partial payload could yield a wrong replacement) — it is skipped.
export const MAX_TOOL_OUTPUT_BYTES = 2 * 1024 * 1024;

export type HookEvent =
  | "SessionStart"
  | "UserPromptSubmit"
  | "ModelBefore"
  | "ModelAfter"
  | "Stop"
  | "PreToolUse"
  | "PostToolUse"
  | "PostToolUseFailure"
  | "PreCompact"
  | "PostCompact"
  | "SessionEnd";

export type HookResponse = {
  action?: string;
  context?: string;
  message?: string;
  output_replacement?: string;
  recovery_ref?: string;
  decision_id?: string;
  hookSpecificOutput?: {
    hookEventName?: string;
    additionalContext?: string;
    updatedToolOutput?: string;
  };
};

function fits(value: unknown, max: number): value is string {
  return typeof value === "string" && Buffer.byteLength(value, "utf8") <= max;
}

// sanitizeHookResponse drops any field that is missing, mistyped, or over its
// byte cap. Fail-open: a degenerate response becomes an empty object.
export function sanitizeHookResponse(raw: unknown): HookResponse {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return {};
  const value = raw as Record<string, unknown>;
  const out: HookResponse = {};
  if (fits(value.context, MAX_CONTEXT_BYTES)) out.context = value.context;
  if (fits(value.message, MAX_MESSAGE_BYTES)) out.message = value.message;
  if (fits(value.output_replacement, MAX_OUTPUT_REPLACEMENT_BYTES)) out.output_replacement = value.output_replacement;
  if (fits(value.recovery_ref, MAX_RECOVERY_REF_BYTES)) out.recovery_ref = value.recovery_ref;
  if (fits(value.decision_id, MAX_DECISION_ID_BYTES)) out.decision_id = value.decision_id;
  if (typeof value.action === "string") out.action = value.action;
  const hso = value.hookSpecificOutput;
  if (hso && typeof hso === "object" && !Array.isArray(hso)) {
    const nested = hso as Record<string, unknown>;
    const cleaned: NonNullable<HookResponse["hookSpecificOutput"]> = {};
    if (typeof nested.hookEventName === "string") cleaned.hookEventName = nested.hookEventName;
    if (fits(nested.additionalContext, MAX_CONTEXT_BYTES)) cleaned.additionalContext = nested.additionalContext;
    if (fits(nested.updatedToolOutput, MAX_OUTPUT_REPLACEMENT_BYTES)) cleaned.updatedToolOutput = nested.updatedToolOutput;
    out.hookSpecificOutput = cleaned;
  }
  return out;
}

export function additionalContextOf(response: HookResponse | undefined): string | undefined {
  const context = response?.hookSpecificOutput?.additionalContext ?? response?.context;
  return context ? context : undefined;
}

export function outputReplacementOf(response: HookResponse | undefined): string | undefined {
  const replacement = response?.hookSpecificOutput?.updatedToolOutput ?? response?.output_replacement;
  return replacement ? replacement : undefined;
}

// This route table maps each Pi model API to a path under the local gateway.
// An API that is not in this table is unsupported, for example azure, bedrock,
// vertex, mistral, and codex-responses. The extension never routes such an API,
// because it does not guess a wire protocol.
export const ROUTES_BY_API: Readonly<Record<string, string>> = {
  "anthropic-messages": "/w/pi",
  "openai-completions": "/w/pi/openai/v1",
  "openai-responses": "/w/pi/openai/v1",
  "google-generative-ai": "/w/pi/v1beta",
};

// OpenCode Go uses the OpenAI and Anthropic wire protocols, but its upstream is
// not api.openai.com. This provider mount gives the proxy the correct upstream.
const ROUTES_BY_PROVIDER_API: Readonly<Record<string, Readonly<Record<string, string>>>> = {
  "opencode-go": {
    "anthropic-messages": "/w/pi/compat/opencode-go",
    "openai-completions": "/w/pi/compat/opencode-go/v1",
    "openai-responses": "/w/pi/compat/opencode-go/v1",
  },
};

// The proxy sends each route to one fixed upstream host. The extension routes a
// provider only when the original base URL of the provider points at that host.
// A provider with the name "openai" can point at a local relay (litellm, ollama)
// or at Azure. Such a provider must keep its endpoint. If the pi catalog moves
// opencode-go to another host, the stale built-in mount in the proxy must not
// get the request. A provider that is not in this table never routes.
export const UPSTREAM_HOSTS_BY_PROVIDER: Readonly<Record<string, string>> = {
  anthropic: "api.anthropic.com",
  openai: "api.openai.com",
  google: "generativelanguage.googleapis.com",
  "opencode-go": "opencode.ai",
};

export function upstreamHostFor(provider: string | undefined): string | undefined {
  return provider ? UPSTREAM_HOSTS_BY_PROVIDER[provider] : undefined;
}

// hostOf returns the lowercase host name of a URL, or undefined for a value
// that is not a URL. A caller compares the result with the table above.
export function hostOf(url: string | undefined): string | undefined {
  if (!url) return undefined;
  try {
    return new URL(url).hostname;
  } catch {
    return undefined;
  }
}

// routeForApi gives the gateway route for one model, or undefined for a model
// that must stay direct. With a provider, the provider must be in
// UPSTREAM_HOSTS_BY_PROVIDER. With an original base URL, the host of that URL
// must be the upstream host of the provider. Without a provider, the function
// keeps the API-only behavior for existing callers.
export function routeForApi(gateway: string, api: string | undefined, provider?: string, originalBaseUrl?: string): string | undefined {
  if (provider) {
    const expected = UPSTREAM_HOSTS_BY_PROVIDER[provider];
    if (!expected) return undefined;
    if (originalBaseUrl !== undefined && hostOf(originalBaseUrl) !== expected) return undefined;
  }
  const table = (provider ? ROUTES_BY_PROVIDER_API[provider] : undefined) ?? ROUTES_BY_API;
  const path = api ? table[api] : undefined;
  return path ? joinUrl(gateway, path) : undefined;
}

export function joinUrl(base: string, path: string): string {
  return `${base.replace(/\/+$/, "")}${path}`;
}

// Only a loopback gateway may be routed: managed gateways need separate auth
// proof that v1 does not carry.
export function isLoopbackUrl(url: string): boolean {
  try {
    const host = new URL(url).hostname;
    return host === "127.0.0.1" || host === "::1" || host === "[::1]" || host === "localhost";
  } catch {
    return false;
  }
}

export function boundedString(value: string, max: number): string {
  if (Buffer.byteLength(value, "utf8") <= max) return value;
  return Buffer.from(value, "utf8").subarray(0, max).toString("utf8").replace(/�+$/, "");
}
