// OpenClaw v2026.8.2 (0965053fe6b9341776df147a6934b7485c60b5ca):
// packages/ai/src/transports/openai-completions-compat.ts
// packages/ai/src/transports/openai-responses-payload-policy.ts
// packages/ai/src/providers/anthropic.ts
// src/agents/provider-attribution.ts, src/config/types.models.ts
// This is a bounded config-overlay gate, not a copy of OpenClaw's private
// plugin metadata registry. Unknown host policy must remain in the host.

type ObjectValue = Record<string, unknown>;
type ModelInput = { provider: string; id: string; api: string; baseUrl: string; compat?: unknown };
type CompatResult = { ok: true; compat?: ObjectValue } | { ok: false; reason: string };

function object(value: unknown): ObjectValue | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as ObjectValue : undefined;
}

function hostname(baseUrl: string): string | undefined {
  try { return new URL(baseUrl).hostname.toLowerCase(); } catch { return undefined; }
}

function nativeOpenAIHost(host: string): boolean {
  return host === "api.openai.com" || host === "chatgpt.com" || [
    ".openai.azure.com", ".cognitiveservices.azure.com", ".services.ai.azure.com", ".api.cognitive.microsoft.com",
  ].some(suffix => host.endsWith(suffix));
}

/** Overrides that change the final transport cannot be copied onto localhost. */
export function openClawRequestCompatibilityIssue(value: unknown): string | undefined {
  if (value === undefined) return undefined;
  const request = object(value);
  if (!request) return "its request transport overrides could not be resolved";
  const auth = object(request.auth);
  if (request.auth !== undefined && (!auth || auth.mode !== "provider-default")) {
    return "its custom request authentication cannot be preserved through routing";
  }
  if (request.proxy !== undefined || request.tls !== undefined) {
    return "its request proxy or TLS settings belong to the original endpoint";
  }
  if (request.allowPrivateNetwork === false) {
    return "its request policy explicitly disallows the local proxy endpoint";
  }
  if (request.headers !== undefined && !object(request.headers)) {
    return "its request headers could not be resolved";
  }
  return undefined;
}

/** Freeze public defaults only when their values do not require private metadata. */
export function preserveOpenClawProviderCompat(model: ModelInput): CompatResult {
  const host = hostname(model.baseUrl);
  if (!host) return { ok: false, reason: "its original endpoint compatibility could not be resolved" };
  const compat = object(model.compat);
  if (model.compat !== undefined && !compat) {
    return { ok: false, reason: "its explicit model compatibility could not be resolved" };
  }

  if (model.api === "openai-completions") {
    // Even a complete public compat object cannot override the private
    // requiresNonEmptyUserOrAssistantMessage field or the native-route store
    // wrapper. Calling the standalone resolver would guess active metadata.
    return { ok: false, reason: "OpenClaw does not expose the resolved OpenAI Chat provider policy needed to preserve its payload defaults" };
  }

  // OpenClaw normalizes configured provider IDs before matching models and
  // applying transport policy. Keep the config key intact, but compare the
  // same identity its runtime sees (including for public provider defaults).
  const provider = model.provider.trim().toLowerCase();
  // Public model compat cannot freeze xAI's native compact-endpoint decision.
  if (model.api === "openai-responses" && (provider === "xai" || provider === "x-ai") && host === "api.x.ai") {
    return { ok: false, reason: "OpenClaw's native xAI compact-endpoint policy cannot be preserved by public compatibility settings" };
  }

  // These headers are generated after model resolution from endpoint metadata.
  // Static config headers do not recover the installed runtime version, and
  // OpenClaw's attribution defaults take precedence over configured values.
  if ((provider === "openai" && nativeOpenAIHost(host)) ||
      (provider === "openrouter" && (host === "openrouter.ai" || host.endsWith(".openrouter.ai"))) ||
      (provider === "xai" && host === "api.x.ai") ||
      host === "integrate.api.nvidia.com" || host === "generativelanguage.googleapis.com") {
    return { ok: false, reason: "OpenClaw generates endpoint-specific request headers that this config overlay cannot preserve" };
  }

  if (model.api === "openai-responses") {
    if (nativeOpenAIHost(host) || model.baseUrl.includes("api.openai.com")) {
      return { ok: false, reason: "OpenClaw's native Responses cache, replay and service-tier policy cannot be preserved by public compatibility settings" };
    }
    // The bundled Responses payload resolver treats all other routes as
    // non-native. xAI alone additionally defaults to an instructions field.
    // Explicit switches prevent the transport host from re-deriving these
    // public defaults after the endpoint changes to localhost.
    return { ok: true, compat: {
      ...compat,
      supportsStore: compat?.supportsStore ?? true,
      supportsPromptCacheKey: compat?.supportsPromptCacheKey ?? false,
      supportsInstructions: compat?.supportsInstructions ?? host === "api.x.ai",
    } };
  }

  if (model.api === "anthropic-messages") {
    if (provider === "anthropic" && host === "api.anthropic.com") {
      return { ok: false, reason: "OpenClaw's native Anthropic fallback beta, service-tier and stream validation policy cannot be preserved by public compatibility settings" };
    }
    const fireworks = provider === "fireworks";
    return { ok: true, compat: {
      ...compat,
      supportsEagerToolInputStreaming: compat?.supportsEagerToolInputStreaming ?? !fireworks,
      supportsLongCacheRetention: compat?.supportsLongCacheRetention ?? !fireworks,
      sendSessionAffinityHeaders: compat?.sendSessionAffinityHeaders ??
        (fireworks || (provider === "cloudflare-ai-gateway" && model.baseUrl.includes("anthropic"))),
    } };
  }

  if (model.api === "google-generative-ai") {
    return { ok: true, ...(compat ? { compat: { ...compat } } : {}) };
  }
  return { ok: false, reason: `OpenClaw's ${model.api} transport compatibility has not been verified` };
}
