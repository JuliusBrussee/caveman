#!/usr/bin/env node
// Dumps the environment it was started with, so a test can prove the engine
// subprocess receives an ALLOW-LISTED env, not the parent's full process.env
// with its provider/account secrets.
import { createHash } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

const [command, handle] = process.argv.slice(2);
const store = process.env.CAVE_FAKE_ENGINE_STORE;
if (!store) throw new Error("CAVE_FAKE_ENGINE_STORE missing");

if (command === "compress") {
  const chunks = [];
  for await (const chunk of process.stdin) chunks.push(chunk);
  const input = Buffer.concat(chunks);
  await writeFile(resolve(store, "engine-env.json"), JSON.stringify(process.env));
  const digest = createHash("sha256").update(input).digest("hex");
  const recoveryHandle = `ccr_${digest}`;
  await writeFile(resolve(store, recoveryHandle), input);
  process.stdout.write("compressed summary");
  process.stderr.write(`${JSON.stringify({ recovery_handle: recoveryHandle, method: "text", ratio: 0.8 })}\n`);
} else if (command === "retrieve" && handle) {
  process.stdout.write(await readFile(resolve(store, handle)));
} else {
  process.exitCode = 2;
}
