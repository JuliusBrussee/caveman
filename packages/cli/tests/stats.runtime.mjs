import assert from "node:assert/strict";
import test from "node:test";
import { copyFileSync, linkSync, readFileSync, symlinkSync } from "node:fs";
import { join } from "node:path";
import { parseStatsOptions, renderStatsSummary } from "../dist/stats-cli.js";
import { isolatedCliEnv, runCli } from "./_cli.mjs";
import { nativeStub, stubEnv } from "./harness/stub-bin.mjs";

function statsEnv() {
  const isolated = isolatedCliEnv();
  const { home } = isolated;
  const bin = join(home, "bin");
  const node = join(bin, process.platform === "win32" ? "node.exe" : "node");
  if (process.platform === "win32") {
    try { linkSync(process.execPath, node); } catch { copyFileSync(process.execPath, node); }
  } else {
    // Preserve relative shared-library lookup for dynamically linked Node builds.
    symlinkSync(process.execPath, node);
  }
  const noop = nativeStub(bin, "stats-noop", "");
  // Only fixture binaries and temporary config are visible to the command.
  isolated.env = stubEnv({
    ...(process.env.SystemRoot ? { SystemRoot: process.env.SystemRoot } : {}),
    ...(process.env.PATHEXT ? { PATHEXT: process.env.PATHEXT } : {}),
    PATH: "", HOME: home, USERPROFILE: home,
    APPDATA: join(home, "AppData", "Roaming"), LOCALAPPDATA: join(home, "AppData", "Local"),
    CAVEMAN_HOME: home, CAVE_NO_KEYCHAIN: "1", NO_COLOR: "1", CI: "1",
    CAVEMAN_TELEMETRY: "0", CAVEMAN_OFFLINE: "1", CAVE_GATEWAY_URL: "http://127.0.0.1:9",
    CAVEMAN_PROXY_BIN: noop, CAVEMAN_ENGINE_BIN: noop, CAVEMAN_MCP_BIN: noop,
    CAVEMAN_BROWSE_BIN: noop, CAVEMEM_BIN: noop,
  }, bin);
  return isolated;
}

function usageReport(metrics = {}) {
  return {
    schema: "caveman.stats.v1",
    window: { days: 30, lifetime: false },
    totals: {
      requests: 1, successful_requests: 1, failed_requests: 0,
      input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0,
      complete_usage_requests: 0, partial_usage_requests: 1,
      measured_requests: 0, unmeasured_requests: 1, before_tokens: 0, after_tokens: 0, saved_tokens: 0,
      api_savings_usd: null, api_equivalent_savings_usd: null, api_spend_usd: null,
      api_savings_requests: 0, equivalent_savings_requests: 0, api_spend_requests: 0,
      ...metrics,
    },
  };
}

test("stats terminal keeps absent provider usage and comparisons unknown", () => {
  const text = renderStatsSummary(usageReport());
  assert.match(text, /Provider usage\s+unknown input \/ unknown output/);
  assert.match(text, /Cache usage\s+unknown read \/ unknown written/);
  assert.match(text, /Usage coverage\s+0 complete \/ 1 partial or unavailable/);
  assert.match(text, /Compared request tokens\s+unavailable/);
  assert.doesNotMatch(text, /0 original \/ 0 delivered/);
});

test("stats terminal labels observed subtotals when usage is incomplete", () => {
  const text = renderStatsSummary(usageReport({
    requests: 2, successful_requests: 2, complete_usage_requests: 1, partial_usage_requests: 1,
    input_tokens: 100, output_tokens: 10, cache_read_tokens: 20,
  }));
  assert.match(text, /Provider usage\s+100 observed input \/ 10 observed output/);
  assert.match(text, /Cache usage\s+20 observed read \/ 0 observed written/);
  assert.match(text, /Usage coverage\s+1 complete \/ 1 partial or unavailable/);
});

test("stats terminal preserves reported zero usage and measured zero comparisons", () => {
  const text = renderStatsSummary(usageReport({
    complete_usage_requests: 1, partial_usage_requests: 0, measured_requests: 1, unmeasured_requests: 0,
  }));
  assert.match(text, /Provider usage\s+0 input \/ 0 output/);
  assert.match(text, /Cache usage\s+0 read \/ 0 written/);
  assert.match(text, /Compared request tokens\s+0 original \/ 0 delivered/);
});

test("stats validates filters and incompatible modes before touching the store", () => {
  assert.deepEqual(parseStatsOptions(["--days", "7", "--provider", "openai", "--json"]).filters,
    ["--days", "7", "--provider", "openai"]);
  assert.deepEqual(parseStatsOptions(["--all-time"]).filters, ["--days", "0"]);
  for (const args of [
    ["--days", "0"], ["--days", "3661"], ["--days", "1.5"],
    ["--days", "NaN"], ["--provider"], ["--model", "--json"],
    ["--days", "7", "--all-time"], ["--json", "--open"],
    ["--plain", "--open"], ["--json", "--json"], ["--unknown"],
  ]) assert.throws(() => parseStatsOptions(args), undefined, args.join(" "));
});

test("stats help is local and does not require a proxy", async () => {
  const isolated = statsEnv();
  isolated.env.CAVEMAN_PROXY_BIN = join(isolated.home, "missing", "proxy");
  try {
    const out = await runCli(["stats", "--help"], { env: isolated.env, cwd: isolated.home, prefix: "" });
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stdout, /subscription API equivalents are separate/);
    assert.match(out.stdout, /--all-time/);
  } finally { isolated.cleanup(); }
});

test("stats JSON uses the rich report without opening or writing HTML", async () => {
  const isolated = statsEnv();
  try {
    const log = join(isolated.home, "args.json");
    isolated.env.CAVEMAN_PROXY_BIN = nativeStub(join(isolated.home, "bin"), "stats-proxy", `
require('node:fs').writeFileSync(process.env.STATS_TEST_LOG, JSON.stringify(ARGV));
console.log(JSON.stringify({schema:'caveman.stats.v1',basis:'inferred',totals:{requests:0},groups:[],series:[],recent:[]}));
`);
    isolated.env.STATS_TEST_LOG = log;
    const out = await runCli(["stats", "--json", "--provider", "openai", "--days", "7"],
      { env: isolated.env, cwd: isolated.home, prefix: "" });
    assert.equal(out.code, 0, out.stderr);
    assert.equal(JSON.parse(out.stdout).schema, "caveman.stats.v1");
    assert.deepEqual(JSON.parse(readFileSync(log, "utf8")), ["stats", "--report", "--provider", "openai", "--days", "7"]);
    assert.doesNotMatch(out.stderr, /Opening|dashboard/);
  } finally { isolated.cleanup(); }
});
