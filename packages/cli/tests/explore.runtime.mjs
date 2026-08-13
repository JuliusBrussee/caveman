import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { mkdtempSync, readFileSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const cli = join(here, "..", "dist", "index.js");
const packageParent = join(here, "..", "..");
const canonical = [
  join(packageParent, "skills", "caveman-explore", "SKILL.md"),
  join(packageParent, "..", "skills", "caveman-explore", "SKILL.md"),
].find((candidate) => existsSync(candidate));
assert.ok(canonical, "canonical caveman-explore skill must exist beside public/ or packages/");

function run(extraArgs, extraEnv = {}) {
  const env = { ...process.env, NO_COLOR: "1", ...extraEnv };
  delete env.CAVE_GATEWAY_URL;
  return new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "explore", ...extraArgs], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

// `caveman explore` survives as an unprinted legacy alias for installing the
// canonical caveman-explore skill.
test("explore install writes caveman-explore SKILL.md matching canonical artifact", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-explore-"));
  const out = await run(["install", "--dir", dir]);
  assert.equal(out.code, 0, `explore install exited ${out.code}: ${out.stderr}`);
  const dest = join(dir, "SKILL.md");
  assert.ok(existsSync(dest), "explore install must write SKILL.md");
  const written = readFileSync(dest, "utf8");
  assert.match(written, /^---\nname: caveman-explore\n/, "must write valid skill frontmatter");
  assert.match(written, /model: haiku/, "explorer must run on the cheap model");
  assert.match(written, /path\/to\/file\.ext:START-END/, "must carry the compact citation contract");
  assert.equal(
    written,
    readFileSync(canonical, "utf8"),
    "legacy alias and canonical caveman-explore skill must remain byte-identical",
  );
});

// Codex must fail closed: its transcript isolation is unproven, so we do not ship a
// half-isolated explorer under the FastContext name.
test("explore install --agent codex fails closed (non-zero, no file)", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-explore-codex-"));
  const out = await run(["install", "--agent", "codex", "--dir", dir]);
  assert.notEqual(out.code, 0, "codex must not be installed yet");
  assert.match(
    out.stderr,
    /caveman explore: only --agent claude is wired today \(codex needs verified transcript isolation first\)\. got: codex/,
  );
  assert.ok(!existsSync(join(dir, "SKILL.md")), "no skill file may be written for unsupported agent");
});

// `cave explore` with no/unknown subcommand is a usage error (non-zero), never a hang.
test("explore with no subcommand prints usage and exits non-zero", async () => {
  const out = await run([]);
  assert.notEqual(out.code, 0, "bare explore must exit non-zero");
  assert.match(out.stderr, /usage: caveman explore install/);
});
