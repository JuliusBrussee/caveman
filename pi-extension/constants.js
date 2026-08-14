import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

export const EXTENSION_DIR = dirname(fileURLToPath(import.meta.url));
export const DEFAULT_MODE = "full";
export const FALLBACK_MODES = new Set([
  "off",
  "lite",
  "full",
  "ultra",
  "wenyan",
  "wenyan-lite",
  "wenyan-full",
  "wenyan-ultra",
]);
export const CONFIG_MODES = new Set([...FALLBACK_MODES, "commit", "review", "compress"]);
export const INDEPENDENT_MODES = new Set(["commit", "review", "compress"]);

export const SKILL_PATH = join(EXTENSION_DIR, "..", "skills", "caveman", "SKILL.md");
