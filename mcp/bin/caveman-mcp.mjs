#!/usr/bin/env node
// Launcher for the caveman-mcp server. The server itself is the prebuilt Go
// `caveman-mcp` binary (it links the engine in-process); this shim only locates
// and execs it, forwarding stdio so the MCP host talks to the binary directly.
//
import { spawn } from "node:child_process";
import { existsSync, realpathSync } from "node:fs";
import { basename, delimiter, dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { ensureBinary } from "./binary-installer.generated.mjs";

let bin;
const launcher = realpathSync(fileURLToPath(import.meta.url));
const explicit = process.env.CAVEMAN_MCP_BIN;
if (explicit && existsSync(explicit) && realpathSync(explicit) === launcher) {
  process.stderr.write(`caveman-mcp: CAVEMAN_MCP_BIN must point to the Go binary, not the npm launcher\n`);
  process.exit(127);
}
const originalPath = process.env.PATH;
process.env.PATH = withoutOwnNpmBin(originalPath);
try {
  bin = await ensureBinary({ name: "caveman-mcp", envVar: "CAVEMAN_MCP_BIN" });
} catch (error) {
  process.stderr.write(`caveman-mcp: ${error.message}\n`);
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
      return realpathSync(resolve(absolute, "caveman-mcp")) !== launcher;
    } catch {
      return true;
    }
  }).join(delimiter);
}

child.on("error", (err) => {
  if (err.code === "ENOENT") {
    process.stderr.write(`caveman-mcp: verified binary disappeared before launch (${bin})\n`);
    process.exit(127);
  }
  process.stderr.write(`caveman-mcp: ${err.message}\n`);
  process.exit(1);
});

child.on("exit", (code, signal) => {
  if (signal) process.kill(process.pid, signal);
  else process.exit(code ?? 0);
});
