import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { homedir } from "node:os";

import { DEFAULT_MODE } from "./constants.js";
import { normalizeConfigMode } from "./modes.js";

export function getConfigPath(options = {}) {
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
