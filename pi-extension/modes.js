import { CONFIG_MODES, DEFAULT_MODE, FALLBACK_MODES } from "./constants.js";

function normalizeModeText(mode) {
  if (typeof mode !== "string") return null;

  const normalized = mode.trim().toLowerCase();
  return normalized || null;
}

export function normalizeMode(mode) {
  const normalized = normalizeModeText(mode);
  if (!normalized) return null;
  if (normalized === "wenyan") return "wenyan-full";
  return FALLBACK_MODES.has(normalized) ? normalized : null;
}

export function normalizeConfigMode(mode) {
  const normalized = normalizeModeText(mode);
  return normalized && CONFIG_MODES.has(normalized) ? normalized : null;
}

export function normalizePersistedMode(mode) {
  return normalizeMode(mode) || normalizeConfigMode(mode);
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

export function parseCavemanCommand(text, _defaultMode = DEFAULT_MODE) {
  const normalizedText = String(text || "").trim();

  if (!normalizedText) {
    return { type: "status" };
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
