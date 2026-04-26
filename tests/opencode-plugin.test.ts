import { expect, test } from "bun:test"
import { cpSync, mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import path from "node:path"
import { fileURLToPath, pathToFileURL } from "node:url"

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..")
const serverPath = path.join(root, "plugins/caveman/.opencode-plugin/server.ts")
const statePath = path.join(root, "plugins/caveman/.opencode-plugin/state.ts")
const tuiPath = path.join(root, "plugins/caveman/.opencode-plugin/tui.ts")

function tempDir(): string {
  return mkdtempSync(path.join(tmpdir(), "caveman-opencode-test-"))
}

function runJson(script: string, env: Record<string, string | undefined> = {}): unknown {
  const mergedEnv: Record<string, string> = { ...process.env }
  for (const [key, value] of Object.entries(env)) {
    if (value === undefined) delete mergedEnv[key]
    else mergedEnv[key] = value
  }

  const result = Bun.spawnSync({
    cmd: ["bun", "--eval", script],
    cwd: root,
    env: mergedEnv,
    stdout: "pipe",
    stderr: "pipe",
  })

  if (result.exitCode !== 0) {
    throw new Error(
      `bun --eval failed\nstdout:\n${result.stdout.toString()}\nstderr:\n${result.stderr.toString()}`,
    )
  }

  return JSON.parse(result.stdout.toString())
}

test("exports OpenCode package modules", async () => {
  const serverModule = await import(pathToFileURL(root).href)
  const tuiModule = await import(pathToFileURL(tuiPath).href)

  expect(serverModule.default.id).toBe("caveman")
  expect(typeof serverModule.default.server).toBe("function")
  expect(tuiModule.default.id).toBe("caveman-tui")
  expect(typeof tuiModule.default.tui).toBe("function")
})

test("state uses macOS-compatible XDG fallback paths", () => {
  const home = tempDir()
  const stateUrl = pathToFileURL(statePath).href
  const result = runJson(
    `
      const state = await import(${JSON.stringify(stateUrl)})
      state.writeState({ enabled: true, mode: "ultra" })
      const fs = await import("node:fs")
      const path = await import("node:path")
      const statePath = path.join(process.env.HOME, ".local/state/opencode/caveman-mode.json")
      console.log(JSON.stringify({
        state: state.readState(),
        stateExists: fs.existsSync(statePath),
        statePath,
      }))
    `,
    { HOME: home, XDG_STATE_HOME: undefined, XDG_CONFIG_HOME: undefined },
  ) as { state: { enabled: boolean; mode: string }; stateExists: boolean; statePath: string }

  expect(result.state).toEqual({ enabled: true, mode: "ultra" })
  expect(result.stateExists).toBe(true)
  expect(result.statePath).toBe(path.join(home, ".local/state/opencode/caveman-mode.json"))
  rmSync(home, { recursive: true, force: true })
})

test("state honors absolute XDG state and config homes", () => {
  const home = tempDir()
  const xdgState = tempDir()
  const xdgConfig = tempDir()
  const configDir = path.join(xdgConfig, "caveman")
  const configPath = path.join(configDir, "config.json")
  const stateUrl = pathToFileURL(statePath).href

  const before = runJson(
    `
      const fs = await import("node:fs")
      const path = await import("node:path")
      fs.mkdirSync(${JSON.stringify(configDir)}, { recursive: true })
      fs.writeFileSync(${JSON.stringify(configPath)}, JSON.stringify({ defaultMode: "wenyan" }))
      const state = await import(${JSON.stringify(stateUrl)})
      console.log(JSON.stringify({ before: state.readState() }))
    `,
    { HOME: home, XDG_STATE_HOME: xdgState, XDG_CONFIG_HOME: xdgConfig },
  ) as { before: { enabled: boolean; mode: string } }

  expect(before.before).toEqual({ enabled: true, mode: "wenyan-full" })

  const result = runJson(
    `
      const state = await import(${JSON.stringify(stateUrl)})
      state.writeState({ enabled: true, mode: "lite" })
      const fs = await import("node:fs")
      const path = await import("node:path")
      const statePath = path.join(process.env.XDG_STATE_HOME, "opencode/caveman-mode.json")
      console.log(JSON.stringify({
        state: state.readState(),
        stateExists: fs.existsSync(statePath),
        statePath,
      }))
    `,
    { HOME: home, XDG_STATE_HOME: xdgState, XDG_CONFIG_HOME: xdgConfig },
  ) as { state: { enabled: boolean; mode: string }; stateExists: boolean; statePath: string }

  expect(result.state).toEqual({ enabled: true, mode: "lite" })
  expect(result.stateExists).toBe(true)
  expect(result.statePath).toBe(path.join(xdgState, "opencode/caveman-mode.json"))
  rmSync(home, { recursive: true, force: true })
  rmSync(xdgState, { recursive: true, force: true })
  rmSync(xdgConfig, { recursive: true, force: true })
})

test("state ignores relative XDG paths", () => {
  const home = tempDir()
  const stateUrl = pathToFileURL(statePath).href
  const result = runJson(
    `
      const state = await import(${JSON.stringify(stateUrl)})
      state.writeState({ enabled: true, mode: "ultra" })
      const fs = await import("node:fs")
      const path = await import("node:path")
      const fallbackPath = path.join(process.env.HOME, ".local/state/opencode/caveman-mode.json")
      const relativePath = path.join(process.cwd(), "relative-state/opencode/caveman-mode.json")
      console.log(JSON.stringify({
        fallbackExists: fs.existsSync(fallbackPath),
        relativeExists: fs.existsSync(relativePath),
        state: state.readState(),
      }))
    `,
    { HOME: home, XDG_STATE_HOME: "relative-state", XDG_CONFIG_HOME: "relative-config" },
  ) as { fallbackExists: boolean; relativeExists: boolean; state: { enabled: boolean; mode: string } }

  expect(result.fallbackExists).toBe(true)
  expect(result.relativeExists).toBe(false)
  expect(result.state).toEqual({ enabled: true, mode: "ultra" })
  rmSync(home, { recursive: true, force: true })
})

test("state refuses symlink state file writes", () => {
  const home = tempDir()
  const stateDir = path.join(home, ".local/state/opencode")
  const stateFile = path.join(stateDir, "caveman-mode.json")
  const target = path.join(home, "target.json")
  writeFileSync(target, "secret")
  mkdirSync(stateDir, { recursive: true })
  symlinkSync(target, stateFile)
  const stateUrl = pathToFileURL(statePath).href

  const result = runJson(
    `
      const state = await import(${JSON.stringify(stateUrl)})
      state.writeState({ enabled: true, mode: "ultra" })
      const fs = await import("node:fs")
      console.log(JSON.stringify({ target: fs.readFileSync(${JSON.stringify(target)}, "utf8") }))
    `,
    { HOME: home, XDG_STATE_HOME: undefined, XDG_CONFIG_HOME: undefined },
  ) as { target: string }

  expect(result.target).toBe("secret")
  rmSync(home, { recursive: true, force: true })
})

test("server exposes startup, chat, command, event, system, and compaction hooks", () => {
  const home = tempDir()
  const skill = path.join(home, "SKILL.md")
  writeFileSync(skill, "---\ntitle: fixture\n---\nFixture caveman skill")
  const serverUrl = pathToFileURL(serverPath).href
  const result = runJson(
    `
      const mod = await import(${JSON.stringify(serverUrl)})
      const hooks = await mod.server({})
      const start = await hooks["session.start"]()
      const system = { system: [] }
      await hooks["experimental.chat.system.transform"]({}, system)
      const compaction = { context: [] }
      await hooks["experimental.session.compacting"]({}, compaction)
      await hooks["command.execute.before"]({ command: "caveman", arguments: "ultra", sessionID: "s" }, { parts: [] })
      const afterCommand = await hooks["session.start"]()
      await hooks["chat.message"]({}, { parts: [{ type: "text", text: "normal mode" }] })
      const afterDisable = await hooks["session.start"]()
      console.log(JSON.stringify({
        keys: Object.keys(hooks).sort(),
        start,
        system,
        compaction,
        afterCommand,
        afterDisable,
      }))
    `,
    { HOME: home, CAVEMAN_SKILL_PATH: skill, XDG_STATE_HOME: undefined, XDG_CONFIG_HOME: undefined },
  ) as {
    keys: string[]
    start: { context: string[] }
    system: { system: string[] }
    compaction: { context: string[] }
    afterCommand: { context: string[] }
    afterDisable: { context: string[] }
  }

  expect(result.keys).toContain("session.start")
  expect(result.keys).toContain("chat.message")
  expect(result.keys).toContain("command.execute.before")
  expect(result.keys).toContain("event")
  expect(result.keys).toContain("experimental.chat.system.transform")
  expect(result.keys).toContain("experimental.session.compacting")
  expect(result.start.context[0]).toContain("Fixture caveman skill")
  expect(result.start.context[0]).not.toContain("title: fixture")
  expect(result.system.system[0]).toContain("Fixture caveman skill")
  expect(result.compaction.context[0]).toContain("Fixture caveman skill")
  expect(result.afterCommand.context[0]).toContain("CAVEMAN MODE ACTIVE — level: ultra")
  expect(result.afterDisable.context).toEqual([])
  rmSync(home, { recursive: true, force: true })
})

test("server uses fallback instructions without readable skill", () => {
  const home = tempDir()
  const pluginRoot = tempDir()
  const pluginDir = path.join(pluginRoot, "plugins/caveman/.opencode-plugin")
  mkdirSync(pluginDir, { recursive: true })
  cpSync(serverPath, path.join(pluginDir, "server.ts"))
  cpSync(statePath, path.join(pluginDir, "state.ts"))
  const serverUrl = pathToFileURL(path.join(pluginDir, "server.ts")).href
  const result = runJson(
    `
      const mod = await import(${JSON.stringify(serverUrl)})
      const hooks = await mod.server({})
      const start = await hooks["session.start"]()
      console.log(JSON.stringify(start))
    `,
    { HOME: home, CAVEMAN_SKILL_PATH: path.join(home, "missing.md") },
  ) as { context: string[] }

  expect(result.context[0]).toContain("CAVEMAN MODE ACTIVE — level: full")
  expect(result.context[0]).toContain("Respond terse like smart caveman")
  expect(result.context[0]).toContain("Current level: full")
  rmSync(home, { recursive: true, force: true })
  rmSync(pluginRoot, { recursive: true, force: true })
})

test("plain caveman trigger respects off default mode", () => {
  const home = tempDir()
  const serverUrl = pathToFileURL(serverPath).href
  const result = runJson(
    `
      const mod = await import(${JSON.stringify(serverUrl)})
      const hooks = await mod.server({})
      await hooks["command.execute.before"]({ command: "caveman", arguments: "", sessionID: "s" }, { parts: [] })
      const plain = await hooks["session.start"]()
      await hooks["command.execute.before"]({ command: "caveman", arguments: "lite", sessionID: "s" }, { parts: [] })
      const explicit = await hooks["session.start"]()
      console.log(JSON.stringify({ plain, explicit }))
    `,
    { HOME: home, CAVEMAN_DEFAULT_MODE: "off", XDG_STATE_HOME: undefined, XDG_CONFIG_HOME: undefined },
  ) as { plain: { context: string[] }; explicit: { context: string[] } }

  expect(result.plain.context).toEqual([])
  expect(result.explicit.context[0]).toContain("CAVEMAN MODE ACTIVE — level: lite")
  rmSync(home, { recursive: true, force: true })
})

test("server updates mode from OpenCode events", () => {
  const home = tempDir()
  const serverUrl = pathToFileURL(serverPath).href
  const result = runJson(
    `
      const mod = await import(${JSON.stringify(serverUrl)})
      const hooks = await mod.server({})
      await hooks.event({ event: { message: { role: "user", parts: [{ type: "text", text: "talk like caveman" }] } } })
      const enabled = await hooks["session.start"]()
      await hooks.event({ event: { properties: { prompt: "normal mode" } } })
      const disabled = await hooks["session.start"]()
      console.log(JSON.stringify({ enabled, disabled }))
    `,
    { HOME: home, XDG_STATE_HOME: undefined, XDG_CONFIG_HOME: undefined },
  ) as { enabled: { context: string[] }; disabled: { context: string[] } }

  expect(result.enabled.context[0]).toContain("CAVEMAN MODE ACTIVE — level: full")
  expect(result.disabled.context).toEqual([])
  rmSync(home, { recursive: true, force: true })
})
