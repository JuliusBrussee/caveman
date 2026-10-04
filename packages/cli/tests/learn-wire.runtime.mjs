import assert from "node:assert/strict";
import { chmodSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import test from "node:test";
import { isolatedCliEnv, runCli } from "./_cli.mjs";

// A stand-in caveman-proxy: records argv, answers with the JSON shape the Go
// side prints, and fails the way fatalJSON does (stderr + exit 1).
function fakeProxy(home) {
  const bin = join(home, "bin", "fake-proxy");
  const log = join(home, "argv.log");
  writeFileSync(bin, `#!/bin/sh
printf '%s\\n' "$*" >> '${log}'
case "$2 $3 $4" in
  "experiment start distill"*) printf '%s\\n' '{"label":"distill","sink_id":"procedure_repeat:abc","fix_kind":"skill_distillation","created_at":"2026-09-01T00:00:00Z","arms":[{"arm":"on","started_at":"2026-09-01T00:00:00Z"}]}' ;;
  "experiment list"*) printf '%s\\n' '[]' ;;
  "experiment report nope") echo '{"level":"ERROR","msg":"command failed","error":"no experiment named \\"nope\\""}' >&2; exit 1 ;;
  "export"*) printf '%s\\n' '{"path":"/tmp/d.json","summary":"2 findings · 9 sessions · cave score 70 · nothing sent","digest":{"schema":"caveman.learn.digest.v1"}}' ;;
  "reconcile"*) printf '%s\\n' '{"schema":"caveman.learn.reconcile.v1","billed_tokens":8000,"measured_tokens":2000,"coverage_pct":25,"unattributed_tokens":6000,"models":[{"model":"claude-sonnet-4-6","billed_tokens":8000,"measured_tokens":2000,"coverage_pct":25}],"caveats":["Coverage is a token comparison."]}' ;;
  *) exit 9 ;;
esac
`);
  chmodSync(bin, 0o755);
  return { bin, log };
}

test("learn experiment/export/reconcile forward flags verbatim and render by default", { skip: process.platform === "win32" }, async () => {
  const isolated = isolatedCliEnv();
  try {
    const proxy = fakeProxy(isolated.home);
    const env = { ...isolated.env, CAVEMAN_PROXY_BIN: proxy.bin };

    let result = await runCli(["learn", "experiment", "start", "distill", "--sink", "procedure_repeat:abc", "--fix-kind", "skill_distillation"], { env });
    assert.equal(result.code, 0, result.stderr);
    assert.match(result.stdout, /distill\s+on since 2026-09-01T00:00:00Z/);
    assert.match(result.stdout, /id procedure_repeat:abc · skill_distillation/);

    result = await runCli(["learn", "experiment", "list"], { env });
    assert.match(result.stdout, /no experiments yet/);

    result = await runCli(["learn", "export", "--json"], { env });
    assert.equal(JSON.parse(result.stdout).digest.schema, "caveman.learn.digest.v1");
    result = await runCli(["learn", "export"], { env });
    assert.match(result.stdout, /digest written: \/tmp\/d\.json/);
    assert.match(result.stdout, /nothing sent/);

    result = await runCli(["learn", "reconcile", "--usage-export", "/tmp/usage.csv"], { env });
    assert.equal(result.code, 0, result.stderr);
    assert.match(result.stdout, /25\.0% of billed tokens seen locally/);
    assert.match(result.stdout, /claude-sonnet-4-6\s+billed/);
    assert.doesNotMatch(result.stdout, /\$/);

    result = await runCli(["learn", "experiment", "report", "nope"], { env });
    assert.equal(result.code, 1);
    assert.match(result.stderr, /no experiment named/);

    assert.deepEqual(readFileSync(proxy.log, "utf8").trim().split("\n"), [
      "learn experiment start distill --sink procedure_repeat:abc --fix-kind skill_distillation",
      "learn experiment list",
      "learn export --json",
      "learn export",
      "learn reconcile --usage-export /tmp/usage.csv",
      "learn experiment report nope",
    ]);
  } finally {
    isolated.cleanup();
  }
});
