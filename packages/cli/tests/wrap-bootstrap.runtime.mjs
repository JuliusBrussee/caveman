import test from "node:test";
import assert from "node:assert/strict";

const { shouldBootstrapWrapRuntime } = await import("../dist/index.js?wrap-bootstrap");

const base = {
  local: true,
  proxyEnabled: true,
  interactive: true,
  explicitBinaryOverride: false,
  runtimeReady: false,
};

test("first interactive local wrap bootstraps missing signed runtime", () => {
  assert.equal(shouldBootstrapWrapRuntime(base), true);
});

test("wrap bootstrap never mutates explicit or automated runtime choices", () => {
  assert.equal(shouldBootstrapWrapRuntime({ ...base, local: false }), false);
  assert.equal(shouldBootstrapWrapRuntime({ ...base, proxyEnabled: false }), false);
  assert.equal(shouldBootstrapWrapRuntime({ ...base, interactive: false }), false);
  assert.equal(shouldBootstrapWrapRuntime({ ...base, explicitBinaryOverride: true }), false);
  assert.equal(shouldBootstrapWrapRuntime({ ...base, runtimeReady: true }), false);
});
