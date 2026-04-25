/**
 * Caveman always-on plugin for OpenCode.
 *
 * WORKAROUND IMPLEMENTATION (v2):
 * Original hooks (chat.message, experimental.chat.system.transform) are broken
 * in OpenCode due to bugs #22831 and #17100.
 *
 * This version uses:
 * - session.start: Injects caveman rules into system prompt on session start
 *   (fires on initial session AND after compaction since compaction creates new session)
 * - event (message.updated): Parses /caveman commands and persists mode to file
 * - File-based state: ~/.config/opencode/.caveman-mode
 */

const fs = await import("node:fs")
const path = await import("node:path")

const MODE_FULL = "full"
const MODE_FILE = path.join(
  process.env.HOME || "/tmp",
  ".config/opencode/.caveman-mode"
)

const BASE_RULE = [
  "Respond terse like smart caveman. All technical substance stay. Only fluff die.",
  "",
  "Rules:",
  "- Drop: articles (a/an/the), filler (just/really/basically), pleasantries, hedging",
  "- Fragments OK. Short synonyms. Technical terms exact. Code unchanged.",
  "- Pattern: [thing] [action] [reason]. [next step].",
  '  "- Not: "Sure! I\'d be happy to help you with that."',
  '  "- Yes: "Bug in auth middleware. Fix:"',
  "",
  "Switch level: /caveman lite|full|ultra|wenyan",
  '  "Stop: "stop caveman" or "normal mode"',
  "",
  "Auto-Clarity: drop caveman for security warnings, irreversible actions, user confused. Resume after.",
  "",
  "Boundaries: code/commits/PRs written normal.",
].join("\n")

function readModeFile() {
  try {
    const raw = fs.readFileSync(MODE_FILE, "utf-8").trim()
    const parsed = JSON.parse(raw)
    return { enabled: parsed.enabled ?? true, mode: parsed.mode || MODE_FULL }
  } catch {
    return { enabled: true, mode: MODE_FULL }
  }
}

function writeModeFile(state) {
  try {
    const dir = path.dirname(MODE_FILE)
    if (!fs.existsSync(dir)) {
      fs.mkdirSync(dir, { recursive: true })
    }
    fs.writeFileSync(MODE_FILE, JSON.stringify(state, null, 2))
  } catch (err) {
    console.error("[caveman] Failed to write mode file:", err)
  }
}

function normalizeMode(raw) {
  const value = (raw || "").trim().toLowerCase()
  if (
    [
      "lite",
      "full",
      "ultra",
      "wenyan",
      "wenyan-lite",
      "wenyan-full",
      "wenyan-ultra",
    ].includes(value)
  ) {
    return value
  }
  return MODE_FULL
}

function parseIntent(text) {
  const lower = text.trim().toLowerCase()
  if (!lower) return null

  if (/(^|\b)(stop caveman|normal mode)(\b|$)/.test(lower)) {
    return { enabled: false }
  }

  const explicit = lower.match(
    /(?:^|\s)\/?caveman(?:\s+(lite|full|ultra|wenyan(?:-(?:lite|full|ultra))?))?(?:\s|$)/
  )
  if (explicit) {
    return { enabled: true, mode: normalizeMode(explicit[1]) }
  }

  // Bare mode word at end of message (e.g., "...explain this. ultra")
  const bareMode = lower.match(/(?:^|\s)(lite|full|ultra|wenyan(?:-(?:lite|full|ultra))?)\s*$/)
  if (bareMode) {
    return { enabled: true, mode: normalizeMode(bareMode[1]) }
  }

  if (
    lower.includes("talk like caveman") ||
    lower.includes("caveman mode") ||
    lower.includes("less tokens please")
  ) {
    return { enabled: true, mode: MODE_FULL }
  }

  return null
}

function modeRule(mode) {
  switch (mode) {
    case "lite":
      return `${BASE_RULE}\n\nIntensity override: lite. Keep grammar mostly intact. Still terse. No fluff.`
    case "ultra":
      return `${BASE_RULE}\n\nIntensity override: ultra. Compress aggressively. Telegraphic OK.`
    case "wenyan":
    case "wenyan-lite":
    case "wenyan-full":
    case "wenyan-ultra":
      return `${BASE_RULE}\n\nIntensity override: ${mode}. Prefer classical-Chinese-style compression while preserving technical correctness when feasible.`
    case "full":
    default:
      return BASE_RULE
  }
}

/**
 * @type {import("@opencode-ai/plugin").Plugin}
 */
export default async () => {
  // Ensure mode file exists with defaults
  const initialState = readModeFile()
  writeModeFile(initialState)

  return {
    // PRIMARY: session.start injects caveman into system prompt
    // Fires on: initial session start AND after compaction (new session)
    // Returns: { context: ["string to prepend to system prompt"] }
    "session.start": async (input) => {
      const state = readModeFile()
      if (!state.enabled) {
        return { context: [] }
      }
      return {
        context: [modeRule(state.mode)],
      }
    },

    // FALLBACK: event hook catches /caveman commands mid-session
    // Updates mode file for next session.start (next turn or post-compaction)
    // Note: Won't affect current turn's system prompt
    event: async ({ event }) => {
      if (event.type === "message.updated" && event.message?.role === "user") {
        const text =
          event.message.content || event.message.text || event.message.parts?.[0]?.text || ""
        const intent = parseIntent(text)
        if (intent) {
          writeModeFile(intent)
          console.log(`[caveman] Mode updated: ${JSON.stringify(intent)}`)
        }
      }
    },

    // BACKUP: Also try the original transform hook in case it gets fixed
    "experimental.chat.system.transform": async (input, output) => {
      const state = readModeFile()
      if (!state.enabled) return
      output.system.push(modeRule(state.mode))
    },
  }
}
