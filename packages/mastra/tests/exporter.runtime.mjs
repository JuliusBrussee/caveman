/**
 * Exporter-configuration tests. No network.
 * Run: node --test tests/exporter.runtime.mjs (after `pnpm build`).
 */
import { test } from "node:test";
import assert from "node:assert/strict";

import { cavemanExporterConfig } from "../dist/index.js";

const base = {
  baseUrl: "https://gateway.caveman.so",
  apiKey: "cave_live_abcdefghijkl_secret",
  projectId: "project_123",
  environment: "production",
};

test("endpoint is the gateway OTLP route and trailing slashes are trimmed", () => {
  const config = cavemanExporterConfig({ ...base, baseUrl: "https://gateway.caveman.so//" });
  assert.equal(config.endpoint, "https://gateway.caveman.so/otlp/v1/traces");
  assert.equal(config.provider.custom.endpoint, config.endpoint);
});

test("auth headers carry the project key in both forms the gateway accepts", () => {
  const config = cavemanExporterConfig(base);
  assert.equal(config.headers["x-cave-api-key"], base.apiKey);
  assert.equal(config.headers.authorization, `Bearer ${base.apiKey}`);
  assert.equal(config.provider.custom.headers, config.headers);
});

test("agent id becomes the x-cave-agent header", () => {
  const config = cavemanExporterConfig({ ...base, agentId: "support-agent" });
  assert.equal(config.headers["x-cave-agent"], "support-agent");
  const without = cavemanExporterConfig(base);
  assert.equal("x-cave-agent" in without.headers, false);
});

test("resource attributes match the required metadata block (spec 23.3)", () => {
  const config = cavemanExporterConfig({
    ...base,
    agentId: "support-agent",
    agentVersion: "31",
    workflowVersion: "17",
    serviceVersion: "git-sha",
  });
  assert.deepEqual(config.resourceAttributes, {
    "caveman.project.id": "project_123",
    "caveman.environment": "production",
    "caveman.agent.id": "support-agent",
    "caveman.agent.version": "31",
    "caveman.workflow.version": "17",
    "service.version": "git-sha",
  });
});

test("optional metadata is omitted rather than emitted empty", () => {
  const config = cavemanExporterConfig(base);
  assert.deepEqual(Object.keys(config.resourceAttributes).sort(), [
    "caveman.environment",
    "caveman.project.id",
  ]);
  assert.equal("serviceName" in config, false);
});

test("caller headers and resource attributes merge last", () => {
  const config = cavemanExporterConfig({
    ...base,
    agentId: "a",
    headers: { "x-cave-agent": "override", "x-tenant": "t1" },
    resourceAttributes: { "deployment.id": "d1" },
    serviceName: "support",
  });
  assert.equal(config.headers["x-cave-agent"], "override");
  assert.equal(config.headers["x-tenant"], "t1");
  assert.equal(config.resourceAttributes["deployment.id"], "d1");
  assert.equal(config.serviceName, "support");
});

test("protocol defaults to http/json and other protocols are rejected", () => {
  assert.equal(cavemanExporterConfig(base).protocol, "http/json");
  assert.throws(() => cavemanExporterConfig({ ...base, protocol: "http/protobuf" }), /protobuf-JSON only/);
  assert.throws(() => cavemanExporterConfig({ ...base, protocol: "grpc" }), /protobuf-JSON only/);
});

test("required fields fail closed", () => {
  assert.throws(() => cavemanExporterConfig({ ...base, baseUrl: "" }), /baseUrl is required/);
  assert.throws(() => cavemanExporterConfig({ ...base, baseUrl: "gateway.caveman.so" }), /absolute http\(s\) URL/);
  assert.throws(() => cavemanExporterConfig({ ...base, apiKey: "  " }), /apiKey is required/);
  assert.throws(() => cavemanExporterConfig({ ...base, projectId: "" }), /projectId is required/);
  assert.throws(() => cavemanExporterConfig({ ...base, environment: "" }), /environment is required/);
});

test("credentials never ride plain http to a non-loopback host", () => {
  assert.throws(() => cavemanExporterConfig({ ...base, baseUrl: "http://attacker.example:8080" }), /must use https/);
  assert.throws(() => cavemanExporterConfig({ ...base, baseUrl: "ftp://gateway.caveman.so" }), /absolute http\(s\) URL/);
  // Loopback development targets stay usable.
  const local = cavemanExporterConfig({ ...base, baseUrl: "http://localhost:8787" });
  assert.equal(local.endpoint, "http://localhost:8787/otlp/v1/traces");
  const loopback = cavemanExporterConfig({ ...base, baseUrl: "http://127.0.0.1:8787" });
  assert.equal(loopback.endpoint, "http://127.0.0.1:8787/otlp/v1/traces");
});

test("a prototype-chain span type never classifies a span", async () => {
  const { cavemanSpanProcessor } = await import("../dist/index.js");
  const processor = cavemanSpanProcessor();
  const attrs = { "mastra.span.type": "constructor" };
  const span = {
    name: "mystery",
    attributes: attrs,
    setAttribute(key, value) {
      attrs[key] = value;
    },
  };
  processor.onStart(span);
  processor.onEnd(span);
  const diag = processor.diagnostics();
  assert.equal(diag.unmappedSpans, 1);
  assert.equal(diag.contextOnlySpans, 0);
  assert.deepEqual(diag.unmappedSpanNames, ["mystery"]);
});
