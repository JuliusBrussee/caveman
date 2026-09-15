import test from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

function run(args, input, env) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, ...args], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => (stdout += chunk));
    child.stderr.on("data", (chunk) => (stderr += chunk));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
    child.stdin.end(input);
  });
}

test("compress catalog delegates stdin and arguments to caveman-shrink", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-catalog-"));
  const stub = join(dir, "caveman-shrink");
  const argvLog = join(dir, "argv.txt");
  writeFileSync(stub, `#!/bin/sh\nprintf '%s' "$*" > ${JSON.stringify(argvLog)}\ncat\n`, { mode: 0o755 });

  const catalog = '{"tools":[{"name":"search"}]}';
  const out = await run(
    ["tools", "compress", "catalog", "lint", "tools.json"],
    catalog,
    { ...process.env, CAVEMAN_SHRINK_BIN: stub },
  );

  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.stdout, catalog);
  assert.equal(readFileSync(argvLog, "utf8"), "lint tools.json");
});

test("compress catalog fails loudly when dedicated binary is missing", async () => {
  const out = await run(
    ["tools", "compress", "catalog"],
    "{}",
    { ...process.env, CAVEMAN_SHRINK_BIN: "caveman-shrink-does-not-exist" },
  );

  assert.equal(out.code, 1);
  assert.equal(out.stdout, "");
  assert.match(out.stderr, /cannot run caveman-shrink-does-not-exist/);
  assert.match(out.stderr, /caveman setup --install/);
});

test("compress catalog forwards caveman-shrink exit status", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-catalog-exit-"));
  const stub = join(dir, "caveman-shrink");
  writeFileSync(stub, "#!/bin/sh\nexit 7\n", { mode: 0o755 });

  const out = await run(
    ["tools", "compress", "catalog", "lint", "bad.json"],
    "",
    { ...process.env, CAVEMAN_SHRINK_BIN: stub },
  );
  assert.equal(out.code, 7);
});

test("tools discovery distinguishes command-output shrink from tool catalogs", async () => {
  const out = await run(["tools"], "", { ...process.env, NO_COLOR: "1" });
  assert.equal(out.code, 0, out.stderr);
  assert.match(out.stdout, /compress\s+compress stdin; use `compress catalog` for tool schemas/);
  assert.match(out.stdout, /shrink\s+run a command and keep output recoverable/);
});
