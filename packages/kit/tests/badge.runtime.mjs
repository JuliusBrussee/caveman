/**
 * Runtime guards for the React surfaces, rendered with react-dom/server. Run:
 *   node --test public/kit/tests/badge.runtime.mjs
 * Imports compiled dist (the package "test" script builds first).
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";

import { OptimizedByCaveman, BasisBadge } from "../dist/index.js";

test("OptimizedByCaveman renders the locked mark and canonical link", () => {
  const html = renderToStaticMarkup(React.createElement(OptimizedByCaveman, {}));
  assert.match(html, /Optimized by Caveman/);
  assert.match(html, /caveman\.so/);
  assert.match(html, /data-caveman-mark="optimized"/);
});

test("the mark cannot be rebranded via extra props", () => {
  // A host that tries to override the wordmark or link gets neither.
  const html = renderToStaticMarkup(
    React.createElement(OptimizedByCaveman, { label: "Acme Compression", href: "https://evil.example" }),
  );
  assert.match(html, /Optimized by Caveman/);
  assert.doesNotMatch(html, /Acme/);
  assert.doesNotMatch(html, /evil\.example/);
});

test("BasisBadge shows Verified only for a genuine verified basis", () => {
  const html = renderToStaticMarkup(React.createElement(BasisBadge, { basis: "verified" }));
  assert.match(html, /Verified/);
  assert.match(html, /data-caveman-basis="verified"/);
});

test("BasisBadge fails closed on an unknown basis crossing the boundary", () => {
  const html = renderToStaticMarkup(React.createElement(BasisBadge, { basis: "totally-made-up" }));
  assert.match(html, /Inferred/);
  assert.match(html, /data-caveman-basis="unverified"/);
  assert.doesNotMatch(html, /Verified/);
});
