import assert from "node:assert/strict";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { learnReportOpener } from "../dist/index.js";
import { learnMoveBody, learnScoreBar } from "../dist/learn-tui.js";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

test("learn score bar is bounded and deterministic", () => {
  assert.equal(learnScoreBar(-10, 4), "────");
  assert.equal(learnScoreBar(50, 4), "━━──");
  assert.equal(learnScoreBar(110, 4), "━━━━");
});

test("learn move card keeps title, evidence, and action together", () => {
  const body = learnMoveBody({
    title: "Repeated deployment preamble",
    kind: "recurring context",
    detail: "largest ~623 tokens · up to 75 sessions · inferred",
    action: "Review before moving it to memory.",
  });
  assert.match(body, /^Repeated deployment preamble/m);
  assert.match(body, /largest ~623 tokens/);
  assert.match(body, /Review before moving it to memory/);
});

test("visual report opener uses native platform commands without a shell", () => {
  assert.deepEqual(learnReportOpener("/tmp/report.html", "darwin"), {
    command: "open",
    args: ["/tmp/report.html"],
  });
  assert.deepEqual(learnReportOpener("/tmp/report.html", "linux"), {
    command: "xdg-open",
    args: ["/tmp/report.html"],
  });
  assert.deepEqual(learnReportOpener("C:\\reports\\learn.html", "win32"), {
    command: "rundll32.exe",
    args: ["url.dll,FileProtocolHandler", "C:\\reports\\learn.html"],
  });
});

test("learn --plain stays compact and never reaches proxy", () => {
  const dir = mkdtempSync(join(tmpdir(), "caveman-learn-plain-"));
  const proxy = join(dir, "caveman-proxy");
  const argsFile = join(dir, "args.txt");
  writeFileSync(proxy, `#!/bin/sh
printf '%s\\n' "$@" >> "$CAVE_TEST_ARGS"
if [ "$2" = "report" ]; then
  printf '%s\\n' '{"schema":"caveman.learn.v1","basis":"inferred","sessions_scanned":0,"sessions_by_source":{},"cave_score":{"score":0,"basis":"inferred"},"sinks":[]}'
fi
`);
  chmodSync(proxy, 0o755);

  const result = spawnSync(process.execPath, [cli, "learn", "--plain", "--since", "7d"], {
    encoding: "utf8",
    env: {
      ...process.env,
      NO_COLOR: "1",
      CAVEMAN_HOME: dir,
      CAVEMAN_PROXY_BIN: proxy,
      CAVE_TEST_ARGS: argsFile,
    },
  });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /no Claude Code or Codex sessions found/);
  assert.doesNotMatch(readFileSync(argsFile, "utf8"), /--plain/);
  assert.match(readFileSync(argsFile, "utf8"), /--since\n7d/);
});

test("new proxy writes learn report from scan plan without a second analysis", () => {
  const dir = mkdtempSync(join(tmpdir(), "caveman-learn-single-pass-"));
  const proxy = join(dir, "caveman-proxy");
  const argsFile = join(dir, "args.txt");
  writeFileSync(proxy, `#!/bin/sh
printf '%s\\n' "$@" >> "$CAVE_TEST_ARGS"
if [ "$2" = "scan" ]; then
  mkdir -p "$CAVEMAN_HOME/reports"
  printf '<html>learn</html>\\n' > "$CAVEMAN_HOME/reports/caveman-learn.html"
  printf '%s\\n' '{"schema":"caveman.learn.v1","basis":"inferred","sessions_scanned":3,"sessions_by_source":{"claude":3},"cave_score":{"score":88,"basis":"inferred"},"sinks":[{"sink_id":"recurring_context:abc","practice_id":"prompt-prefix-stability","title":"Repeated context","class":"recurring_context","basis":"inferred","tokens_per_turn":420,"tokens_per_day_rate":1260}]}'
  exit 0
fi
exit 99
`);
  chmodSync(proxy, 0o755);

  const result = spawnSync(process.execPath, [cli, "learn", "--plain"], {
    encoding: "utf8",
    env: {
      ...process.env,
      NO_COLOR: "1",
      CAVEMAN_HOME: dir,
      CAVEMAN_PROXY_BIN: proxy,
      CAVE_TEST_ARGS: argsFile,
    },
  });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /Setup Score 88\/100/);
  const args = readFileSync(argsFile, "utf8");
  assert.match(args, /scan\n--write-report/);
  assert.doesNotMatch(args, /report\n--json/);
});

