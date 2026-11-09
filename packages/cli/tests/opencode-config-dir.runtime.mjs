import { test } from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { isolatedEnv } from "../../../tests/installer/_isolated-env.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../../..");
const cli = join(root, "packages/cli/dist/index.js");
const installer = join(root, "installer/install.js");
const cases = [
  { name: "ordinary", value: (home) => join(home, "selected config"), selected: true },
  { name: "trailing spaces", value: (home) => `${join(home, "selected")}  `, selected: true, posix: true },
  { name: "leading spaces", value: () => "  selected/config", selected: true },
  { name: "empty", value: () => "" },
  { name: "whitespace only", value: () => " \t " },
  { name: "unset", value: () => undefined },
];

function fixture(input, xdg) {
  const home = mkdtempSync(join(tmpdir(), "cave-oc-path-"));
  const bin = join(home, "bin");
  mkdirSync(bin);
  if (process.platform === "win32") {
    writeFileSync(join(bin, "opencode.cmd"), "@echo off\r\necho opencode 2.2.5\r\n");
  } else {
    writeFileSync(join(bin, "opencode"), "#!/bin/sh\necho 'opencode 2.2.5'\n", { mode: 0o755 });
  }
  const env = { ...isolatedEnv(home, [bin]), CAVEMAN_DEFAULT_MODE: "caveman" };
  const value = input.value(home);
  if (value === undefined) delete env.OPENCODE_CONFIG_DIR;
  else env.OPENCODE_CONFIG_DIR = value;
  if (xdg) env.XDG_CONFIG_HOME = join(home, "xdg config");
  else delete env.XDG_CONFIG_HOME;
  const fallback = xdg ? join(env.XDG_CONFIG_HOME, "opencode") : join(home, ".config", "opencode");
  const expected = input.selected ? resolve(home, value) : fallback;
  const wrong = new Set([fallback, join(home, ".config", "opencode")]);
  if (input.selected) wrong.add(resolve(home, value.trim()));
  wrong.delete(expected);
  for (const directory of wrong) {
    mkdirSync(directory, { recursive: true });
    writeFileSync(join(directory, "user.txt"), "untouched\n");
  }
  return { home, env, expected, wrong };
}

function run(fx, args, env = fx.env) {
  const result = spawnSync(process.execPath, args, {
    cwd: fx.home, env, encoding: "utf8", timeout: 30_000,
  });
  assert.equal(result.error, undefined, result.error?.message);
  assert.equal(result.status, 0, `${result.stdout}\n${result.stderr}`);
  return result;
}

function install(fx, env = fx.env) {
  run(fx, [installer, "--only", "opencode", "--non-interactive", "--no-mcp-shrink",
    "--config-dir", join(fx.home, ".claude")], env);
}

function assertWrongRootsUntouched(fx) {
  for (const directory of fx.wrong) {
    assert.deepEqual(readdirSync(directory), ["user.txt"], `unexpected writes under ${JSON.stringify(directory)}`);
    assert.equal(readFileSync(join(directory, "user.txt"), "utf8"), "untouched\n");
  }
}

for (const input of cases) {
  for (const xdg of [true, false]) {
    const options = { skip: input.posix && process.platform === "win32" ? "Win32 normalizes trailing-space filenames" : false };
    const label = `#1485 ${input.name}, ${xdg ? "XDG" : "home fallback"}`;

    test(`${label}: CLI install/uninstall`, options, () => {
      const fx = fixture(input, xdg);
      try {
        run(fx, [cli, "tools", "hooks", "install", "opencode"]);
        const plugin = join(fx.expected, "plugins", "caveman-shrink.js");
        assert.ok(existsSync(plugin), `missing CLI plugin under ${JSON.stringify(fx.expected)}`);
        assertWrongRootsUntouched(fx);
        run(fx, [cli, "tools", "hooks", "uninstall", "opencode"]);
        assert.ok(!existsSync(plugin), "CLI uninstall must use the same selected root");
        assertWrongRootsUntouched(fx);
      } finally { rmSync(fx.home, { recursive: true, force: true }); }
    });

    test(`${label}: unified installer/uninstaller`, options, () => {
      const fx = fixture(input, xdg);
      try {
        install(fx);
        const plugin = join(fx.expected, "plugins", "caveman", "plugin.js");
        assert.ok(existsSync(plugin), `missing installed plugin under ${JSON.stringify(fx.expected)}`);
        assertWrongRootsUntouched(fx);
        writeFileSync(join(fx.expected, "user.txt"), "keep\n");
        run(fx, [installer, "--uninstall", "--non-interactive", "--config-dir", join(fx.home, ".claude")]);
        assert.ok(!existsSync(plugin), "uninstall must remove the selected payload");
        assert.equal(readFileSync(join(fx.expected, "user.txt"), "utf8"), "keep\n");
        assertWrongRootsUntouched(fx);
      } finally { rmSync(fx.home, { recursive: true, force: true }); }
    });

    test(`${label}: copied mode plugin`, options, () => {
      const fx = fixture(input, xdg);
      try {
        // Bootstrap the copied payload through an ordinary path so this test
        // reaches the plugin even when the installer's path handling is broken.
        const payload = join(fx.home, "payload");
        install(fx, { ...fx.env, OPENCODE_CONFIG_DIR: payload });
        const plugin = pathToFileURL(join(payload, "plugins", "caveman", "plugin.js")).href;
        run(fx, ["--input-type=module", "-e", `
          const mod = await import(process.argv[1]);
          const hooks = await (mod.default || mod.CavemanPlugin)({});
          await hooks['chat.message']({}, { parts: [{ type: 'text', text: '/caveman ultra' }] });
        `, plugin]);
        assert.equal(readFileSync(join(fx.expected, ".caveman-active"), "utf8"), "ultracave");
        assertWrongRootsUntouched(fx);
      } finally { rmSync(fx.home, { recursive: true, force: true }); }
    });
  }
}
