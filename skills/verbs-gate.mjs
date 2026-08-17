// verbs-gate.mjs — the skills-verbs drift gate (fail-closed, deterministic).
//
// Invariant: a skill can never document a CLI command, MCP tool, or SDK call
// the shipped surfaces do not expose. This module derives the real surfaces
// from source — the CLI verb tables (TOOL_DISCOVERY/CLOUD_DISCOVERY + the
// reserved-verbs registry), both MCP servers' tool names (engine Go + the
// agent-native TS server), and the two SDKs' method definitions — then scans
// each canonical SKILL.md for references and reports anything that does not
// resolve. It is pure (returns a list of violations; it never exits), so the
// compiler and the tests share one implementation.
//
// Recognition rule: commands/tools/API calls are recognized inside code
// contexts (fenced blocks and inline `code` spans). An unknown token inside a
// code context fails closed. An unknown top-level word in prose is treated as
// ordinary prose (the `caveman` persona skill legitimately says "like caveman
// while ...") and ignored — but the unambiguous command markers `caveman tools`
// and `caveman cloud`, MCP `caveman_*` tokens, and the required `source`
// argument on `artifacts.page` are enforced wherever they appear.

import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";

function readOptional(path) {
  return existsSync(path) ? readFileSync(path, "utf8") : "";
}

// Pull the { verb: "x" } tokens out of one DiscoveryGroup[] literal in the CLI.
function discoveryVerbs(src, marker) {
  const start = src.indexOf(marker);
  if (start < 0) return new Set();
  const end = src.indexOf("\n];", start);
  if (end < 0) return new Set();
  const block = src.slice(start, end);
  const verbs = new Set();
  for (const m of block.matchAll(/verb:\s*"([a-z0-9-]+)"/g)) verbs.add(m[1]);
  return verbs;
}