test("learn report token prevents a second scan when report mtime is unchanged", () => {
  const dir = mkdtempSync(join(tmpdir(), "caveman-learn-generation-"));
  const reports = join(dir, "reports");
  const proxy = join(dir, "caveman-proxy");
  const argsFile = join(dir, "args.txt");
  mkdirSync(reports, { recursive: true });
  writeFileSync(join(reports, "caveman-learn.html"), "<html>existing</html>\n");
  writeFileSync(proxy, `#!/bin/sh
printf '%s\\n' "$@" >> "$CAVE_TEST_ARGS"
if [ "$2" = "scan" ]; then
  previous=
  token=
  for arg in "$@"; do
    if [ "$previous" = "--write-report-token" ]; then token="$arg"; fi
    previous="$arg"
  done
  printf '{"generation":"%s"}\\n' "$token" > "$CAVEMAN_HOME/reports/caveman-learn.json"
  printf '%s\\n' '{"schema":"caveman.learn.v1","basis":"inferred","sessions_scanned":3,"sessions_by_source":{"claude":3},"cave_score":{"score":88,"basis":"inferred"},"sinks":[]}'
  exit 0
fi
exit 99
`);
  chmodSync(proxy, 0o755);

  const result = spawnSync(process.execPath, [cli, "learn", "--plain"], {
    encoding: "utf8",
    env: {
      ...process.env,
      NO_COLOR: "1",
      CAVEMAN_HOME: dir,
      CAVEMAN_PROXY_BIN: proxy,
      CAVE_TEST_ARGS: argsFile,
    },
  });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /3 sessions scanned/);
  const args = readFileSync(argsFile, "utf8");
  assert.match(args, /--write-report-token/);
  assert.doesNotMatch(args, /report\n--json/);
});

test("learn --json accepts reports larger than Node's default exec buffer", () => {
  const dir = mkdtempSync(join(tmpdir(), "caveman-learn-large-report-"));
  const proxy = join(dir, "caveman-proxy");
  writeFileSync(proxy, `#!/usr/bin/env node
const fs = require("node:fs");
const path = require("node:path");
const args = process.argv.slice(2);
const tokenAt = args.indexOf("--write-report-token");
const generation = tokenAt >= 0 ? args[tokenAt + 1] : "";
const reports = path.join(process.env.CAVEMAN_HOME, "reports");
fs.mkdirSync(reports, { recursive: true });
fs.writeFileSync(path.join(reports, "caveman-learn.html"), "<html>large</html>\\n");
fs.writeFileSync(path.join(reports, "caveman-learn.json"), JSON.stringify({ generation }));
const payload = {
  schema: "caveman.learn.v1",
  basis: "inferred",
  sessions_scanned: 3,
  sessions_by_source: { claude: 3 },
  cave_score: { score: 88, basis: "inferred" },
  sinks: [{
    sink_id: "behavioral:large",
    title: "Large local report",
    class: "behavioral",
    basis: "observed_local",
    tokens_per_turn: 0,
    tokens_per_day_rate: 0,
    suggestion: "x".repeat(2 * 1024 * 1024),
  }],
};
process.stdout.write(JSON.stringify(payload));
`);
  chmodSync(proxy, 0o755);

  const result = spawnSync(process.execPath, [cli, "learn", "--json"], {
    encoding: "utf8",
    maxBuffer: 8 * 1024 * 1024,
    env: {
      ...process.env,
      CAVEMAN_HOME: dir,
      CAVEMAN_PROXY_BIN: proxy,
    },
  });
  assert.equal(result.status, 0, result.stderr);
  const plan = JSON.parse(result.stdout);
  assert.equal(plan.sinks[0].suggestion.length, 2 * 1024 * 1024);
});
