import assert from "node:assert/strict";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import cavemanExtension from "../index.js";

function createPiHarness() {
  const events = new Map();
  const commands = new Map();
  const appendedEntries = [];
  const sentUserMessages = [];

  const pi = {
    on(eventName, handler) {
      events.set(eventName, handler);
    },
    registerCommand(name, options) {
      commands.set(name, options);
    },
    appendEntry(customType, data) {
      appendedEntries.push({ customType, data });
    },
    sendUserMessage(text, options) {
      sentUserMessages.push({ text, options });
    },
  };

  cavemanExtension(pi);

  return { events, commands, appendedEntries, sentUserMessages };
}

function createCommandContext(overrides = {}) {
  return {
    cwd: process.cwd(),
    hasUI: true,
    isIdle: () => true,
    sessionManager: {
      getEntries: () => [],
    },
    ui: {
      notify() {},
      setStatus() {},
    },
    ...overrides,
  };
}

test("extension registers the full Caveman command set", () => {
  const { commands } = createPiHarness();

  assert.deepEqual([...commands.keys()].sort(), [
    "caveman",
    "caveman-commit",
    "caveman-help",
    "caveman-review",
    "caveman:compress",
  ]);
});

test("/caveman updates persisted mode and before_agent_start injects that mode", async () => {
  const tempConfigHome = mkdtempSync(join(tmpdir(), "caveman-session-"));
  const previousXdg = process.env.XDG_CONFIG_HOME;
  process.env.XDG_CONFIG_HOME = tempConfigHome;

  try {
    const { commands, events, appendedEntries } = createPiHarness();
    const sessionStart = events.get("session_start");
    const beforeAgentStart = events.get("before_agent_start");

    assert.ok(sessionStart, "session_start handler should be registered");
    assert.ok(beforeAgentStart, "before_agent_start handler should be registered");

    const ctx = createCommandContext();
    await sessionStart({ reason: "startup" }, ctx);

    await commands.get("caveman").handler("ultra", ctx);

    assert.deepEqual(appendedEntries.at(-1), {
      customType: "caveman-mode",
      data: { mode: "ultra" },
    });

    const result = await beforeAgentStart({ systemPrompt: "BASE" }, ctx);
    assert.ok(result.systemPrompt.includes("CAVEMAN MODE ACTIVE"));
    assert.ok(result.systemPrompt.includes("ultra"));
  } finally {
    if (previousXdg === undefined) delete process.env.XDG_CONFIG_HOME;
    else process.env.XDG_CONFIG_HOME = previousXdg;
    rmSync(tempConfigHome, { recursive: true, force: true });
  }
});

test("session_start restores the latest persisted session mode over the global default", async () => {
  const tempConfigHome = mkdtempSync(join(tmpdir(), "caveman-restore-"));
  const previousXdg = process.env.XDG_CONFIG_HOME;
  process.env.XDG_CONFIG_HOME = tempConfigHome;

  try {
    const { events } = createPiHarness();
    const sessionStart = events.get("session_start");
    const beforeAgentStart = events.get("before_agent_start");

    const ctx = createCommandContext({
      sessionManager: {
        getEntries: () => [
          {
            type: "custom",
            customType: "caveman-mode",
            data: { mode: "wenyan-lite" },
          },
        ],
      },
    });

    await sessionStart({ reason: "resume" }, ctx);
    const result = await beforeAgentStart({ systemPrompt: "BASE" }, ctx);
    assert.ok(result.systemPrompt.includes("wenyan-lite"));
  } finally {
    if (previousXdg === undefined) delete process.env.XDG_CONFIG_HOME;
    else process.env.XDG_CONFIG_HOME = previousXdg;
    rmSync(tempConfigHome, { recursive: true, force: true });
  }
});

test("skill alias commands delegate to Pi skill commands via sendUserMessage", async () => {
  const { commands, sentUserMessages } = createPiHarness();
  const ctx = createCommandContext();

  await commands.get("caveman-commit").handler("", ctx);
  await commands.get("caveman-review").handler("", ctx);
  await commands.get("caveman-help").handler("", ctx);
  await commands.get("caveman:compress").handler("CLAUDE.md", ctx);

  assert.deepEqual(sentUserMessages.map((entry) => entry.text), [
    "/skill:caveman-commit",
    "/skill:caveman-review",
    "/skill:caveman-help",
    "/skill:caveman-compress CLAUDE.md",
  ]);
  assert.deepEqual(sentUserMessages.map((entry) => entry.options), [
    undefined,
    undefined,
    undefined,
    undefined,
  ]);
});

test("skill alias commands queue follow-up user messages while the agent is busy", async () => {
  const { commands, sentUserMessages } = createPiHarness();
  const ctx = createCommandContext({
    isIdle: () => false,
  });

  await commands.get("caveman-commit").handler("", ctx);
  await commands.get("caveman:compress").handler("CLAUDE.md", ctx);

  assert.deepEqual(sentUserMessages, [
    {
      text: "/skill:caveman-commit",
      options: { deliverAs: "followUp" },
    },
    {
      text: "/skill:caveman-compress CLAUDE.md",
      options: { deliverAs: "followUp" },
    },
  ]);
});

test("input handler disables persistent mode on 'stop caveman' or 'normal mode'", async () => {
  const { commands, events } = createPiHarness();
  const sessionStart = events.get("session_start");
  const input = events.get("input");
  const beforeAgentStart = events.get("before_agent_start");
  const ctx = createCommandContext();

  await sessionStart({ reason: "startup" }, ctx);
  await commands.get("caveman").handler("ultra", ctx);

  const active = await beforeAgentStart({ systemPrompt: "BASE" }, ctx);
  assert.ok(active.systemPrompt.includes("ultra"));

  await input({ text: "normal mode", source: "interactive" }, ctx);
  const disabled = await beforeAgentStart({ systemPrompt: "BASE" }, ctx);
  assert.equal(disabled, undefined);
});
