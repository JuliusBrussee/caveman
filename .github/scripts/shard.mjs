#!/usr/bin/env node
// Splits a list read from stdin (one item per line) into <total> shards and
// prints shard <index> (1-based), one item per line.
//
//   go list ./... | node .github/scripts/shard.mjs 1 2
//   ... | node .github/scripts/shard.mjs 1 2 --first slow/pkg/a,slow/pkg/b
//
// Items are dealt round-robin in sorted order. --first deals the named items
// before the rest, so known-slow items land in different shards; each must be
// in the input, so a renamed item fails here instead of silently clumping.
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

const args = process.argv.slice(2);
let first = [];
const at = args.indexOf("--first");
if (at !== -1) {
  first = (args[at + 1] ?? "").split(",").map((item) => item.trim()).filter(Boolean);
  args.splice(at, 2);
}
const [index, total] = args.map((value) => Number.parseInt(value, 10));
if (args.length !== 2 || !Number.isInteger(total) || total < 1 || !Number.isInteger(index) || index < 1 || index > total) {
  fail("usage: shard.mjs <index 1..total> <total> [--first a,b] < items");
}

const items = readFileSync(0, "utf8")
  .split(/\r?\n/)
  .map((line) => line.trim())
  .filter(Boolean);
if (new Set(items).size !== items.length) fail("input has duplicate items");
if (items.length < total) fail(`${items.length} items cannot fill ${total} shards`);

const unknown = first.filter((item) => !items.includes(item));
if (unknown.length) fail(`--first names items not in the input: ${unknown.join(", ")}`);

// Sorted, so every job computes the same partition from the same list.
const order = [...new Set(first), ...[...items].filter((item) => !first.includes(item)).sort()];
const shards = Array.from({ length: total }, () => []);
order.forEach((item, i) => shards[i % total].push(item));

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
