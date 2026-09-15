import { test } from "node:test";
import assert from "node:assert/strict";

import { parseArgs } from "../run.mjs";

// Collect errors instead of exiting so bad input is testable.
const parse = (argv) => {
  const errors = [];
  const args = parseArgs(argv, (m) => { errors.push(m); throw new Error(m); });
  return { args, errors };
};
const parseExpectingError = (argv) => {
  try {
    parse(argv);
    return null;
  } catch (err) {
    return err.message;
  }
};

test("defaults are sane", () => {
  const { args } = parse([]);
  assert.equal(args.timeout, 90);
  assert.equal(args.grace, 3);
  assert.equal(args.repeat, 1);
  assert.equal(args.harness, null);
  assert.equal(args.isolate, false);
});

test("valued flags reject a missing value instead of hanging or measuring everything", () => {
  assert.match(parseExpectingError(["--timeout"]), /--timeout needs a value/);
  assert.match(parseExpectingError(["--harness"]), /--harness needs a value/);
  assert.match(parseExpectingError(["--out"]), /--out needs a value/);
  assert.match(parseExpectingError(["--ratio"]), /--ratio needs a value/);
  assert.match(parseExpectingError(["--grace"]), /--grace needs a value/);
  // a following flag is not a value
  assert.match(parseExpectingError(["--timeout", "--json"]), /--timeout needs a value/);
});

test("numeric flags reject non-numbers and non-positives up front, not after every harness ran", () => {
  assert.match(parseExpectingError(["--ratio", "abc"]), /--ratio needs a positive number/);
  assert.match(parseExpectingError(["--timeout", "0"]), /--timeout needs a positive number/);
  assert.match(parseExpectingError(["--timeout", "-5"]), /--timeout needs a positive number/);
  assert.match(parseExpectingError(["--grace", "NaN"]), /--grace needs a positive number/);
});

test("unknown flags fail closed", () => {
  assert.match(parseExpectingError(["--nope"]), /unknown flag/);
});

test("harness list parses and empty list is rejected", () => {
  const { args } = parse(["--harness", "claude, pi"]);
  assert.deepEqual(args.harness, ["claude", "pi"]);
  assert.match(parseExpectingError(["--harness", ",,"]), /at least one harness/);
});

test("repeat floors to an integer", () => {
  assert.equal(parse(["--repeat", "3.7"]).args.repeat, 3);
});