// Derive every surface a skill may reference. `errors` is non-empty when a
// source could not be parsed — the caller must fail closed rather than scan
// against an empty surface (which would pass everything).
export function loadSurfaces({ skillsDir, cliDir }) {
  const errors = [];
  const publicRoot = join(skillsDir, "..");
  const packageRoot = existsSync(join(publicRoot, "sdk"))
    ? publicRoot
    : join(publicRoot, "packages");

  // Top-level CLI verbs: the reserved-verbs registry is the canonical source
  // (it already drives reserved-verbs.generated.ts).
  let topLevel = new Set();
  try {
    const doc = JSON.parse(readFileSync(join(skillsDir, "..", "agents", "reserved-verbs.json"), "utf8"));
    if (!Array.isArray(doc.verbs)) throw new Error("verbs is not an array");
    topLevel = new Set(doc.verbs);
  } catch (error) {
    errors.push(`reserved-verbs.json: ${error.message}`);
  }

  // `caveman tools <verb>` / `caveman cloud <verb>` namespaces.
  const indexSrc = readOptional(join(cliDir, "src", "index.ts"));
  const toolVerbs = discoveryVerbs(indexSrc, "const TOOL_DISCOVERY");
  const cloudVerbs = discoveryVerbs(indexSrc, "const CLOUD_DISCOVERY");
  if (toolVerbs.size === 0) errors.push("could not parse TOOL_DISCOVERY verbs from CLI index.ts");
  if (cloudVerbs.size === 0) errors.push("could not parse CLOUD_DISCOVERY verbs from CLI index.ts");

  // MCP tool names: engine server (Go) + agent-native server (TS).
  const mcpTools = new Set();
  let declaredEngineTools = [];
  try {
    const manifest = JSON.parse(readFileSync(join(skillsDir, "engine-mcp-tools.json"), "utf8"));
    if (manifest.schema_version !== "1" || !Array.isArray(manifest.tools)) {
      throw new Error('schema_version must be "1" and tools must be an array');
    }
    declaredEngineTools = [...manifest.tools];
    if (
      declaredEngineTools.length === 0
      || new Set(declaredEngineTools).size !== declaredEngineTools.length
      || declaredEngineTools.some((name) => typeof name !== "string" || !/^caveman_[a-z_]+$/.test(name))
    ) {
      throw new Error("tools must be unique caveman_* names");
    }
    for (const name of declaredEngineTools) mcpTools.add(name);
  } catch (error) {
    errors.push(`engine-mcp-tools.json: ${error.message}`);
  }
  const engineSrc = readOptional(join(skillsDir, "..", "mcp", "engine_tools.go"));
  const sourceEngineTools = [...engineSrc.matchAll(/Tool\w+\s*=\s*"(caveman_[a-z_]+)"/g)].map((match) => match[1]);
  if (sourceEngineTools.length > 0) {
    const declared = [...declaredEngineTools].sort();
    const source = [...new Set(sourceEngineTools)].sort();
    if (JSON.stringify(declared) !== JSON.stringify(source)) {
      errors.push("engine-mcp-tools.json drifted from public/mcp/engine_tools.go");
    }
  }
  const agentMcpSrc = readOptional(join(cliDir, "src", "agent-mcp.ts"));
  for (const m of agentMcpSrc.matchAll(/name:\s*"(caveman_[a-z_]+)"/g)) mcpTools.add(m[1]);
  if (mcpTools.size === 0) errors.push("could not parse MCP tool names");

  // SDK method/property surface: TS + Python definitions.
  const sdkMembers = new Set();
  const tsSrc = readOptional(join(packageRoot, "sdk", "typescript", "src", "index.ts"));
  for (const re of [
    /^\s+(?:public\s+|private\s+|readonly\s+|static\s+)*(?:async\s+)?([a-zA-Z_$][\w$]*)\s*\(/gm, // method defs
    /^\s+([a-zA-Z_$][\w$]*)\s*=\s*/gm, // class fields (e.g. artifacts = { ... })
    /^\s+([a-zA-Z_$][\w$]*)\s*:\s*(?:async\s*)?\(/gm, // object-literal methods (page: async ( ... ))
  ]) {
    for (const m of tsSrc.matchAll(re)) sdkMembers.add(m[1]);
  }
  const pySrc = readOptional(join(packageRoot, "sdk", "python", "caveman_cloud", "core.py"));
  for (const m of pySrc.matchAll(/^\s+def\s+([a-zA-Z_]\w*)\s*\(/gm)) sdkMembers.add(m[1]);
  if (!sdkMembers.has("compress") || !sdkMembers.has("page") || !sdkMembers.has("tools")) {
    errors.push("could not parse the SDK member surface");
  }

  // Assert the required-`source` contract against the live TS signature so the
  // gate can never enforce a stale requirement.
  const pageRequiresSource = /page:\s*async\s*\([^)]*options:\s*\{[^}]*\bsource:\s*string\b/.test(tsSrc);
  if (!pageRequiresSource) {
    errors.push("artifacts.page no longer declares a required `source` — refresh the gate");
  }

  // caveman_-prefixed identifiers that are NOT MCP tools: the Python SDK package.
  const sdkPackages = new Set();
  if (existsSync(join(packageRoot, "sdk", "python", "caveman_cloud"))) sdkPackages.add("caveman_cloud");

  return { topLevel, toolVerbs, cloudVerbs, mcpTools, sdkMembers, sdkPackages, pageRequiresSource, errors };
}

// Character ranges covered by a fenced code block or an inline `code` span.
// Fenced regions are masked before inline matching so an inline span can never
// straddle a fence; inline spans may cross a single line break (Markdown renders
// an inline `code` span containing a newline as a space).
function codeIntervals(body) {
  const intervals = [];
  const fence = /^```[^\n]*\n([\s\S]*?)^```/gm;
  let m;
  while ((m = fence.exec(body))) intervals.push([m.index, fence.lastIndex]);
  const masked = body.split("");
  for (const [start, end] of intervals) {
    for (let i = start; i < end; i++) masked[i] = " ";
  }
  const inline = /`[^`]+`/g;
  const maskedText = masked.join("");
  while ((m = inline.exec(maskedText))) intervals.push([m.index, inline.lastIndex]);
  return intervals;
}

function inCode(intervals, idx) {
  return intervals.some(([start, end]) => idx >= start && idx < end);
}

// Smallest code context containing idx (the inline span for an inline call).
function enclosingCode(intervals, body, idx) {
  let best = null;
  for (const [start, end] of intervals) {
    if (idx >= start && idx < end && (!best || end - start < best[1] - best[0])) best = [start, end];
  }
  return best ? body.slice(best[0], best[1]) : "";
}

// Scan one SKILL.md body against the derived surfaces. Returns violation
// strings (empty = clean). Never throws, never exits.
export function scanSkillBody(id, body, surfaces) {
  const violations = [];
  const intervals = codeIntervals(body);
  const V = (message) => violations.push(`${id}: ${message}`);

  // CLI commands: `caveman <verb> [<sub>]` / `cave <verb> [<sub>]`. The lookbehind
  // rejects `/caveman` (slash command), `@caveman-ai/sdk`, `caveman-learn`, and
  // `foo.cave`.
  const cliRe = /(?<![\w/@.\-])(caveman|cave)[ \t]+([a-z][\w-]*)(?:[ \t]+([a-z][\w-]*))?/g;
  for (const m of body.matchAll(cliRe)) {
    const [, bin, verb, sub] = m;
    const code = inCode(intervals, m.index);
    if (verb === "tools") {
      if (sub && !surfaces.toolVerbs.has(sub)) V(`unknown \`${bin} tools ${sub}\` — not a TOOL_VERBS verb`);
      else if (!sub && code) V(`\`${bin} tools\` without a verb`);
    } else if (verb === "cloud") {
      if (sub && !surfaces.cloudVerbs.has(sub)) V(`unknown \`${bin} cloud ${sub}\` — not a CLOUD_VERBS verb`);
      else if (!sub && code) V(`\`${bin} cloud\` without a verb`);
    } else if (surfaces.topLevel.has(verb)) {
      // known top-level command
    } else if (code) {
      V(`unknown command \`${bin} ${verb}\` — not a reserved top-level verb`);
    }
    // unknown verb in prose = ordinary word (the caveman persona) → ignore
  }

  // MCP tool tokens: caveman_<name>. Snake-case is never English prose, so an
  // unknown one fails wherever it appears (except the Python SDK package name).
  const mcpRe = /(?<![\w/@.\-])(caveman_[a-z_]+)/g;
  for (const m of body.matchAll(mcpRe)) {
    const name = m[1];
    if (surfaces.sdkPackages.has(name) || surfaces.mcpTools.has(name)) continue;
    V(`unknown MCP tool / caveman_ identifier \`${name}\``);
  }

  // SDK calls: cave./trace./handle.<member>[.<member>]( — only inside code.
  const sdkRe = /(?<![\w$.])(cave|trace|handle)\.([a-zA-Z_$][\w$]*)(?:\.([a-zA-Z_$][\w$]*))?[ \t\n]*\(/g;
  for (const m of body.matchAll(sdkRe)) {
    if (!inCode(intervals, m.index)) continue;
    const [, recv, first, second] = m;
    if (!surfaces.sdkMembers.has(first)) { V(`unknown SDK member \`${recv}.${first}\``); continue; }
    if (second && !surfaces.sdkMembers.has(second)) { V(`unknown SDK member \`${recv}.${first}.${second}\``); continue; }
    if (first === "artifacts" && second === "page") {
      const ctx = enclosingCode(intervals, body, m.index);
      if (!/\bsource\b/.test(ctx)) V("`artifacts.page` call omits the required `source` argument");
    }
  }

  return violations;
}
