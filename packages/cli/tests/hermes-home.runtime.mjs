import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
} from "node:fs";
import { homedir, tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "node:test";
import { hermesHome } from "../dist/index.js";
import { nativeStub, stubEnv } from "./harness/stub-bin.mjs";

// Hermes 0.21.5 hermes_constants.py uses LOCALAPPDATA on Windows, strips
// HERMES_HOME, and expands env vars then a leading tilde in it
// (Path(expanduser(expandvars(value)))). 0.19.1 took the value literally.
test("Hermes home matches native Windows and POSIX default roots", () => {
  const local = join(homedir(), "synthetic local app data");
  assert.equal(hermesHome({}, "darwin"), join(homedir(), ".hermes"));
  assert.equal(
    hermesHome({ LOCALAPPDATA: local }, "win32"),
    join(local, "hermes"),
  );
  assert.equal(
    hermesHome({ LOCALAPPDATA: `  ${local}  ` }, "win32"),
    join(local, "hermes"),
  );
  assert.equal(
    hermesHome({}, "win32"),
    join(homedir(), "AppData", "Local", "hermes"),
  );
  assert.equal(
    hermesHome({ HERMES_HOME: "   ", LOCALAPPDATA: local }, "win32"),
    join(local, "hermes"),
  );
});

test("Hermes explicit home expands tilde and env vars and resolves relative paths", () => {
  for (const platform of ["darwin", "win32"]) {
    assert.equal(
      hermesHome({ HERMES_HOME: "  relative profile  " }, platform),
      resolve("relative profile"),
    );
    assert.equal(
      hermesHome({ HERMES_HOME: "~/literal-profile" }, platform),
      join(homedir(), "literal-profile"),
    );
    assert.equal(
      hermesHome({ HERMES_HOME: "$ROOT/a/${SUB}", ROOT: "/srv", SUB: "b" }, platform),
      resolve("/srv/a/b"),
    );
    assert.equal(
      hermesHome({ HERMES_HOME: "$UNSET_HERMES_VAR/p" }, platform),
      resolve("$UNSET_HERMES_VAR/p"),
    );
    assert.equal(
      hermesHome({ HERMES_HOME: "%ROOT%/p", ROOT: "/srv" }, platform),
      resolve(platform === "win32" ? "/srv/p" : "%ROOT%/p"),
    );
    assert.equal(
      hermesHome(
        { HERMES_HOME: "explicit", LOCALAPPDATA: "ignored" },
        platform,
      ),
      resolve("explicit"),
    );
  }
});

for (const override of [
  undefined,
  "  relative profile  ",
  "~/tilde-profile",
]) {
  test(`Hermes MCP install reaches the native config root: ${override ?? "platform default"}`, (t) => {
    const root = mkdtempSync(join(tmpdir(), "cave-hermes home "));
    t.after(() => rmSync(root, { recursive: true, force: true }));
    const home = join(root, "home"),
      bin = join(root, "bin"),
      local = join(home, "local app data");
    for (const path of [home, bin, local]) mkdirSync(path, { recursive: true });
    const mcp = nativeStub(
      bin,
      "caveman-mcp",
      `
if (ARGV[0] === "version") console.log(JSON.stringify({version:"test",capabilities:["mcp_recovery"]}));
`,
    );
    const env = stubEnv(
      {
        PATH: dirname(process.execPath),
        ...(process.env.SystemRoot
          ? { SystemRoot: process.env.SystemRoot }
          : {}),
        ...(process.env.PATHEXT ? { PATHEXT: process.env.PATHEXT } : {}),
        HOME: home,
        USERPROFILE: home,
        LOCALAPPDATA: local,
        APPDATA: join(home, "roaming"),
        ...(override === undefined ? {} : { HERMES_HOME: override }),
        CAVEMAN_HOME: join(home, ".caveman"),
        CAVEMAN_MCP_BIN: mcp,
        CAVEMAN_OFFLINE: "1",
        CAVEMAN_TELEMETRY: "0",
        CAVE_NO_KEYCHAIN: "1",
        NO_COLOR: "1",
      },
      bin,
    );
    const cli = fileURLToPath(new URL("../dist/index.js", import.meta.url));
    const out = spawnSync(process.execPath, [cli, "mcp", "install", "hermes"], {
      env,
      cwd: root,
      encoding: "utf8",
      timeout: 20_000,
    });
    assert.equal(out.status, 0, out.stderr);
    const expected =
      override?.startsWith("~/")
        ? join(home, override.slice(2))
        : override !== undefined
        ? resolve(root, override.trim())
        : process.platform === "win32"
          ? join(local, "hermes")
          : join(home, ".hermes");
    assert.match(
      readFileSync(join(expected, "config.yaml"), "utf8"),
      /mcp_servers:/,
    );
    if (override?.startsWith("~"))
      assert.equal(existsSync(join(root, "~")), false);
  });
}
