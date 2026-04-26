import type { Plugin } from "@opencode-ai/plugin"
import {
  closeSync,
  existsSync,
  lstatSync,
  mkdirSync,
  openSync,
  readFileSync,
  renameSync,
  writeSync,
} from "node:fs"
import { homedir } from "node:os"
import path from "node:path"

type CavemanMode =
  | "lite"
  | "full"
  | "ultra"
  | "wenyan-lite"
  | "wenyan-full"
  | "wenyan-ultra"

type CavemanState = {
  enabled: boolean
  mode: CavemanMode
}

type JsonRecord = Record<string, unknown>

const DEFAULT_MODE: CavemanMode = "full"
const VALID_MODES = new Set<CavemanMode>([
  "lite",
  "full",
  "ultra",
  "wenyan-lite",
  "wenyan-full",
  "wenyan-ultra",
])

const home = homedir()
const stateDir = process.env.XDG_STATE_HOME
  ? path.join(process.env.XDG_STATE_HOME, "opencode")
  : path.join(home, ".local/state/opencode")
const statePath = path.join(stateDir, "caveman-mode.json")

const configPath = process.env.XDG_CONFIG_HOME
  ? path.join(process.env.XDG_CONFIG_HOME, "caveman", "config.json")
  : path.join(home, ".config", "caveman", "config.json")

const skillCandidates = [
  process.env.CAVEMAN_SKILL_PATH,
  path.join(home, ".agents", "skills", "caveman", "SKILL.md"),
  path.join(home, ".config", "opencode", "skills", "caveman", "SKILL.md"),
].filter((candidate): candidate is string => Boolean(candidate))

