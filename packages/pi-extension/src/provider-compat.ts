import type { ExtensionContext } from "@earendil-works/pi-coding-agent";
import type { OpenAICompletionsCompat, OpenAIResponsesCompat } from "@earendil-works/pi-ai";

type SelectedModel = NonNullable<ExtensionContext["model"]>;

// Pi's coding-agent layer adds these after auth resolution, outside model.compat.
// Canonical provider IDs keep their original attribution branch after routing.
// For aliases, an explicit later header or an identical model header can make
// the result independent of the active (private) telemetry setting. Require the
// original spelling: Pi merges these objects case-sensitively before the SDK.
export function unpreservedAttributionHeaders(
  model: SelectedModel, resolvedHeaders: Record<string, string | null> | undefined,
  sessionId: string | undefined, telemetryEnv = process.env.PI_TELEMETRY,
): string[] {
  let host: string | undefined;
  try { host = new URL(model.baseUrl).hostname; } catch { /* endpoint gate reports invalid URLs */ }
  const expected: Record<string, string | undefined> = {};
  const telemetryDisabled = telemetryEnv !== undefined && !["1", "true", "yes"].includes(telemetryEnv.toLowerCase());
  if (!telemetryDisabled) {
    if (model.provider !== "openrouter" && model.baseUrl.includes("openrouter.ai")) {
      Object.assign(expected, { "HTTP-Referer": "https://pi.dev", "X-OpenRouter-Title": "pi", "X-OpenRouter-Categories": "cli-agent" });
    }
    if (model.provider !== "nvidia" && host === "integrate.api.nvidia.com") expected["X-BILLING-INVOKE-ORIGIN"] = "Pi";
    if (!["cloudflare-workers-ai", "cloudflare-ai-gateway"].includes(model.provider)
      && ["api.cloudflare.com", "gateway.ai.cloudflare.com"].includes(host ?? "")) expected["User-Agent"] = "pi-coding-agent";
  }
  if (!["opencode", "opencode-go"].includes(model.provider) && host === "opencode.ai") {
    expected["x-opencode-session"] = sessionId;
    expected["x-opencode-client"] = "pi";
  }
  return Object.entries(expected).filter(([name, value]) => {
    if (resolvedHeaders && Object.hasOwn(resolvedHeaders, name)) return false;
    return value === undefined || model.headers?.[name] !== value;
  }).map(([name]) => name).sort();
}

// Freeze only URL-dependent defaults before replacing the endpoint. Pi's
// public compat overrides take precedence over detection (nullish values do
// not override defaults). Provider/id-based behavior keeps its original input.
// Oracle: Pi 0.84.2's pi-ai 0.84.2, api/openai-completions.ts detectCompat/getCompat and
// api/openai-responses.ts detectSessionAffinityFormat. Real SDK payload tests
// cover this boundary so dependency updates cannot silently change semantics.
export function compatForRoutedModel(model: SelectedModel): SelectedModel["compat"] {
  const provider = model.provider;
  const url = model.baseUrl;
  const openRouter = provider === "openrouter" || url.includes("openrouter.ai");
  if (model.api === "openai-responses") {
    const explicit = model.compat as OpenAIResponsesCompat | undefined;
    return { ...explicit, sessionAffinityFormat: explicit?.sessionAffinityFormat ?? (openRouter ? "openrouter" : "openai") };
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
  const nonStandard = nvidia || provider === "cerebras" || url.includes("cerebras.ai") || grok || together
    || url.includes("chutes.ai") || deepseek || zai || moonshot
    || provider === "opencode" || url.includes("opencode.ai") || cloudflareWorkers || cloudflareGateway || antLing;
  const maxTokens = url.includes("chutes.ai") || deepseek || moonshot || cloudflareGateway || together || nvidia || antLing || zai;
  const openRouterDeveloper = openRouter && (model.id.startsWith("anthropic/") || model.id.startsWith("openai/"));
  return {
    ...explicit,
    supportsStore: explicit?.supportsStore ?? !nonStandard,
    supportsDeveloperRole: explicit?.supportsDeveloperRole ?? (openRouterDeveloper || (!nonStandard && !openRouter)),
    supportsReasoningEffort: explicit?.supportsReasoningEffort ?? !(grok || zai || moonshot || together || cloudflareGateway || nvidia || antLing),
    maxTokensField: explicit?.maxTokensField ?? (maxTokens ? "max_tokens" : "max_completion_tokens"),
    requiresReasoningContentOnAssistantMessages: explicit?.requiresReasoningContentOnAssistantMessages ?? deepseek,
    thinkingFormat: explicit?.thinkingFormat ?? (deepseek ? "deepseek" : zai ? "zai" : together ? "together" : antLing ? "ant-ling" : openRouter ? "openrouter" : "openai"),
    supportsStrictMode: explicit?.supportsStrictMode ?? !(moonshot || together || cloudflareGateway || nvidia),
    sessionAffinityFormat: explicit?.sessionAffinityFormat ?? (openRouter ? "openrouter" : "openai"),
    supportsLongCacheRetention: explicit?.supportsLongCacheRetention ?? !(together || cloudflareWorkers || cloudflareGateway || nvidia || antLing),
  };
}
