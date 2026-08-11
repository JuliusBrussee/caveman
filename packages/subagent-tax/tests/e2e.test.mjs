// Full-pipeline test with a fake `claude` binary on PATH: detect → sink →
// capture → analyze → report → manifest, no real harness or network needed.
import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, existsSync } from "node:fs";
import { join, dirname } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const pkgDir = join(dirname(fileURLToPath(import.meta.url)), "..");

const FAKE_CLAUDE = `#!/usr/bin/env node
if (process.argv.includes("--version")) { console.log("9.9.9 (fake-claude)"); process.exit(0); }
const fixture = JSON.parse(require("node:fs").readFileSync(${JSON.stringify(join(pkgDir, "fixtures", "anthropic-first-request.json"))}, "utf8"));
const base = process.env.ANTHROPIC_BASE_URL;
// A small warmup ping first, then the real prefix — mirrors real harness behavior.
const post = (body) => fetch(base + "/v1/messages", {
  method: "POST",
  headers: { "content-type": "application/json", "x-api-key": process.env.ANTHROPIC_API_KEY },
  body: JSON.stringify(body),
});
(async () => {
  await post({ model: "claude-haiku", messages: [{ role: "user", content: "ping" }] });
  await post(fixture);
  console.log("DONE");
  process.exit(0);
})();
`;

// The fake `claude` fixtures are extensionless Unix scripts prepended to PATH;
// Windows cannot exec them (PATHEXT resolution). Windows PATH-shim handling
// has dedicated coverage in process-tree.test.mjs.
const unixOnly = { skip: process.platform === "win32" ? "unix shell-script fixture" : false };

test("run.mjs end-to-end with a fake claude on PATH", unixOnly, async () => {
  const binDir = mkdtempSync(join(tmpdir(), "sat-bin-"));
  const outDir = mkdtempSync(join(tmpdir(), "sat-out-"));
  const fakePath = join(binDir, "claude");
  writeFileSync(fakePath, FAKE_CLAUDE, { mode: 0o755 });

  const stdout = execFileSync(process.execPath, [join(pkgDir, "run.mjs"), "--harness", "claude", "--out", join(outDir, "report"), "--timeout", "30", "--grace", "1"], {
    encoding: "utf8",
    env: { ...process.env, PATH: `${binDir}:${process.env.PATH}` },
    timeout: 60000,
  });

  assert.ok(stdout.includes("The Subagent Tax"), "prints the headline");
  assert.ok(stdout.includes("claude"), "prints the harness row");
  assert.ok(stdout.includes("Basis: inferred"), "prints the honesty block");
  assert.ok(stdout.includes("bill delta"), "prints the cache-honesty line");

  const report = JSON.parse(readFileSync(join(outDir, "report", "report.json"), "utf8"));
  assert.equal(report.basis, "inferred");
  assert.ok(Array.isArray(report.honesty) && report.honesty.length >= 4);
  const row = report.rows[0];
  assert.equal(row.harness, "claude");
  assert.equal(row.status, "ok");
  assert.equal(row.version, "9.9.9 (fake-claude)");
  assert.equal(row.primary.tools_count, 3, "primary is the fixture, not the warmup ping");
  assert.equal(row.primary.mcp_tools_count, 1);
  assert.equal(row.tokens.basis, "est");
  assert.ok(row.seconds_to_first_capture >= 0);
  assert.equal(row.captures_total, 2);

  const manifest = readFileSync(join(outDir, "report", "manifest.sha256"), "utf8");
  assert.ok(manifest.includes("report.json"));
  assert.ok(manifest.match(/claude\/captures\/00\d-anthropic-messages\.json/), "manifest covers captures");
  assert.ok(existsSync(join(outDir, "report", "claude", "harness-output.log")));

  for (const line of manifest.trim().split("\n")) {
    assert.match(line, /^[0-9a-f]{64}  \S/);
  }
});

// A harness that captures then hangs forever — the timeout/grace/killTree path
// that no other test exercises.
const HANGING_CLAUDE = `#!/usr/bin/env node
if (process.argv.includes("--version")) { console.log("1.0.0 (hanging)"); process.exit(0); }
const fixture = JSON.parse(require("node:fs").readFileSync(${JSON.stringify(join(pkgDir, "fixtures", "anthropic-first-request.json"))}, "utf8"));
fetch(process.env.ANTHROPIC_BASE_URL + "/v1/messages", {
  method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(fixture),
}).then(() => { setInterval(() => {}, 1000); });  // never exits
`;

