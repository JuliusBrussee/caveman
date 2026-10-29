import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { mkdtempSync, writeFileSync, chmodSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

// A fake `cavemem` binary so the test never depends on a built Go core: it echoes
// canned JSON per subcommand (and the raw original for `recover`).
function fakeCavemem() {
  const dir = mkdtempSync(join(tmpdir(), "cave-cavemem-"));
  const bin = join(dir, "cavemem");
  writeFileSync(
    bin,
    `#!/usr/bin/env bash
case "$1" in
  remember) echo '{"id":"mem_abc","created_at":"2026-06-26T00:00:00Z","basis":"inferred"}';;
  recall)   echo "{\\"hits\\":[{\\"id\\":\\"mem_abc\\",\\"text\\":\\"compact\\",\\"score\\":0.5,\\"tokens_added\\":12,\\"basis\\":\\"inferred\\",\\"recovery_handle\\":\\"ccr_xyz\\"}],\\"basis\\":\\"inferred\\",\\"limit\\":\\"\${3:-}\\"}";;
  supersede) echo "{\\"id\\":\\"mem_new\\",\\"supersedes\\":\\"\${2:-}\\",\\"basis\\":\\"inferred\\"}";;
  history)  echo '{"history":[{"id":"mem_abc","valid_until":"2026-07-29T00:00:00Z","superseded_by":"mem_new"},{"id":"mem_new","supersedes":"mem_abc"}],"basis":"inferred"}';;
  forget)   echo '{"forgotten":true}';;
  recover)  printf 'BYTE EXACT ORIGINAL';;
  *) echo "unknown cavemem subcommand: $1" 1>&2; exit 2;;
esac
`,
    { mode: 0o755 },
  );
  chmodSync(bin, 0o755);
  return bin;
}

// A fake cavemem that echoes the free-text argument (remember's text, or
// supersede's replacement), so a test can assert it survived argument parsing
// verbatim.
function echoCavemem() {
  const dir = mkdtempSync(join(tmpdir(), "cave-cavemem-echo-"));
  const bin = join(dir, "cavemem");
  writeFileSync(
    bin,
    `#!/usr/bin/env bash\ncase "$1" in\n  remember) printf '%s' "$2";;\n  supersede) printf '%s' "$3";;\n  *) echo "unknown" 1>&2; exit 2;;\nesac\n`,
    { mode: 0o755 },
  );
  chmodSync(bin, 0o755);
  return bin;
}

// A fake cavemem whose `remember --stdin` / `supersede <id> --stdin` echoes what
// it read on stdin, so a test can assert the text never rode on argv.
function stdinEchoCavemem() {
  const dir = mkdtempSync(join(tmpdir(), "cave-cavemem-stdin-"));
  const bin = join(dir, "cavemem");
  writeFileSync(
    bin,
    `#!/usr/bin/env bash\nif [ "$1 $2" = "remember --stdin" ] && [ "$#" -eq 2 ]; then cat; elif [ "$1 $3" = "supersede --stdin" ] && [ "$#" -eq 3 ]; then cat; else echo "unexpected: $*" 1>&2; exit 2; fi\n`,
    { mode: 0o755 },
  );
  chmodSync(bin, 0o755);
  return bin;
}

function runCli(argv, env, input = "") {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, ...argv], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
    // A CLI that rejects its input closes stdin before we finish writing; that
    // EPIPE is the symptom under test, so let the assertions report it.
    child.stdin.on("error", () => {});
    child.stdin.end(input);
  });
}

test("mem remember shells through to cavemem", async () => {
  const env = { ...process.env, CAVEMEM_BIN: fakeCavemem() };
  const out = await runCli(["mem", "remember", "the billing service owns gainshare math"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(JSON.parse(out.stdout).id, "mem_abc");
});

test("mem remember -- keeps a block that opens with a --- rule (issue #141)", async () => {
  const env = { ...process.env, CAVEMEM_BIN: echoCavemem() };
  const block = "---\nAlways cite sources.\n---";
  const out = await runCli(["mem", "remember", "--", block], env);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.stdout, block, "the leading --- block must reach cavemem verbatim");
});

test("mem remember --stdin passes the block to cavemem byte for byte", async () => {
  const env = { ...process.env, CAVEMEM_BIN: stdinEchoCavemem() };
  // Backticks, a variable, quotes and a leading --- rule: everything a shell
  // command line would run, expand or cut short. No trailing newline.
  const block = '---\nBefore committing run `make clean` and check "$HOME/.config" !\n---';
  const out = await runCli(["mem", "remember", "--stdin"], env, block);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.stdout, block, "stdin must reach cavemem unchanged");
});

