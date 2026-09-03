// Provider routing: baseUrl-only overrides pointing the selected model's
// provider at the local Caveman gateway. No model catalogue is created and
// models.json is never replaced — Pi keeps owning auth, pricing, reasoning
// flags, context sizes, and model names.

import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";
import { MAX_MESSAGE_BYTES, boundedString, compatUpstreamFor, hostOf, isLoopbackUrl, routeForApi, upstreamHostFor } from "./protocol.ts";

type Notify = (message: string, kind: "warning" | "info") => void;

export class ProviderRouter {
  private pi: ExtensionAPI;
  private notify: Notify;
  private gateway: string | undefined;
  private gateOpen = false;
  private overridden = new Set<string>();
  // originals keeps the base URL of each model, keyed "<provider>/<id>". After
  // an override, the registry reports the gateway route for EVERY model of that
  // provider, so the originals are not available from the registry. Per model,
  // not per provider: Pi lets each model carry its own baseUrl, and a
  // same-provider model pointing at another endpoint must not inherit the
  // default model's URL (#973). Pi keeps extension overrides in memory only, so
  // a new pi process always starts from the original base URLs and this map
  // never sees a stale override from an earlier process.
  private originals = new Map<string, string>();
  // Named compat mounts the running proxy published in its run-state file.
  private compatUpstreams: Readonly<Record<string, string>> | undefined;
  private applying = false;
  private warnedModels = new Set<string>();

  constructor(pi: ExtensionAPI, notify: Notify) {
    this.pi = pi;
    this.notify = notify;
  }

  // openGate is called once per session after the recovery gate held. Refuses
  // non-loopback gateways: managed routing needs auth proof v1 does not carry.
  async openGate(gateway: string, ctx: ExtensionContext, compatUpstreams?: Readonly<Record<string, string>>): Promise<void> {
    if (!isLoopbackUrl(gateway)) {
      this.notify("Caveman: direct mode, no compression this session (gateway is not loopback)", "warning");
      return;
    }
    this.compatUpstreams = compatUpstreams;
    this.gateway = gateway;
    this.gateOpen = true;
    await this.apply(ctx.model, ctx);
  }

  closeGate(): void {
    this.gateOpen = false;
    this.clearOverrides();
  }

  routing(): boolean {
    return this.gateOpen && this.overridden.size > 0;
  }

  // apply routes one model's provider, or restores direct mode when the model
  // has no verified route. Called from the gate and from model_select; the
  // applying flag swallows the model_select echo of our own setModel call.
  async apply(model: ExtensionContext["model"], ctx: ExtensionContext): Promise<void> {
    if (!this.gateOpen || this.applying || !this.gateway) return;
    if (!model) return;
    const key = `${model.provider}/${model.id}`;
    // While the provider is not overridden the registry still reports real base
    // URLs, so this is the only moment the originals can be captured. A model
    // missing from the snapshot reads the gateway route later and fails the host
    // gate — fail-closed to direct, never routed to the wrong upstream.
    if (!this.overridden.has(model.provider)) this.snapshot(model, ctx);
    const original = this.originals.get(key) ?? model.baseUrl;
    const route = routeForApi(this.gateway, model.api, model.provider, original, this.compatUpstreams);
    let oauth = true;
    try {
      oauth = ctx.modelRegistry.isUsingOAuth(model);
    } catch {
      // Cannot determine the auth kind ⇒ treat as OAuth and refuse (uncertain ⇒ direct).
    }
    if (!route || oauth) {
      this.clearOverrides();
      if (!this.warnedModels.has(key)) {
        this.warnedModels.add(key);
        const mount = compatUpstreamFor(model.provider, this.compatUpstreams);
        const expected = mount !== undefined ? hostOf(mount) : upstreamHostFor(model.provider);
        const reason = oauth
          ? "OAuth/subscription credentials are not routed"
          : expected === undefined
            ? `no compat mount named "${model.provider}" in the local proxy; add compat.${model.provider}.base_url to caveman.yaml to route it`
            : hostOf(original) !== expected
              ? `provider endpoint ${hostOf(original) ?? original} is not ${expected}`
              : `unsupported provider/API "${model.provider}/${model.api}"`;
        this.notify(boundedString(`Caveman: pass-through for ${key} (${reason}); no compression`, MAX_MESSAGE_BYTES), "warning");
      }
      return;
    }
    this.applying = true;
    try {
      for (const provider of this.overridden) {
        if (provider !== model.provider) {
          this.pi.unregisterProvider(provider);
          this.overridden.delete(provider);
        }
      }
      this.originals.set(key, original);
      this.pi.registerProvider(model.provider, { baseUrl: route });
      this.overridden.add(model.provider);
      // Re-resolve so the FIRST request uses the refreshed model object. A
      // model that cannot be re-resolved or applied would keep sending direct
      // while the registry claims routing — restore direct honestly instead.
      const refreshed = ctx.modelRegistry.find(model.provider, model.id);
      if (!refreshed || !(await this.pi.setModel(refreshed))) {
        this.clearOverrides();
        this.notify("Caveman: direct mode, no compression this session (model re-resolution failed)", "warning");
        return;
      }
    } catch {
      this.clearOverrides();
      this.notify("Caveman: direct mode, no compression this session (provider override failed)", "warning");
    } finally {
      this.applying = false;
    }
  }

  // snapshot records the real base URL of every model of this provider, so a
  // later selection inside the same provider is gated against its OWN endpoint.
  private snapshot(model: NonNullable<ExtensionContext["model"]>, ctx: ExtensionContext): void {
    try {
      for (const candidate of ctx.modelRegistry.getAll()) {
        if (candidate.provider === model.provider && typeof candidate.baseUrl === "string") {
          this.originals.set(`${candidate.provider}/${candidate.id}`, candidate.baseUrl);
        }
      }
    } catch { /* registry enumeration is best-effort; the selected model is below */ }
    if (typeof model.baseUrl === "string") this.originals.set(`${model.provider}/${model.id}`, model.baseUrl);
  }

  private clearOverrides(): void {
    for (const provider of this.overridden) {
      try {
        this.pi.unregisterProvider(provider);
      } catch { /* restoring direct is best-effort */ }
    }
    this.overridden.clear();
    this.originals.clear();
  }
}
