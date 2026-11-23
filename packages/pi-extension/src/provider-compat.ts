import type { ExtensionContext } from "@earendil-works/pi-coding-agent";
import type { AnthropicMessagesCompat, OpenAICompletionsCompat, OpenAIResponsesCompat } from "@earendil-works/pi-ai";

type SelectedModel = NonNullable<ExtensionContext["model"]>;

// Pi's pi-ai identity layer adds attribution headers from model.provider/baseUrl
// below model.headers and optionsHeaders (dist/utils/identity.js), while
// Models.applyAuth adds OpenCode's x-opencode-session into optionsHeaders below
// resolved auth headers and above model.headers (dist/models.js). Canonical
// provider IDs keep their original branches after routing; for aliases, check
// that explicit headers make the wire result independent of URL and telemetry.
export function unpreservedAttributionHeaders(
  model: SelectedModel, resolvedHeaders: Record<string, string | null> | undefined,
  sessionId: string | undefined, telemetryEnv = process.env.PI_TELEMETRY,
): string[] {
  let host: string | undefined;
  try { host = new URL(model.baseUrl).hostname; } catch { /* endpoint gate reports invalid URLs */ }
  const telemetryDisabled = telemetryEnv !== undefined && !["1", "true", "yes"].includes(telemetryEnv.toLowerCase());
  const missing = new Set<string>();

  const identityHeaders: string[] = [];
  if (!telemetryDisabled) {
    if (model.provider !== "openrouter" && model.baseUrl.includes("openrouter.ai")) {
      identityHeaders.push("HTTP-Referer", "X-OpenRouter-Title", "X-OpenRouter-Categories");
    }
    if (model.provider !== "nvidia" && host === "integrate.api.nvidia.com") {
      identityHeaders.push("X-BILLING-INVOKE-ORIGIN");
    }
    if (!["opencode", "opencode-go"].includes(model.provider) && host === "opencode.ai") {
      identityHeaders.push("x-opencode-client");
    }
  }
  for (const name of identityHeaders) {
    // pi-ai merges identity, model, and option headers with case-sensitive
    // object spread before fetch normalization, so require exact key ownership.
    const coveredByModel = Boolean(model.headers && Object.hasOwn(model.headers, name));
    const coveredByResolved = Boolean(resolvedHeaders && Object.hasOwn(resolvedHeaders, name));
    if (!coveredByModel && !coveredByResolved) missing.add(name);
  }

  if (!["opencode", "opencode-go"].includes(model.provider) && host === "opencode.ai") {
    const coveredByResolved = Boolean(resolvedHeaders && Object.hasOwn(resolvedHeaders, "x-opencode-session"));
    const coveredByModel = sessionId !== undefined && model.headers?.["x-opencode-session"] === sessionId;
    if (!coveredByResolved && !coveredByModel) missing.add("x-opencode-session");
  }

  return [...missing].sort();
}