function isRecord(value: unknown): value is JsonRecord {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function normalizeMode(value: unknown): CavemanMode | "off" | undefined {
  if (typeof value !== "string") return undefined
  const mode = value.trim().toLowerCase()
  if (mode === "off") return "off"
  if (mode === "wenyan") return "wenyan-full"
  return VALID_MODES.has(mode as CavemanMode) ? (mode as CavemanMode) : undefined
}

function commandModeFromText(value: unknown): CavemanMode | "off" | undefined {
  if (typeof value !== "string") return undefined
  const [first] = value.trim().toLowerCase().split(/\s+/)
  return normalizeMode(first)
}

function defaultMode(): CavemanMode | "off" {
  const envMode = normalizeMode(process.env.CAVEMAN_DEFAULT_MODE)
  if (envMode) return envMode

  try {
    const parsed = JSON.parse(readFileSync(configPath, "utf8")) as unknown
    if (isRecord(parsed)) {
      const mode = normalizeMode(parsed.defaultMode)
      if (mode) return mode
    }
  } catch {
    // Missing or invalid config means use built-in default.
  }

  return DEFAULT_MODE
}

function readState(): CavemanState {
  try {
    const parsed = JSON.parse(readFileSync(statePath, "utf8")) as unknown
    if (isRecord(parsed)) {
      const mode = normalizeMode(parsed.mode)
      return {
        enabled: typeof parsed.enabled === "boolean" ? parsed.enabled : true,
        mode: mode && mode !== "off" ? mode : DEFAULT_MODE,
      }
    }
  } catch {
    // State is best-effort. Fall back to configured default.
  }

  const mode = defaultMode()
  return mode === "off"
    ? { enabled: false, mode: DEFAULT_MODE }
    : { enabled: true, mode }
}

function writeState(state: CavemanState): void {
  try {
    mkdirSync(stateDir, { recursive: true })
    if (existsSync(stateDir) && lstatSync(stateDir).isSymbolicLink()) return
    if (existsSync(statePath) && lstatSync(statePath).isSymbolicLink()) return

    const tempPath = path.join(
      stateDir,
      `.caveman-mode.${process.pid}.${Date.now()}`,
    )
    const fd = openSync(tempPath, "wx", 0o600)
    try {
      writeSync(fd, JSON.stringify(state, null, 2))
    } finally {
      closeSync(fd)
    }
    renameSync(tempPath, statePath)
  } catch {
    // Do not break OpenCode startup because mode persistence failed.
  }
}

function stripFrontmatter(markdown: string): string {
  return markdown.replace(/^---[\s\S]*?---\s*/, "")
}

function readSkill(): string | undefined {
  for (const candidate of skillCandidates) {
    try {
      return stripFrontmatter(readFileSync(candidate, "utf8")).trim()
    } catch {
      continue
    }
  }

  return undefined
}

function filteredSkill(mode: CavemanMode): string {
  const skill = readSkill()
  if (!skill) {
    return [
      "Respond terse like smart caveman. All technical substance stay. Only fluff die.",
      "",
      "## Persistence",
      `ACTIVE EVERY RESPONSE. Current level: ${mode}. Off only: \"stop caveman\" / \"normal mode\".`,
      "",
      "## Rules",
      "Drop articles/filler/pleasantries/hedging. Fragments OK. Technical terms exact. Code blocks unchanged. Errors quoted exact.",
      "",
      "## Auto-Clarity",
      "Drop caveman for security warnings, irreversible actions, user confusion, or where terse fragments risk misread. Resume after clear part done.",
      "",
      "## Boundaries",
      "Code/commits/PRs: write normal.",
    ].join("\n")
  }

  const modeLabel = mode === "wenyan-full" ? "wenyan" : mode
  const lines = skill.split("\n")
  const filtered = lines.filter((line) => {
    const tableMatch = line.match(/^\| \*\*(.*?)\*\* \|/)
    if (tableMatch) return tableMatch[1] === mode

    const exampleMatch = line.match(/^- ([\w-]+): /)
    if (exampleMatch) return exampleMatch[1] === mode

    return true
  })

  return `CAVEMAN MODE ACTIVE - level: ${modeLabel}\n\n${filtered.join("\n")}`
}

function activationContext(): string[] {
  const state = readState()
  if (!state.enabled) return []
  return [filteredSkill(state.mode)]
}

function messageText(message: unknown): string {
  if (typeof message === "string") return message
  if (!isRecord(message)) return ""
  if (typeof message.content === "string") return message.content
  if (typeof message.text === "string") return message.text
  if (Array.isArray(message.parts)) {
    return message.parts
      .map((part) =>
        isRecord(part) && typeof part.text === "string" ? part.text : "",
      )
      .filter(Boolean)
      .join("\n")
  }
  return ""
}

function partText(part: unknown): string {
  if (!isRecord(part)) return ""
  if (part.type === "text" && typeof part.text === "string") return part.text
  if (part.type === "subtask" && typeof part.prompt === "string") {
    return part.prompt
  }
  return ""
}

function partsText(parts: unknown): string {
  if (!Array.isArray(parts)) return ""
  return parts.map(partText).filter(Boolean).join("\n")
}

function eventUserText(event: unknown): string {
  if (!isRecord(event)) return ""
  if (isRecord(event.message) && event.message.role === "user") {
    return messageText(event.message)
  }
  if (isRecord(event.properties)) {
    if (typeof event.properties.command === "string") {
      return event.properties.command
    }
    if (isRecord(event.properties.info) && event.properties.info.role === "user") {
      return messageText(event.properties.info)
    }
    if (isRecord(event.properties.part)) return partText(event.properties.part)
    if (typeof event.properties.prompt === "string") return event.properties.prompt
    if (typeof event.properties.message === "string") return event.properties.message
  }
  return ""
}

function updateModeFromText(text: string): void {
  const lower = text.trim().toLowerCase()
  if (!lower) return

  if (
    /\b(normal mode|stop caveman|disable caveman|turn off caveman|caveman off)\b/.test(
      lower,
    )
  ) {
    writeState({ enabled: false, mode: readState().mode })
    return
  }

  const explicit = lower.match(
    /(?:^|\s)\/?caveman(?::caveman)?(?:\s+(off|lite|full|ultra|wenyan(?:-(?:lite|full|ultra))?))?(?:\s|$)/,
  )
  const natural =
    /\b(activate|enable|turn on|start|use|talk like)\b.*\bcaveman\b/.test(lower) ||
    /\bcaveman\b.*\b(mode|activate|enable|turn on|start)\b/.test(lower) ||
    /\b(less tokens|be brief)\b/.test(lower)

  if (!explicit && !natural) return

  const requestedMode = explicit ? commandModeFromText(explicit[1]) : undefined
  const fallbackMode = defaultMode()
  const mode = requestedMode && requestedMode !== "off" ? requestedMode : fallbackMode

  if (mode === "off") {
    writeState({ enabled: false, mode: readState().mode })
    return
  }

  writeState({ enabled: true, mode })
}

export const CavemanPlugin: Plugin = async () => {
  writeState(readState())

  return {
    "session.start": async () => ({ context: activationContext() }),

    "chat.message": async (_input, output) => {
      updateModeFromText(partsText(output.parts))
    },

    "command.execute.before": async (input) => {
      updateModeFromText(`/${input.command} ${input.arguments}`)
    },

    event: async ({ event }: { event: unknown }) => {
      updateModeFromText(eventUserText(event))
    },

    "experimental.chat.system.transform": async (
      _input: unknown,
      output: { system?: string[] },
    ) => {
      const [context] = activationContext()
      if (context) output.system?.push(context)
    },

    "experimental.session.compacting": async (
      _input: unknown,
      output: { context?: string[] },
    ) => {
      const [context] = activationContext()
      if (context) output.context?.push(context)
    },
  }
}

export default CavemanPlugin
