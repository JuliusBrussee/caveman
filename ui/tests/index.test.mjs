import assert from "node:assert/strict";
import test from "node:test";

import { cx, surface } from "../dist/index.js";

test("cx preserves class order while removing only false and nullish values", () => {
  assert.equal(cx("base", false, "active", null, undefined, "0", ""), "base active 0");
});

test("cx returns an empty class name when every candidate is absent", () => {
  assert.equal(cx(false, null, undefined), "");
});

test("surface keeps light and dark border and background contracts", () => {
  for (const token of [
    "border",
    "border-black/10",
    "bg-white/80",
    "shadow-sm",
    "dark:border-white/10",
    "dark:bg-white/5",
  ]) {
    assert.ok(surface.split(" ").includes(token), `surface is missing ${token}`);
  }
});
