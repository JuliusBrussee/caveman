// Provider routing: baseUrl-only overrides pointing the selected model's
// provider at the local Caveman gateway. No model catalogue is created and
// models.json is never replaced — Pi keeps owning auth, pricing, reasoning
// flags, context sizes, and model names.

import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";
import { isLoopbackUrl, routeForApi } from "./protocol.ts";

type Notify = (message: string, kind: "warning" | "info") => void;

export class ProviderRouter {
  private pi: ExtensionAPI;
  private notify: Notify;
  private gateway: string | undefined;
  private gateOpen = false;
  private overridden = new Set<string>();
  private applying = false;
  private warnedModels = new Set<string>();

  constructor(pi: ExtensionAPI, notify: Notify) {
    this.pi = pi;
    this.notify = notify;
  }

  // openGate is called once per session after the recovery gate held. Refuses
  // non-loopback gateways: managed routing needs auth proof v1 does not carry.
  async openGate(gateway: string, ctx: ExtensionContext): Promise<void> {
    if (!isLoopbackUrl(gateway)) {
      this.notify("Caveman: direct mode, no compression this session (gateway is not loopback)", "warning");
      return;
    }
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
    const route = routeForApi(this.gateway, model.api);
    let oauth = true;
    try {
      oauth = ctx.modelRegistry.isUsingOAuth(model);
    } catch {
      // Cannot determine the auth kind ⇒ treat as OAuth and refuse (uncertain ⇒ direct).
    }
    if (!route || oauth) {
      this.clearOverrides();
      const key = `${model.provider}/${model.id}`;
      if (!this.warnedModels.has(key)) {
        this.warnedModels.add(key);
        const reason = oauth ? "OAuth/subscription credentials are not routed" : `unsupported API "${model.api}"`;
        this.notify(`Caveman: pass-through for ${key} (${reason}); no compression`, "warning");
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

  private clearOverrides(): void {
    for (const provider of this.overridden) {
      try {
        this.pi.unregisterProvider(provider);
      } catch { /* restoring direct is best-effort */ }
    }
    this.overridden.clear();
  }
}