// Freeze only URL-dependent defaults before replacing the endpoint. Pi's
// public compat overrides take precedence over detection (nullish values do
// not override defaults). Provider/id-based behavior keeps its original input.
// Oracle: Pi 1.6.0's pi-ai 1.6.0, api/openai-completions.ts detectCompat/getCompat,
// api/openai-responses.ts detectSessionAffinityFormat and
// api/anthropic-messages.ts getAnthropicCompat. Real SDK payload tests cover
// this boundary so dependency updates cannot silently change semantics.
// 1.6.0 changes carried here: supportsDeveloperRole on OpenAI Chat is OpenAI-only
// (provider === "openai" || baseUrl includes "api.openai.com"); OpenAI Chat
// exposes promptCacheKeyMode so api.openai.com routes with prompt_cache_key
// intact; Anthropic Messages enables supportsMessageThreads on api.anthropic.com.
export function compatForRoutedModel(model: SelectedModel): SelectedModel["compat"] {
  const provider = model.provider;
  const url = model.baseUrl;
  const openRouter = provider === "openrouter" || url.includes("openrouter.ai");
  if (model.api === "openai-responses") {
    const explicit = model.compat as OpenAIResponsesCompat | undefined;
    return { ...explicit, sessionAffinityFormat: explicit?.sessionAffinityFormat ?? (openRouter ? "openrouter" : "openai") };
  }
  if (model.api === "anthropic-messages") {
    const explicit = model.compat as AnthropicMessagesCompat | undefined;
    const messageThreads = url.includes("api.anthropic.com");
    if (!openRouter && !messageThreads) return model.compat;
    return {
      ...explicit,
      ...(openRouter ? {
        sendSessionAffinityHeaders: explicit?.sendSessionAffinityHeaders ?? true,
        sessionAffinityFormat: explicit?.sessionAffinityFormat ?? "openrouter",
      } : {}),
      ...(messageThreads ? {
        supportsMessageThreads: explicit?.supportsMessageThreads ?? true,
      } : {}),
    };
  }
  if (model.api !== "openai-completions") return model.compat;

  const explicit = model.compat as OpenAICompletionsCompat | undefined;
  const zai = provider === "zai" || provider === "zai-coding-cn" || url.includes("api.z.ai") || url.includes("open.bigmodel.cn");
  const together = provider === "together" || url.includes("api.together.ai") || url.includes("api.together.xyz");
  const moonshot = provider === "moonshotai" || provider === "moonshotai-cn" || url.includes("api.moonshot.");
  const cloudflareWorkers = provider === "cloudflare-workers-ai" || url.includes("api.cloudflare.com");
  const cloudflareGateway = provider === "cloudflare-ai-gateway" || url.includes("gateway.ai.cloudflare.com");
  const nvidia = provider === "nvidia" || url.includes("integrate.api.nvidia.com");
  const antLing = provider === "ant-ling" || url.includes("api.ant-ling.com");
  const grok = provider === "xai" || url.includes("api.x.ai");
  const deepseek = provider === "deepseek" || url.toLowerCase().includes("deepseek.com");
  const isOpenAI = provider === "openai" || url.includes("api.openai.com");
  const nonStandard = nvidia || provider === "cerebras" || url.includes("cerebras.ai") || grok || together
    || url.includes("chutes.ai") || deepseek || zai || moonshot
    || provider === "opencode" || url.includes("opencode.ai") || cloudflareWorkers || cloudflareGateway || antLing;
  const maxTokens = url.includes("chutes.ai") || deepseek || moonshot || cloudflareGateway || together || nvidia || antLing || zai;
  return {
    ...explicit,
    supportsStore: explicit?.supportsStore ?? !nonStandard,
    supportsDeveloperRole: explicit?.supportsDeveloperRole ?? isOpenAI,
    supportsReasoningEffort: explicit?.supportsReasoningEffort ?? !(grok || zai || moonshot || together || cloudflareGateway || nvidia || antLing),
    maxTokensField: explicit?.maxTokensField ?? (maxTokens ? "max_tokens" : "max_completion_tokens"),
    requiresReasoningContentOnAssistantMessages: explicit?.requiresReasoningContentOnAssistantMessages ?? deepseek,
    thinkingFormat: explicit?.thinkingFormat ?? (deepseek ? "deepseek" : zai ? "zai" : together ? "together" : antLing ? "ant-ling" : openRouter ? "openrouter" : "openai"),
    sendSessionAffinityHeaders: explicit?.sendSessionAffinityHeaders ?? openRouter,
    sessionAffinityFormat: explicit?.sessionAffinityFormat ?? (openRouter ? "openrouter" : "openai"),
    supportsLongCacheRetention: explicit?.supportsLongCacheRetention ?? !(together || cloudflareWorkers || cloudflareGateway || nvidia || antLing),
    ...(url.includes("api.openai.com") ? { promptCacheKeyMode: explicit?.promptCacheKeyMode ?? "enabled" } : {}),
  };
}
