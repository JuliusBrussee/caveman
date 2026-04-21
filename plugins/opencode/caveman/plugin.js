import fs from "node:fs";
import path from "node:path";
import os from "node:os";

const FLAG_DIR = path.join(os.homedir(), ".config", "opencode");
const FLAG_FILE = path.join(FLAG_DIR, ".caveman-active");

function writeFlag(mode) {
  try {
    fs.mkdirSync(FLAG_DIR, { recursive: true });
    fs.writeFileSync(FLAG_FILE, mode);
  } catch {}
}

function clearFlag() {
  try { fs.unlinkSync(FLAG_FILE); } catch {}
}

function detectModeFromCommand(text) {
  const trimmed = text.trim();
  if (/^\/caveman-commit\b/.test(trimmed)) return "commit";
  if (/^\/caveman-review\b/.test(trimmed)) return "review";
  if (/^\/caveman-?compress[:\/]/.test(trimmed) || /^\/caveman-compress\b/.test(trimmed)) return "compress";
  const match = trimmed.match(/^\/caveman(?:\s+(.+))?$/);
  if (match) {
    const level = (match[1] || "").trim().toLowerCase();
    if (!level) return "full";
    if (level === "lite") return "lite";
    if (level === "ultra") return "ultra";
    if (level === "wenyan-lite") return "wenyan-lite";
    if (/^(wenyan|wenyan-full)$/.test(level)) return "wenyan";
    if (level === "wenyan-ultra") return "wenyan-ultra";
    return "full";
  }
  return null;
}

function detectModeFromInput(text) {
  const lower = text.toLowerCase();
  if (/\b(stop caveman|normal mode|caveman off|disable caveman)\b/.test(lower)) return "inactive";
  if (/\b(caveman mode|talk like caveman|use caveman|less tokens|be brief|be concise|terse mode)\b/.test(lower)) return "active";
  return null;
}

/**
 * @param {{project: any, client: any, $: any, directory: string, worktree: string}} ctx
 */
export const cavemanPlugin = async ({ client, $, directory }) => {
  writeFlag("full");
  if (client?.app?.log) {
    try {
      await client.app.log({
        body: { service: "caveman", level: "info", message: "caveman mode activated (full)" },
      });
    } catch {}
  }

  let currentMode = "full";

  return {
    "tui.command.execute": async (input, output) => {
      const text = output.command || input.command || "";
      const mode = detectModeFromCommand(text);
      if (mode) { currentMode = mode; writeFlag(mode); }
      if (/\b(stop caveman|normal mode)\b/i.test(text)) { currentMode = null; clearFlag(); }
    },

    "message.updated": async ({ event }) => {
      try {
        const role = event.message?.role;
        if (role !== "user") return;
        const text = event.message?.content || [];
        const textStr = text.filter((p) => p.type === "text").map((p) => p.text || "").join(" ").toLowerCase();
        if (!textStr) return;
        if (textStr.startsWith("/caveman")) {
          const mode = detectModeFromCommand(textStr);
          if (mode) { currentMode = mode; writeFlag(mode); }
          return;
        }
        const result = detectModeFromInput(textStr);
        if (result === "active" && !currentMode) { currentMode = "full"; writeFlag("full"); }
        else if (result === "inactive" && currentMode) { currentMode = null; clearFlag(); }
      } catch {}
    },

    "session.created": async () => {
      writeFlag("full");
      currentMode = "full";
    },
  };
};
