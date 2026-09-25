// Route only the selected model object. Pi keeps owning its provider registry,
// auth, pricing, reasoning flags, context sizes, and model names. Registering
// and later unregistering a provider would delete another extension's custom
// models, API key fallback, OAuth registration, and stream handlers.

import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";
import { unforwardedProviderHeaders } from "../../cli/src/provider-routing.ts";
import { compatForRoutedModel, unpreservedAttributionHeaders } from "./provider-compat.ts";
import { MAX_MESSAGE_BYTES, boundedString, compatUpstreamFor, hostOf, isLoopbackUrl, routeForApi, upstreamHostFor } from "./protocol.ts";

type Notify = (message: string, kind: "warning" | "info") => void;

export class ProviderRouter {
  private pi: ExtensionAPI;
  private notify: Notify;
  private gateway: string | undefined;
  private gateOpen = false;
  private routed: {
    provider: string; id: string; originalBaseUrl: string; route: string;
    originalCompat: NonNullable<ExtensionContext["model"]>["compat"]; hadCompat: boolean;
  } | undefined;
  // Named compat mounts the running proxy published in its run-state file.
  private compatUpstreams: Readonly<Record<string, string>> | undefined;
  private providerUpstreams: Readonly<Record<string, string>> | undefined;
  private compatForwardHeaders: Readonly<Record<string, readonly string[]>> | undefined;
  private applying = false;
  private gateGeneration = 0;
  private warnedModels = new Set<string>();

  constructor(pi: ExtensionAPI, notify: Notify) {
    this.pi = pi;
    this.notify = notify;
  }

  // openGate is called once per session after the recovery gate held. Refuses
  // non-loopback gateways: managed routing needs auth proof v1 does not carry.
  async openGate(gateway: string, ctx: ExtensionContext, compatUpstreams?: Readonly<Record<string, string>>, providerUpstreams?: Readonly<Record<string, string>>, compatForwardHeaders?: Readonly<Record<string, readonly string[]>>): Promise<void> {
    if (!(await this.closeGate(ctx))) return;
    if (!isLoopbackUrl(gateway)) {
      this.notify("Caveman: direct mode, no compression this session (gateway is not loopback)", "warning");
      return;
    }
    this.compatUpstreams = compatUpstreams;
    this.providerUpstreams = providerUpstreams;
    this.compatForwardHeaders = compatForwardHeaders;
    this.gateway = gateway;
    this.gateOpen = true;
    await this.apply(ctx.model, ctx);
  }

  async closeGate(ctx: ExtensionContext): Promise<boolean> {
    this.gateOpen = false;
    this.gateGeneration++;
    return this.restoreCurrentModel(ctx);
  }

  routing(): boolean {
    return this.routed !== undefined;
  }

