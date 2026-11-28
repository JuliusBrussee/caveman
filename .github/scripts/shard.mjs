#!/usr/bin/env node
// Splits a list read from stdin (one item per line) into <total> shards and
// prints shard <index> (1-based), one item per line.
//
//   go list ./... | node .github/scripts/shard.mjs 1 2
//
// CI shards a suite across jobs with this, so it refuses to print anything
// unless every shard together is exactly the input: each item in exactly one
// shard, none dropped, none invented, no shard empty. The list comes from the
// real package or file list at run time, so a new package or test file lands
// in a shard without editing the workflow.
import { readFileSync } from "node:fs";

function fail(message) {
  process.stderr.write(`shard: ${message}\n`);
  process.exit(1);
}

const [index, total] = process.argv.slice(2).map((value) => Number.parseInt(value, 10));
if (!Number.isInteger(total) || total < 1 || !Number.isInteger(index) || index < 1 || index > total) {
  fail("usage: shard.mjs <index 1..total> <total> < items");
}

const items = readFileSync(0, "utf8")
  .split(/\r?\n/)
  .map((line) => line.trim())
  .filter(Boolean);
if (new Set(items).size !== items.length) fail("input has duplicate items");
if (items.length < total) fail(`${items.length} items cannot fill ${total} shards`);

// Sorted first, so every job computes the same partition from the same list.
const sorted = [...items].sort();
const shards = Array.from({ length: total }, () => []);
sorted.forEach((item, i) => shards[i % total].push(item));

const seen = new Map();
for (const [n, shard] of shards.entries()) {
  if (shard.length === 0) fail(`shard ${n + 1} is empty`);
  for (const item of shard) {
    if (seen.has(item)) fail(`${item} is in shards ${seen.get(item)} and ${n + 1}`);
    seen.set(item, n + 1);
  }
}
const missing = items.filter((item) => !seen.has(item));
if (missing.length || seen.size !== items.length) fail(`shards do not cover the input: missing ${missing.join(", ")}`);

process.stderr.write(`shard ${index}/${total}: ${shards[index - 1].length} of ${items.length} items\n`);
process.stdout.write(shards[index - 1].join("\n") + "\n");
