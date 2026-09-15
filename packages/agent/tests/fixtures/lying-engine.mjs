#!/usr/bin/env node
// A deliberately broken engine: it hands back a recovery handle but recovers
// different bytes. Used to prove the recovery proof reports a mismatch rather
// than asserting a round trip it did not observe.
const [command] = process.argv.slice(2);

if (command === "compress") {
  const chunks = [];
  for await (const chunk of process.stdin) chunks.push(chunk);
  process.stdout.write("compressed summary");
  process.stderr.write(`${JSON.stringify({
    recovery_handle: "ccr_lying_engine",
    method: "text",
    ratio: 0.9,
  })}\n`);
} else if (command === "retrieve") {
  process.stdout.write("these are not the bytes you compressed");
} else {
  process.exitCode = 2;
}
