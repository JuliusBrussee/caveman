import type { Hooks, PluginInput } from "@opencode-ai/plugin";
import path from "path";
import fs from "fs/promises";
import os from "os";

const STATE_FILE = path.join(
  process.env.XDG_STATE_HOME ?? path.join(os.homedir(), ".local", "state"),
  "opencode",
  "caveman.json",
);

type Level = "lite" | "full" | "ultra";
type State = { disabled: string[]; levels: Record<string, Level> };

const DEFAULT: State = { disabled: [], levels: {} };

function record(value: unknown): Record<string, unknown> | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return;
  return value as Record<string, unknown>;
}

function level(value: unknown): Level {
  return value === "lite" || value === "full" || value === "ultra"
    ? value
    : "full";
}

function state(value: unknown): State {
  const data = record(value);
  if (!data) return DEFAULT;
  const disabled = Array.isArray(data.disabled)
    ? data.disabled.filter(
        (value): value is string => typeof value === "string",
      )
    : [];
  const levels = Object.fromEntries(
    Object.entries(record(data.levels) ?? {}).flatMap(([key, value]) =>
      value === "lite" || value === "full" || value === "ultra"
        ? [[key, value]]
        : [],
    ),
  ) as Record<string, Level>;
  return { disabled, levels };
}

async function readState(): Promise<State> {
  try {
    return state(JSON.parse(await fs.readFile(STATE_FILE, "utf8")));
  } catch {
    return DEFAULT;
  }
}

const RULES: Record<Level, string> = {
  lite: `[CAVEMAN MODE: lite] Respond terse. No filler/hedging. Keep articles + full sentences. Professional but tight.
Drop: filler (just/really/basically/actually/simply), pleasantries (sure/certainly/of course/happy to), hedging.
Technical terms exact. Code blocks unchanged. Errors quoted exact.
Auto-clarity: full language for security warnings + irreversible ops.`,

  full: `[CAVEMAN MODE: full] Respond terse like smart caveman. All technical substance stay. Only fluff die.
Drop: articles (a/an/the), filler, pleasantries, hedging. Fragments OK. Short synonyms (big not extensive, fix not "implement a solution for").
Pattern: \`[thing] [action] [reason]. [next step].\`
Technical terms exact. Code blocks unchanged. Errors quoted exact.
Auto-clarity: full language for security warnings + irreversible ops.`,

  ultra: `[CAVEMAN MODE: ultra] Respond ultra-terse. Max compression. All technical substance preserved.
Abbreviate (DB/auth/config/req/res/fn/impl). Strip articles+conjunctions+filler+hedging+pleasantries. Arrows for causality (X → Y). One word when sufficient. Fragments OK.
Technical terms exact. Code blocks unchanged. Errors quoted exact.
Auto-clarity: full language for security warnings + irreversible ops only.`,
};

// Track subagent sessions — auto-apply ultra for them
const childSessions = new Set<string>();

async function server(_input: PluginInput): Promise<Hooks> {
  return {
    event: async ({ event }: any) => {
      if (event?.type === "session.created") {
        const info = event?.properties?.info;
        if (info?.parentID) childSessions.add(info.id);
      }
      if (event?.type === "session.deleted") {
        const sid = event?.properties?.sessionID;
        if (sid) childSessions.delete(sid);
      }
    },

    "experimental.chat.system.transform": async (input, output) => {
      const sid = input.sessionID;
      if (!sid) return;
      const state = await readState();
      if (state.disabled.includes(sid)) return;
      const mode: Level = childSessions.has(sid)
        ? "ultra"
        : level(state.levels[sid]);
      output.system.push(RULES[mode] ?? RULES.full);
    },
  };
}

export default { id: "caveman", server };
