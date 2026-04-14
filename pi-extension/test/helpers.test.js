import assert from "node:assert/strict";
import { existsSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  filterSkillBodyForMode,
  parseCavemanCommand,
  readDefaultMode,
  resolveSessionMode,
  writeDefaultMode,
} from "../index.js";

test("parseCavemanCommand falls back to full when invoked bare and default is off", () => {
  assert.deepEqual(parseCavemanCommand("", "off"), {
    type: "set-mode",
    mode: "full",
  });
});

test("parseCavemanCommand parses mode, status, and default subcommands", () => {
  assert.deepEqual(parseCavemanCommand("wenyan-ultra", "full"), {
    type: "set-mode",
    mode: "wenyan-ultra",
  });
  assert.deepEqual(parseCavemanCommand("status", "full"), {
    type: "status",
  });
  assert.deepEqual(parseCavemanCommand("default ultra", "full"), {
    type: "set-default",
    mode: "ultra",
  });
});

test("resolveSessionMode prefers the latest persisted session mode over config default", () => {
  const entries = [
    {
      type: "custom",
      customType: "caveman-mode",
      data: { mode: "full" },
    },
    {
      type: "custom",
      customType: "caveman-mode",
      data: { mode: "lite" },
    },
  ];

  assert.equal(resolveSessionMode(entries, "ultra"), "lite");
  assert.equal(resolveSessionMode([], "ultra"), "ultra");
});

test("filterSkillBodyForMode keeps only the active intensity row and example lines", () => {
  const body = [
    "## Intensity",
    "",
    "| Level | What change |",
    "|-------|------------|",
    "| **lite** | lite row |",
    "| **full** | full row |",
    "| **ultra** | ultra row |",
    "",
    "Example:",
    "- lite: lite example",
    "- full: full example",
    "- ultra: ultra example",
  ].join("\n");

  const filtered = filterSkillBodyForMode(body, "ultra");
  assert.ok(filtered.includes("| **ultra** | ultra row |"));
  assert.ok(!filtered.includes("| **lite** | lite row |"));
  assert.ok(!filtered.includes("- full: full example"));
  assert.ok(filtered.includes("- ultra: ultra example"));
});

test("readDefaultMode and writeDefaultMode round-trip via config file", () => {
  const tempDir = mkdtempSync(join(tmpdir(), "caveman-config-"));
  const configPath = join(tempDir, "config.json");

  try {
    writeDefaultMode("wenyan-lite", configPath);
    assert.equal(existsSync(configPath), true);
    assert.equal(readDefaultMode({ configPath }), "wenyan-lite");

    const payload = JSON.parse(readFileSync(configPath, "utf8"));
    assert.equal(payload.defaultMode, "wenyan-lite");
  } finally {
    rmSync(tempDir, { recursive: true, force: true });
  }
});
