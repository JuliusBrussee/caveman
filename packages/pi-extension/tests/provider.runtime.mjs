// ProviderRouter unit tests. Pi's provider composer rewrites the baseUrl of
// EVERY model of a provider when an extension overrides that provider, so after
// the first route the registry can no longer report any model's original
// endpoint. The fake registry below reproduces that rewrite, which is the whole
// reason the router has to snapshot originals per model (#973).

import assert from "node:assert/strict";
import test from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";
import { join, dirname } from "node:path";

const dist = pathToFileURL(join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "testable.mjs")).href;
const { ProviderRouter } = await import(dist);

const GATEWAY = "http://127.0.0.1:8787";

function harness(models) {
  const real = new Map(models.map((m) => [`${m.provider}/${m.id}`, m.baseUrl]));
  const overrides = new Map();
  const calls = [];
  const notices = [];
  const live = () => models.map((m) => ({ ...m, baseUrl: overrides.get(m.provider) ?? real.get(`${m.provider}/${m.id}`) }));
  const pi = {
    registerProvider(name, config) {
      calls.push(["register", name, config.baseUrl]);
      overrides.set(name, config.baseUrl);
    },
    unregisterProvider(name) {
      calls.push(["unregister", name]);
      overrides.delete(name);
    },
    async setModel() {
      return true;
    },
  };
  const ctx = {
    hasUI: false,
    model: undefined,
    modelRegistry: {
      getAll: () => live(),
      find: (provider, id) => live().find((m) => m.provider === provider && m.id === id),
      isUsingOAuth: () => false,
    },
  };
  const router = new ProviderRouter(pi, (message) => notices.push(message));
  return { router, ctx, calls, notices, overrides, model: (id) => live().find((m) => m.id === id) };
}

test("a same-provider model with its own endpoint stays direct; the default keeps routing", async () => {
  const h = harness([
    { provider: "relay", id: "default-model", api: "openai-completions", baseUrl: "http://127.0.0.1:4000/v1" },
    { provider: "relay", id: "custom-model", api: "openai-completions", baseUrl: "http://127.0.0.1:1/v1" },
  ]);
  const compat = { relay: "http://127.0.0.1:4000" };

  h.ctx.model = h.model("default-model");
  await h.router.openGate(GATEWAY, h.ctx, compat);
  assert.equal(h.router.routing(), true, `notices: ${h.notices}`);
  assert.deepEqual(h.calls.at(-1), ["register", "relay", `${GATEWAY}/w/pi/compat/relay/v1`]);

  // The registry now reports the gateway route for BOTH models. Selecting the
  // custom one must read its own snapshotted endpoint, not the default's.
  await h.router.apply(h.model("custom-model"), h.ctx);
  assert.equal(h.router.routing(), false, "a model with a foreign endpoint must not ride the mount");
  assert.match(h.notices.at(-1), /pass-through for relay\/custom-model \(provider endpoint 127\.0\.0\.1:1 is not 127\.0\.0\.1:4000\); no compression/);

  await h.router.apply(h.model("default-model"), h.ctx);
  assert.equal(h.router.routing(), true, "the default model must route again");
  assert.deepEqual(h.calls.at(-1), ["register", "relay", `${GATEWAY}/w/pi/compat/relay/v1`]);
});

test("a provider with no published mount gets an actionable pass-through notice", async () => {
  const h = harness([{ provider: "unlisted-relay", id: "m", api: "openai-completions", baseUrl: "http://127.0.0.1:1/v1" }]);
  h.ctx.model = h.model("m");
  await h.router.openGate(GATEWAY, h.ctx, {});
  assert.equal(h.router.routing(), false);
  assert.match(h.notices.at(-1), /no compat mount named "unlisted-relay" in the local proxy; add compat\.unlisted-relay\.base_url to caveman\.yaml to route it/);
});
