import { readFileSync, mkdirSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { homedir } from "node:os";
import { fileURLToPath } from "node:url";

const EXTENSION_DIR = dirname(fileURLToPath(import.meta.url));
const DEFAULT_MODE = "full";
const FALLBACK_MODES = new Set([
  "off",
  "lite",
  "full",
  "ultra",
  "wenyan",
  "wenyan-lite",
  "wenyan-full",
  "wenyan-ultra",
]);
const CONFIG_MODES = new Set([...FALLBACK_MODES, "commit", "review", "compress"]);
const INDEPENDENT_MODES = new Set(["commit", "review", "compress"]);

const SKILL_PATH = join(EXTENSION_DIR, "..", "skills", "caveman", "SKILL.md");

function normalizeMode(mode) {
  if (typeof mode !== "string") return null;

  const normalized = mode.trim().toLowerCase();
  if (!normalized) return null;
  if (normalized === "wenyan") return "wenyan-full";
  return FALLBACK_MODES.has(normalized) ? normalized : null;
}

function normalizeConfigMode(mode) {
  if (typeof mode !== "string") return null;

  const normalized = mode.trim().toLowerCase();
  return CONFIG_MODES.has(normalized) ? normalized : null;
}

function normalizePersistedMode(mode) {
  return normalizeMode(mode) || normalizeConfigMode(mode);
}

function getConfigPath(options = {}) {
  if (typeof options === "string") {
    return options;
  }

  const configPath = options?.configPath;
  if (configPath) {
    return configPath;
  }

  if (process.env.XDG_CONFIG_HOME) {
    return join(process.env.XDG_CONFIG_HOME, "caveman", "config.json");
  }

  if (process.platform === "win32") {
    const baseDir = process.env.APPDATA || join(homedir(), "AppData", "Roaming");
    return join(baseDir, "caveman", "config.json");
  }

  return join(homedir(), ".config", "caveman", "config.json");
}

export function readDefaultMode(options = {}) {
  const envMode = normalizeConfigMode(process.env.CAVEMAN_DEFAULT_MODE);
  if (envMode) {
    return envMode;
  }

  const path = getConfigPath(options);

  try {
    const raw = readFileSync(path, "utf8");
    const parsed = JSON.parse(raw);

    const configMode = normalizeConfigMode(parsed?.defaultMode);
    if (configMode) {
      return configMode;
    }
  } catch (_error) {
    // Ignore invalid/missing config.
  }

  return DEFAULT_MODE;
}

export function writeDefaultMode(mode, options = {}) {
  const normalized = normalizeConfigMode(mode);
  if (!normalized) {
    return null;
  }

  const path = getConfigPath(options);
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, JSON.stringify({ defaultMode: normalized }, null, 2), "utf8");

  return normalized;
}

export function resolveSessionMode(entries, fallbackMode = DEFAULT_MODE) {
  const fallback = normalizePersistedMode(fallbackMode) || DEFAULT_MODE;

  if (!Array.isArray(entries)) {
    return fallback;
  }

  for (let i = entries.length - 1; i >= 0; i -= 1) {
    const entry = entries[i];
    if (entry?.type !== "custom" || entry?.customType !== "caveman-mode") {
      continue;
    }

    const mode = normalizePersistedMode(entry?.data?.mode);
    if (mode) {
      return mode;
    }
  }

  return fallback;
}

export function parseCavemanCommand(text, defaultMode = DEFAULT_MODE) {
  const fallback = normalizePersistedMode(defaultMode) || DEFAULT_MODE;
  const normalizedText = String(text || "").trim();

  if (!normalizedText) {
    return {
      type: "set-mode",
      mode: fallback === "off" ? "full" : fallback,
    };
  }

  const [primary, secondary] = normalizedText.split(/\s+/);

  if (primary === "status") {
    return { type: "status" };
  }

  if (primary === "default") {
    const mode = normalizeConfigMode(secondary);
    if (!mode) {
      return { type: "invalid", reason: "invalid-default-mode" };
    }
    return { type: "set-default", mode };
  }

  const mode = normalizeMode(primary);
  if (!mode) {
    return { type: "invalid", reason: "invalid-mode", mode: primary };
  }

  return {
    type: "set-mode",
    mode,
  };
}

export function filterSkillBodyForMode(body, mode) {
  const effectiveMode = normalizeMode(mode) || DEFAULT_MODE;
  const withoutFrontmatter = String(body || "").replace(/^---[\s\S]*?---\s*/, "");

  return withoutFrontmatter
    .split(/\r?\n/)
    .filter((line) => {
      const tableMatch = line.match(/^\|\s*\*\*(.+?)\*\*\s*\|/);
      if (tableMatch) {
        return tableMatch[1].trim() === effectiveMode;
      }

      const exampleMatch = line.match(/^-\s*([^:]+):\s*/);
      if (exampleMatch) {
        return exampleMatch[1].trim() === effectiveMode;
      }

      return true;
    })
    .join("\n");
}

