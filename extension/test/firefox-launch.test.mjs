import assert from "node:assert/strict";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { webExtInvocation } from "../scripts/firefox-e2e.mjs";

test("Firefox runner passes paths with spaces and shell characters as literal argv", () => {
  const work = mkdtempSync(join(tmpdir(), "caveman first last & launcher-"));
  try {
    const cli = join(work, "fake web-ext.mjs");
    writeFileSync(cli, "process.stdout.write(JSON.stringify(process.argv.slice(2)));\n");
    const source = "C:\\Users\\First Last\\AppData\\Local\\Temp\\caveman & audit\\scratch";
    const firefox = "C:\\Program Files\\Mozilla Firefox\\firefox.exe";
    const invocation = webExtInvocation(cli, source, firefox, 43210);
    assert.equal(invocation.options.shell, false);
    const result = spawnSync(invocation.command, invocation.args, { ...invocation.options, encoding: "utf8" });
    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual(JSON.parse(result.stdout), ["run", "--source-dir", source, "--firefox", firefox,
      "--start-url", "about:blank", "--no-reload", "--verbose", "--args=--remote-debugging-port=43210"]);
  } finally {
    rmSync(work, { recursive: true, force: true });
  }
});
