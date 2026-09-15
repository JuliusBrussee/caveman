#!/usr/bin/env node
// Launcher for caveman-shrink. The tool itself is the prebuilt Go
// `caveman-shrink` binary (it links the engine's tool-schema compressor); this
// shim locates and execs it, forwarding args and stdio.
//
import { spawn } from "node:child_process";
import { ensureBinary } from "./binary-installer.generated.mjs";

let bin;
try {
  bin = await ensureBinary({ name: "caveman-shrink", envVar: "CAVEMAN_SHRINK_BIN" });
} catch (error) {
  process.stderr.write(`caveman-shrink: ${error.message}\n`);
  process.exit(127);
}
const child = spawn(bin, process.argv.slice(2), { stdio: "inherit" });

child.on("error", (err) => {
  if (err.code === "ENOENT") {
    process.stderr.write(`caveman-shrink: verified binary disappeared before launch (${bin})\n`);
    process.exit(127);
  }
  process.stderr.write(`caveman-shrink: ${err.message}\n`);
  process.exit(1);
});

child.on("exit", (code, signal) => {
  if (signal) process.kill(process.pid, signal);
  else process.exit(code ?? 0);
});