function getFallbackInstructions(mode) {
  const header = `CAVEMAN MODE ACTIVE — level: ${mode}`;

  return (
    `${header}\n\n` +
    "Respond terse like smart caveman. All technical substance stay. Only fluff die.\n\n" +
    "ACTIVE EVERY RESPONSE. No revert after many turns. No filler drift. Still active if unsure. " +
    `Off only: \"stop caveman\" / \"normal mode\".\n\n` +
    `Current level: **${mode}**. Switch: /caveman lite|full|ultra.\n\n` +
    "Rules: drop articles (a/an/the), filler (just/really/basically/actually/simply), pleasantries, and hedging. " +
    "Fragments OK. Short synonyms. Technical terms exact."
  );
}

function getCavemanInstructions(mode) {
  const configuredMode = normalizePersistedMode(mode) || DEFAULT_MODE;

  if (INDEPENDENT_MODES.has(configuredMode)) {
    const command = configuredMode === "compress" ? "/caveman:compress" : `/caveman-${configuredMode}`;
    return `CAVEMAN MODE ACTIVE — level: ${configuredMode}. Behavior defined by ${command} skill.`;
  }

  const effectiveMode = normalizeMode(configuredMode) || DEFAULT_MODE;
  const header = `CAVEMAN MODE ACTIVE — level: ${effectiveMode}`;

  let skillBody = "";
  try {
    skillBody = readFileSync(SKILL_PATH, "utf8");
  } catch (_error) {
    return getFallbackInstructions(effectiveMode);
  }

  const filtered = filterSkillBodyForMode(skillBody, effectiveMode);
  return `${header}\n\n${filtered}`;
}

export default function cavemanExtension(pi) {
  let currentMode = DEFAULT_MODE;
  let configuredDefaultMode = readDefaultMode();

  const setMode = (mode, ctx) => {
    const normalized = normalizePersistedMode(mode);
    if (!normalized) {
      return;
    }

    currentMode = normalized;
    pi.appendEntry("caveman-mode", { mode: normalized });

    if (ctx?.ui?.notify) {
      ctx.ui.notify(`Caveman mode set to ${normalized}.`, "info");
    }
  };

  const sendAlias = (skillName, args, ctx) => {
    const normalized = String(args || "").trim();
    const message = normalized ? `${skillName} ${normalized}` : skillName;

    if (ctx?.isIdle?.() === false) {
      pi.sendUserMessage(message, { deliverAs: "followUp" });
      if (ctx?.ui?.notify) {
        ctx.ui.notify(`${skillName} queued as follow-up.`, "info");
      }
      return;
    }

    pi.sendUserMessage(message);
  };

  pi.registerCommand("caveman", {
    description: "Set or report Caveman mode",
    handler: async (args, ctx) => {
      const parsed = parseCavemanCommand(args, configuredDefaultMode);

      if (parsed.type === "status") {
        if (ctx?.ui?.notify) {
          ctx.ui.notify(`Caveman: current ${currentMode} • default ${configuredDefaultMode}`, "info");
        }
        return;
      }

      if (parsed.type === "set-default") {
        const written = writeDefaultMode(parsed.mode);
        if (written) {
          configuredDefaultMode = readDefaultMode();
          if (ctx?.ui?.notify) {
            if (configuredDefaultMode !== written) {
              ctx.ui.notify(
                `Saved default ${written}, but active env override keeps default at ${configuredDefaultMode}.`,
                "info",
              );
            } else {
              ctx.ui.notify(`Default Caveman mode set to ${written}.`, "info");
            }
          }
        }
        return;
      }

      if (parsed.type === "set-mode") {
        setMode(parsed.mode, ctx);
        return;
      }

      if (parsed.type === "invalid") {
        if (ctx?.ui?.notify) {
          ctx.ui.notify("Unknown or unsupported /caveman mode.", "warning");
        }
        return;
      }
    },
  });

  pi.registerCommand("caveman-commit", {
    description: "Run /skill:caveman-commit",
    handler: (_args, ctx) => {
      sendAlias("/skill:caveman-commit", "", ctx);
    },
  });

  pi.registerCommand("caveman-review", {
    description: "Run /skill:caveman-review",
    handler: (_args, ctx) => {
      sendAlias("/skill:caveman-review", "", ctx);
    },
  });

  pi.registerCommand("caveman-help", {
    description: "Run /skill:caveman-help",
    handler: (_args, ctx) => {
      sendAlias("/skill:caveman-help", "", ctx);
    },
  });

  pi.registerCommand("caveman:compress", {
    description: "Run /skill:caveman-compress",
    handler: (args, ctx) => {
      sendAlias("/skill:caveman-compress", args, ctx);
    },
  });

  pi.on("input", async (event) => {
    if (event?.source === "extension") {
      return;
    }

    const text = String(event?.text || "");
    if (currentMode !== "off" && /\b(stop caveman|normal mode)\b/i.test(text)) {
      setMode("off");
    }
  });

  pi.on("session_start", async (_event, ctx) => {
    const entries = ctx?.sessionManager?.getBranch?.() || ctx?.sessionManager?.getEntries?.() || [];
    configuredDefaultMode = readDefaultMode();
    currentMode = resolveSessionMode(entries, configuredDefaultMode);
  });

  pi.on("before_agent_start", async (event) => {
    if (!currentMode || currentMode === "off") {
      return;
    }

    return {
      systemPrompt: `${event.systemPrompt}\n\n${getCavemanInstructions(currentMode)}`,
    };
  });
}
