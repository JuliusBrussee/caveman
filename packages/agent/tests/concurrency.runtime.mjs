import { test } from "node:test";
import assert from "node:assert/strict";
import crypto from "node:crypto";
import { resolveCaveRoute } from "../dist/runtime.js";

const gatewayIdentity = {
  ok: true,
  service: "caveman-proxy",
  schema: "caveman.proxy.health.v1",
  billing: "managed",
  adapters: 4,
};

test("cold concurrent runs share one gateway probe and startup attempt", async () => {
  const gatewayURL = `https://concurrency-${crypto.randomUUID()}.example`;
  const previousFetch = globalThis.fetch;
  let probes = 0;
  let releaseProbe;
  const blocked = new Promise((resolve) => { releaseProbe = resolve; });
  globalThis.fetch = async () => {
    probes += 1;
    await blocked;
    return Response.json(gatewayIdentity);
  };

  try {
    const requests = Array.from(
      { length: 64 },
      () => resolveCaveRoute(gatewayURL, { ensureRuntime: true }, false),
    );
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(probes, 1, "cold burst must not stampede runtime readiness/startup");
    releaseProbe();
    const routes = await Promise.all(requests);
    assert.equal(routes.every((route) => route.useGateway), true);
    assert.equal(routes.every((route) => route.providerBilling === "managed"), true);

    await resolveCaveRoute(gatewayURL, { ensureRuntime: true }, false);
    assert.equal(probes, 1, "completed result should remain memoized inside TTL");
  } finally {
    globalThis.fetch = previousFetch;
    releaseProbe();
  }
});

test("failed concurrent gateway probe is shared without weakening locked-plan failure", async () => {
  const gatewayURL = `https://concurrency-fail-${crypto.randomUUID()}.example`;
  const previousFetch = globalThis.fetch;
  let probes = 0;
  let releaseProbe;
  const blocked = new Promise((resolve) => { releaseProbe = resolve; });
  globalThis.fetch = async () => {
    probes += 1;
    await blocked;
    throw new Error("gateway unavailable");
  };

  try {
    const unlocked = Array.from(
      { length: 32 },
      () => resolveCaveRoute(gatewayURL, { ensureRuntime: true }, false),
    );
    const locked = resolveCaveRoute(gatewayURL, { ensureRuntime: true }, true);
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(probes, 1);
    releaseProbe();

    const routes = await Promise.all(unlocked);
    assert.equal(routes.every((route) => !route.useGateway), true);
    await assert.rejects(locked, /cave_gateway_required_for_locked_plan:/);
    assert.equal(probes, 1);
  } finally {
    globalThis.fetch = previousFetch;
    releaseProbe();
  }
});

test("a pinned legacy route is enriched before it can authorize dollars", async () => {
  const gatewayURL = `https://billing-${crypto.randomUUID()}.example`;
  let probes = 0;
  const route = await resolveCaveRoute(gatewayURL, {
    caveRoute: { useGateway: true },
    billingProofRequired: true,
    fetch: async () => {
      probes++;
      return Response.json({ ...gatewayIdentity, billing: "byok" });
    },
  }, false);
  assert.equal(probes, 1);
  assert.equal(route.useGateway, true);
  assert.equal(route.providerBilling, "byok");
});
