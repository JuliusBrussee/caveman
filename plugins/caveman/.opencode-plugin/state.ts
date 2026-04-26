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

export type CavemanMode =
  | "lite"
  | "full"
  | "ultra"
  | "wenyan-lite"
  | "wenyan-full"
  | "wenyan-ultra"

export type CavemanState = {
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

export function normalizeMode(value: unknown): CavemanMode | "off" | undefined {
  if (typeof value !== "string") return undefined
  const mode = value.trim().toLowerCase()
  if (mode === "off") return "off"
  if (mode === "wenyan") return "wenyan-full"
  return VALID_MODES.has(mode as CavemanMode) ? (mode as CavemanMode) : undefined
}

function isRecord(value: unknown): value is JsonRecord {
  return typeof value === "object" && value !== null && !Array.isArray(value)
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

export function readState(): CavemanState {
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

export function writeState(state: CavemanState): void {
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