test("mem remember without -- still drops flag-looking args (back-compat)", async () => {
  const env = { ...process.env, CAVEMEM_BIN: echoCavemem() };
  const out = await runCli(["mem", "remember", "keep this", "--flag"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.stdout, "keep this");
});

test("mem recall passes --limit through and re-emits hits", async () => {
  const env = { ...process.env, CAVEMEM_BIN: fakeCavemem() };
  const out = await runCli(["mem", "recall", "billing", "--limit", "3"], env);
  assert.equal(out.code, 0, out.stderr);
  const o = JSON.parse(out.stdout);
  assert.equal(o.hits[0].recovery_handle, "ccr_xyz");
  assert.equal(o.limit, "3", "limit must be forwarded as a positional to cavemem");
});

test("mem recall without a query errors honestly", async () => {
  const env = { ...process.env, CAVEMEM_BIN: fakeCavemem() };
  const out = await runCli(["mem", "recall", "--limit", "3"], env);
  assert.equal(out.code, 2);
  assert.match(out.stderr, /usage: caveman mem recall/);
});

test("mem supersede -- keeps a replacement block that opens with a --- rule (issue #141)", async () => {
  const env = { ...process.env, CAVEMEM_BIN: echoCavemem() };
  const block = "---\nCorrected: cite the ledger.\n---";
  const out = await runCli(["mem", "supersede", "mem_abc", "--", block], env);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.stdout, block, "the leading --- replacement block must reach cavemem verbatim");
});

test("mem supersede --stdin passes the replacement to cavemem byte for byte (issue #1385)", async () => {
  const env = { ...process.env, CAVEMEM_BIN: stdinEchoCavemem() };
  // Larger than one Linux argv element (MAX_ARG_STRLEN, ~128 KB): this
  // replacement cannot ride on the command line at all.
  const block = `fact: ${"y".repeat(200 * 1024)}`;
  const out = await runCli(["mem", "supersede", "mem_abc", "--stdin"], env, block);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.stdout, block, "stdin must reach cavemem unchanged");
});

test("mem forget shells through", async () => {
  const env = { ...process.env, CAVEMEM_BIN: fakeCavemem() };
  const out = await runCli(["mem", "forget", "mem_abc"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(JSON.parse(out.stdout).forgotten, true);
});

test("mem supersede and history shell through", async () => {
  const env = { ...process.env, CAVEMEM_BIN: fakeCavemem() };
  const replaced = await runCli(["mem", "supersede", "mem_abc", "corrected deployment fact"], env);
  assert.equal(replaced.code, 0, replaced.stderr);
  assert.equal(JSON.parse(replaced.stdout).supersedes, "mem_abc");

  const history = await runCli(["mem", "history", "mem_new"], env);
  assert.equal(history.code, 0, history.stderr);
  assert.deepEqual(JSON.parse(history.stdout).history.map((m) => m.id), ["mem_abc", "mem_new"]);
});

test("mem supersede requires id and replacement text", async () => {
  const env = { ...process.env, CAVEMEM_BIN: fakeCavemem() };
  const out = await runCli(["mem", "supersede", "mem_abc"], env);
  assert.equal(out.code, 2);
  assert.match(out.stderr, /usage: caveman mem supersede/);
});

test("mem recover writes the byte-exact original (no JSON wrapper)", async () => {
  const env = { ...process.env, CAVEMEM_BIN: fakeCavemem() };
  const out = await runCli(["mem", "recover", "ccr_xyz"], env);
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.stdout, "BYTE EXACT ORIGINAL");
});

test("mem errors honestly when cavemem is missing", async () => {
  const env = { ...process.env, CAVEMEM_BIN: join(tmpdir(), "definitely-not-cavemem-xyz") };
  const out = await runCli(["mem", "remember", "x"], env);
  assert.notEqual(out.code, 0);
  assert.match(out.stderr, /cavemem not found/);
});

test("mem missing-binary path never falls back to npx", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-cavemem-missing-"));
  const env = { ...process.env, CAVEMAN_HOME: home, PATH: "" };
  delete env.CAVEMEM_BIN;
  const out = await runCli(["mem", "recall", "billing"], env);
  assert.notEqual(out.code, 0);
  assert.match(out.stderr, /cavemem not found/);
  assert.doesNotMatch(`${out.stdout}${out.stderr}`, /\bnpx\b/i);
});

test("bare mem prints usage", async () => {
  const out = await runCli(["mem"], process.env);
  assert.equal(out.code, 0, out.stderr);
  assert.match(out.stdout, /caveman mem recall/);
});
