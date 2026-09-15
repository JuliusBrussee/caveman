#!/usr/bin/env node
// Launcher for caveman-browse. The driver itself is the prebuilt Go
// `caveman-browse` binary; this shim only locates and execs it, forwarding stdio
// for MCP hosts and CLI direct subcommands.
//
import { spawn } from "node:child_process";
import { existsSync, realpathSync } from "node:fs";
import { basename, delimiter, dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { ensureBinary } from "./binary-installer.generated.mjs";

let bin;
const launcher = realpathSync(fileURLToPath(import.meta.url));
const explicit = process.env.CAVEMAN_BROWSE_BIN;
if (explicit && existsSync(explicit) && realpathSync(explicit) === launcher) {
  process.stderr.write(`caveman-browse: CAVEMAN_BROWSE_BIN must point to the Go binary, not the npm launcher\n`);
  process.exit(127);
}
const originalPath = process.env.PATH;
process.env.PATH = withoutOwnNpmBin(originalPath);
try {
  bin = await ensureBinary({ name: "caveman-browse", envVar: "CAVEMAN_BROWSE_BIN" });
} catch (error) {
  process.stderr.write(`caveman-browse: ${error.message}\n`);
  process.exit(127);
} finally {
  if (originalPath === undefined) delete process.env.PATH;
  else process.env.PATH = originalPath;
}
const child = spawn(bin, process.argv.slice(2), { stdio: "inherit" });

function withoutOwnNpmBin(value = "") {
  return value.split(delimiter).filter((directory) => {
    if (!directory) return false;
    const absolute = resolve(directory);
    if (basename(absolute).toLowerCase() === ".bin" &&
        basename(dirname(absolute)).toLowerCase() === "node_modules") return false;
    try {
      return realpathSync(resolve(absolute, "caveman-browse")) !== launcher;
    } catch {
      return true;
    }
  }).join(delimiter);
}

child.on("error", (err) => {
  if (err.code === "ENOENT") {
    process.stderr.write(`caveman-browse: verified binary disappeared before launch (${bin})\n`);
    process.exit(127);
  }
  process.stderr.write(`caveman-browse: ${err.message}\n`);
  process.exit(1);
});

child.on("exit", (code, signal) => {
  if (signal) process.kill(process.pid, signal);
  else process.exit(code ?? 0);
});
