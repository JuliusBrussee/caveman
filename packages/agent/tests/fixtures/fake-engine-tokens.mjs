#!/usr/bin/env node
// Like fake-engine.mjs, but the compress report ALSO carries tokens_before /
// tokens_after — the engine's own count of the ORIGINAL and the COMPRESSED
// PAYLOAD ALONE. It reports a deliberately tiny tokens_after so a regression
// that trusts it (instead of measuring the wrapped bytes actually sent) would
// print a fictional reduction.
import { createHash } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

const [command, handle, contentType] = process.argv.slice(2);
const store = process.env.CAVE_FAKE_ENGINE_STORE;
if (!store) throw new Error("CAVE_FAKE_ENGINE_STORE missing");

if (command === "compress") {
  const chunks = [];
  for await (const chunk of process.stdin) chunks.push(chunk);
  const input = Buffer.concat(chunks);
  const digest = createHash("sha256").update(input).digest("hex");
  const recoveryHandle = `ccr_${digest}`;
  await writeFile(resolve(store, recoveryHandle), input);
  process.stdout.write("compressed summary");
  process.stderr.write(`${JSON.stringify({
    recovery_handle: recoveryHandle,
    method: "text",
    ratio: 0.8,
    tokens_before: Math.ceil(input.length / 4),
    // The compressed payload alone, omitting the wrapper the framework adds.
    tokens_after: 2,
  })}\n`);
} else if (command === "retrieve" && handle) {
  process.stdout.write(await readFile(resolve(store, handle)));
} else {
  process.exitCode = 2;
}
