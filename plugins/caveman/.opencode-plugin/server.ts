import type { Plugin } from "@opencode-ai/plugin"
import { readFileSync } from "node:fs"
import { homedir } from "node:os"
import path from "node:path"
import { fileURLToPath } from "node:url"
import {
  type CavemanMode,
  normalizeMode,
  readState,
  writeState,
} from "./state.ts"

const home = homedir()
const pluginDir = path.dirname(fileURLToPath(import.meta.url))
const skillCandidates = [
  process.env.CAVEMAN_SKILL_PATH,
  path.resolve(pluginDir, "../../../skills/caveman/SKILL.md"),
  path.join(home, ".agents", "skills", "caveman", "SKILL.md"),
  path.join(home, ".config", "opencode", "skills", "caveman", "SKILL.md"),
].filter((candidate): candidate is string => Boolean(candidate))

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

function commandModeFromText(value: unknown): CavemanMode | "off" | undefined {
  if (typeof value !== "string") return undefined
  const [first] = value.trim().toLowerCase().split(/\s+/)
  return normalizeMode(first)
}

function textFromUnknown(value: unknown): string {
  if (typeof value === "string") return value
  if (!value || typeof value !== "object") return ""
  if (Array.isArray(value)) return value.map(textFromUnknown).filter(Boolean).join("\n")
  const record = value as Record<string, unknown>
  if (typeof record.content === "string") return record.content
  if (typeof record.text === "string") return record.text
  if (typeof record.prompt === "string") return record.prompt
  if (Array.isArray(record.parts)) return textFromUnknown(record.parts)
  return ""
}

function eventUserText(event: unknown): string {
  if (!event || typeof event !== "object") return ""
  const record = event as Record<string, unknown>
  const message = record.message
  if (message && typeof message === "object") {
    const messageRecord = message as Record<string, unknown>
    if (messageRecord.role === "user") return textFromUnknown(message)
  }
  const properties = record.properties
  if (!properties || typeof properties !== "object") return ""
  const props = properties as Record<string, unknown>
  if (typeof props.command === "string") return props.command
  if (typeof props.prompt === "string") return props.prompt
  if (typeof props.message === "string") return props.message
  if (props.info && typeof props.info === "object") {
    const info = props.info as Record<string, unknown>
    if (info.role === "user") return textFromUnknown(info)
  }
  if (props.part && typeof props.part === "object") return textFromUnknown(props.part)
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
  const fallbackMode = readState().mode
  const mode = requestedMode && requestedMode !== "off" ? requestedMode : fallbackMode

  writeState({ enabled: true, mode })
}

export const server: Plugin = async () => {
  writeState(readState())

  return {
    "session.start": async () => ({ context: activationContext() }),

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
  }
}

export default { id: "caveman", server }