  // apply routes one model object, or restores direct mode when the model
  // has no verified route. Called from the gate and from model_select; the
  // applying flag swallows the model_select echo of our own setModel call.
  async apply(model: ExtensionContext["model"], ctx: ExtensionContext): Promise<void> {
    if (!this.gateOpen || this.applying || !this.gateway) return;
    if (!model) return;
    const gateGeneration = this.gateGeneration;
    const key = `${model.provider}/${model.id}`;
    // Repeated events can contain our routed object. Restore only its endpoint
    // for the gate check; a different model or externally changed endpoint owns
    // its own URL. The registry remains untouched, including same-provider
    // models whose endpoints differ (#973).
    const original = this.isOwnedRoute(model) ? this.routed!.originalBaseUrl : model.baseUrl;
    const route = routeForApi(this.gateway, model.api, model.provider, original, this.compatUpstreams, this.providerUpstreams);
    // Pi's Chat adapter derives short-retention prompt_cache_key directly from
    // this URL; its public compat type has no override. The payload hook cannot
    // recover the effective cache option or distinguish another extension's
    // removal, so leave this specific endpoint direct. Responses is unaffected.
    const compatibilityIssue = model.api === "openai-completions" && original.includes("api.openai.com")
      ? "Pi cannot preserve this OpenAI Chat endpoint's prompt cache keys through routing"
      : undefined;
    let headerIssue: string | undefined;
    let oauth = true;
    try {
      oauth = ctx.modelRegistry.isUsingOAuth(model);
    } catch {
      // Cannot determine the auth kind ⇒ treat as OAuth and refuse (uncertain ⇒ direct).
    }
    if (!oauth && route && !compatibilityIssue) {
      try {
        // Pi adds configured provider/auth headers during request preparation;
        // they need not appear on model.headers. Use its public resolver and
        // discard the API key. Never log values or replace the auth handler.
        const auth = await ctx.modelRegistry.getApiKeyAndHeaders(model);
        if (!auth.ok) throw new Error("request auth unavailable");
        const { headers } = auth;
        const published = {
          ...(this.compatUpstreams ? { compat_upstreams: this.compatUpstreams } : {}),
          ...(this.compatForwardHeaders ? { compat_forward_headers: this.compatForwardHeaders } : {}),
        };
        const missingHeaders = [...new Set([
          ...unforwardedProviderHeaders(model.api, model.provider, model.headers, published),
          ...unforwardedProviderHeaders(model.api, model.provider, headers, published),
        ])].sort();
        if (missingHeaders.length) {
          const remedy = missingHeaders.some(name => ["authorization", "x-api-key", "x-goog-api-key"].includes(name.toLowerCase()))
            ? "the proxy cannot preserve this authentication override; keep the provider direct"
            : `configure compat.${model.provider}.forward_headers`;
          headerIssue = `provider headers ${missingHeaders.join(", ")} are not forwarded; ${remedy}`;
        }
        if (!headerIssue) {
          let sessionId: string | undefined;
          try { sessionId = ctx.sessionManager.getSessionId(); } catch { /* unknown session affinity stays direct */ }
          const attribution = unpreservedAttributionHeaders({ ...model, baseUrl: original }, headers, sessionId);
          if (attribution.length) headerIssue = `Pi cannot preserve URL-derived headers ${attribution.join(", ")} for this provider alias; use its canonical provider ID or explicit headers`;
        }
      } catch {
        headerIssue = "provider request headers could not be resolved";
      }
      // Auth resolution can yield while the user changes models or the gate
      // closes/reopens. A stale result must never select its old model again.
      if (!this.gateOpen || this.gateGeneration !== gateGeneration || ctx.model !== model) return;
    }
    if (!route || oauth || compatibilityIssue || headerIssue) {
      if (!(await this.restoreCurrentModel(ctx))) return;
      if (!this.warnedModels.has(key)) {
        this.warnedModels.add(key);
        const mount = compatUpstreamFor(model.provider, this.compatUpstreams);
        const expected = mount !== undefined ? hostOf(mount) : upstreamHostFor(model.provider);
        const reason = oauth
          ? "OAuth/subscription credentials are not routed"
          : compatibilityIssue ?? headerIssue ?? (expected === undefined
            ? `no compat mount named "${model.provider}" in the local proxy; add compat.${model.provider}.base_url to caveman.yaml to route it`
            : hostOf(original) !== expected
              ? `provider endpoint ${hostOf(original) ?? original} is not ${expected}`
              : `provider endpoint path or API "${model.provider}/${model.api}" is not verified by the running proxy`);
        this.notify(boundedString(`Caveman: pass-through for ${key} (${reason}); no compression`, MAX_MESSAGE_BYTES), "warning");
      }
      return;
    }
    if (this.isOwnedRoute(model) && model.baseUrl === route) return;
    this.applying = true;
    try {
      this.routed = {
        provider: model.provider, id: model.id, originalBaseUrl: original, route,
        originalCompat: model.compat, hadCompat: Object.hasOwn(model, "compat"),
      };
      // Pi's setModel retains this model object for the next provider request;
      // only provider/id are persisted to session/settings. No provider-wide
      // override or replacement model catalogue is needed.
      const compat = compatForRoutedModel(model);
      if (!(await this.pi.setModel({ ...model, baseUrl: route, ...(compat === undefined ? {} : { compat }) }))) {
        if (await this.restoreCurrentModel(ctx)) {
          this.notify("Caveman: direct mode, no compression this session (model selection failed)", "warning");
        }
        return;
      }
    } catch {
      if (await this.restoreCurrentModel(ctx)) {
        this.notify("Caveman: direct mode, no compression this session (model selection failed)", "warning");
      }
    } finally {
      this.applying = false;
    }
  }

  private isOwnedRoute(model: ExtensionContext["model"]): boolean {
    return !!model && !!this.routed
      && model.provider === this.routed.provider && model.id === this.routed.id
      && model.baseUrl === this.routed.route;
  }

  private async restoreCurrentModel(ctx: ExtensionContext): Promise<boolean> {
    const current = ctx.model;
    if (!this.isOwnedRoute(current)) {
      this.routed = undefined;
      return true;
    }
    const { originalBaseUrl, originalCompat, hadCompat } = this.routed!;
    const direct = { ...current!, baseUrl: originalBaseUrl };
    if (hadCompat) direct.compat = originalCompat;
    else delete direct.compat;
    const wasApplying = this.applying;
    this.applying = true;
    try {
      if (await this.pi.setModel(direct)) {
        this.routed = undefined;
        return true;
      }
    } catch { /* the warning below keeps a failed restoration explicit */ }
    finally { this.applying = wasApplying; }
    this.notify("Caveman: could not restore the model's direct endpoint; select the model again before continuing", "warning");
    return false;
  }
}