test("a harness that never exits is still measured, then stopped", unixOnly, () => {
  const binDir = mkdtempSync(join(tmpdir(), "sat-hang-"));
  const outDir = mkdtempSync(join(tmpdir(), "sat-hangout-"));
  writeFileSync(join(binDir, "claude"), HANGING_CLAUDE, { mode: 0o755 });

  const started = Date.now();
  const stdout = execFileSync(
    process.execPath,
    [join(pkgDir, "run.mjs"), "--harness", "claude", "--out", join(outDir, "r"), "--timeout", "20", "--grace", "1", "--json"],
    { encoding: "utf8", env: { ...process.env, PATH: `${binDir}:${process.env.PATH}` }, timeout: 60000 },
  );
  const elapsed = (Date.now() - started) / 1000;
  const report = JSON.parse(stdout);
  assert.equal(report.rows[0].status, "ok", "the capture still counts");
  assert.equal(report.rows[0].primary.tools_count, 3);
  assert.ok(elapsed < 20, `stopped on the grace window (${elapsed.toFixed(1)}s), not the full timeout`);
});

test("stdout never claims savings or dollars", () => {
  const binDir = mkdtempSync(join(tmpdir(), "sat-bin4-"));
  const outDir = mkdtempSync(join(tmpdir(), "sat-out4-"));
  writeFileSync(join(binDir, "claude"), FAKE_CLAUDE, { mode: 0o755 });
  const stdout = execFileSync(
    process.execPath,
    [join(pkgDir, "run.mjs"), "--harness", "claude", "--out", join(outDir, "r"), "--timeout", "30", "--grace", "1"],
    { encoding: "utf8", env: { ...process.env, PATH: `${binDir}:${process.env.PATH}` }, timeout: 60000 },
  );
  // "not savings" is the disclaimer; strip it before checking for claims.
  const claims = stdout.replace(/not savings/gi, "").replace(/not spend/gi, "");
  for (const forbidden of [/\bsaved\b/i, /\bsavings\b/i, /\$\d/, /\bcheaper\b/i, /\bverified\b/i]) {
    assert.ok(!forbidden.test(claims), `output must not contain ${forbidden}`);
  }
  assert.match(stdout, /Basis: inferred/);
});

test("--repeat reports the median trial plus observed spread", unixOnly, () => {
  const binDir = mkdtempSync(join(tmpdir(), "sat-bin3-"));
  const outDir = mkdtempSync(join(tmpdir(), "sat-out3-"));
  writeFileSync(join(binDir, "claude"), FAKE_CLAUDE, { mode: 0o755 });

  const stdout = execFileSync(
    process.execPath,
    [join(pkgDir, "run.mjs"), "--harness", "claude", "--out", join(outDir, "r"), "--repeat", "3", "--timeout", "30", "--grace", "1", "--json"],
    { encoding: "utf8", env: { ...process.env, PATH: `${binDir}:${process.env.PATH}` }, timeout: 90000 },
  );
  const report = JSON.parse(stdout);
  assert.equal(report.repeat, 3);
  assert.ok(report.honesty.some((l) => l.includes("not a confidence interval")), "spread note added");
  const row = report.rows[0];
  assert.equal(row.trials, 3);
  assert.equal(row.trials_ok, 3);
  assert.equal(row.spread.all_chars.length, 3);
  assert.equal(row.spread.median_chars, row.primary.total_chars);
  assert.ok(row.spread.min_chars <= row.spread.median_chars && row.spread.median_chars <= row.spread.max_chars);
  assert.ok(existsSync(join(outDir, "r", "claude", "trial-2", "captures")), "per-trial captures kept");
});

test("run.mjs refuses to write into a non-empty directory it did not create", () => {
  const victim = mkdtempSync(join(tmpdir(), "sat-victim-"));
  writeFileSync(join(victim, "important.txt"), "user data");
  assert.throws(
    () => execFileSync(process.execPath, [join(pkgDir, "run.mjs"), "--harness", "claude", "--out", victim], { encoding: "utf8", stdio: "pipe", timeout: 30000 }),
    (err) => {
      assert.equal(err.status, 2);
      assert.match(err.stderr, /refusing to write/);
      return true;
    },
  );
  assert.equal(readFileSync(join(victim, "important.txt"), "utf8"), "user data");
});

test("run.mjs reports a not-installed harness without failing", () => {
  const outDir = mkdtempSync(join(tmpdir(), "sat-out2-"));
  const emptyBin = mkdtempSync(join(tmpdir(), "sat-bin2-"));
  mkdirSync(emptyBin, { recursive: true });
  const stdout = execFileSync(
    process.execPath,
    [join(pkgDir, "run.mjs"), "--harness", "claude", "--out", join(outDir, "report"), "--json"],
    {
      encoding: "utf8",
      env: { ...process.env, PATH: emptyBin },
      timeout: 30000,
    },
  );
  const report = JSON.parse(stdout);
  assert.equal(report.rows[0].status, "not installed");
});
