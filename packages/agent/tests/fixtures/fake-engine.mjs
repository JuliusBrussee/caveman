#!/usr/bin/env node
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
  if (handle === "--type" && contentType === "toolschema") {
    const parsed = JSON.parse(input.toString("utf8"));
    parsed.description = String(parsed.description).split(".")[0] + ".";
    if (parsed.input && typeof parsed.input === "object") {
      delete parsed.input.title;
      delete parsed.input.default;
    }
    process.stdout.write(JSON.stringify(parsed));
  } else {
    process.stdout.write("compressed summary");
  }
  process.stderr.write(`${JSON.stringify({
    recovery_handle: recoveryHandle,
    method: "text",
    ratio: 0.8,
  })}\n`);
} else if (command === "retrieve" && handle) {
  process.stdout.write(await readFile(resolve(store, handle)));
} else {
  process.exitCode = 2;
}
