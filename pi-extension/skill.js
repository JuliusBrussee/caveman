import { readFileSync } from "node:fs";

import { DEFAULT_MODE, INDEPENDENT_MODES, SKILL_PATH } from "./constants.js";
import { filterSkillBodyForMode, normalizeMode, normalizePersistedMode } from "./modes.js";

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

export function getCavemanInstructions(mode) {
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
