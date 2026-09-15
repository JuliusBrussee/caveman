import { test } from "node:test";
import assert from "node:assert/strict";

import { firstRunScanContract } from "../dist/index.js";

test("first-run child timeout is derived from both proxy scan budgets", () => {
  const contract = firstRunScanContract();
  assert.equal(contract.behaviorBudgetMS, 20_000);
  assert.equal(contract.retroBudgetMS, 60_000);
  assert.equal(contract.timeoutMarginMS, 10_000);
  assert.equal(contract.childTimeoutSeconds, 90, "first wrap must not grow past the accepted 90s cap");
  assert.equal(
    contract.childTimeoutSeconds * 1_000,
    contract.behaviorBudgetMS + contract.retroBudgetMS + contract.timeoutMarginMS,
  );
  assert.deepEqual(contract.proxyArgs, [
    "learn", "scan", "--retro",
    "--behavior-budget-ms", "20000",
    "--retro-budget-ms", "60000",
  ]);
});
