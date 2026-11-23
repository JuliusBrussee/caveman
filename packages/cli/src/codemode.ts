import {
  copyFileSync,
  cpSync,
  existsSync,
  lstatSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  renameSync,
  statSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { basename, join, resolve } from "node:path";

export type CodemodeServerConfig = {
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  cwd?: string;
  url?: string;
  headers?: Record<string, string>;
  includeTools?: string[];
  excludeTools?: string[];
};

export type CodemodeLaunchEntry = {
  command: string;
  args: string[];
};

/**
 * Harnesses that ship their own native code-mode / programmatic tool execution
 * (Claude Code, Codex CLI, OpenCode, and Hermes Agent's built-in `execute_code`).
 * Caveman's QuickJS Code-Mode stays strictly opt-in (`--codemode` / `execute.mcp = codemode`)
 * so it never conflicts with a host's native codemode unless explicitly requested.
 * Note: Hermes's native `execute_code` runs built-in tools only and blocks MCP servers,
 * so `caveman wrap --codemode hermes` brings programmatic execution to Hermes's MCP tools.
 */
export const NATIVE_CODEMODE_AGENTS: ReadonlySet<string> = new Set(["claude", "codex", "opencode", "hermes"]);

function isValidServer(cfg: CodemodeServerConfig): boolean {
  if (typeof cfg.command === "string" && cfg.command.trim()) return true;
  if (typeof cfg.url === "string") {
    const u = cfg.url.trim();
    return u.startsWith("http://") || u.startsWith("https://");
  }
  return false;
}

function isSelfServer(name: string, cfg: CodemodeServerConfig): boolean {
  const lower = name.trim().toLowerCase();
  if (
    lower === "caveman" ||
    lower === "caveman-codemode" ||
    lower === "caveman-mcp" ||
    lower === "caveman-tools" ||
    lower === "caveman_native" ||
    lower === "caveman-native"
  ) {
    return true;
  }
  if (cfg.command) {
    const base = basename(cfg.command.trim()).toLowerCase();
    if (base === "caveman-mcp" || base === "caveman-mcp.exe") {
      return true;
    }
    if ((base === "caveman" || base === "caveman.exe") && cfg.args?.[0]?.toLowerCase() === "codemode") {
      return true;
    }
  }
  return false;
}

/**
 * Parses standard JSON as well as JSONC / relaxed JSON5 configs (UTF-8 BOM,
 * `//` line comments, `/* *\/` block comments, and trailing commas) used by
 * OpenCode, Kilo, OpenClaw, Gemini CLI, and Qwen Code.
 */
export function parseJsonRelaxed(rawText: string): unknown {
  const text = rawText.charCodeAt(0) === 0xfeff ? rawText.slice(1) : rawText;
  let stripped = "";
  let inString = false;
  let escaped = false;
  let i = 0;
  while (i < text.length) {
    const c = text[i]!;
    const next = text[i + 1];
    if (inString) {
      stripped += c;
      if (escaped) {
        escaped = false;
      } else if (c === "\\") {
        escaped = true;
      } else if (c === '"') {
        inString = false;
      }
      i++;
      continue;
    }
    if (c === '"') {
      inString = true;
      stripped += c;
      i++;
      continue;
    }
    if (c === "/" && next === "/") {
      i += 2;
      while (i < text.length && text[i] !== "\n" && text[i] !== "\r") i++;
      continue;
    }
    if (c === "/" && next === "*") {
      i += 2;
      while (i < text.length && !(text[i] === "*" && text[i + 1] === "/")) i++;
      i += 2;
      continue;
    }
    stripped += c;
    i++;
  }
  // Remove trailing commas before `}` or `]` outside strings.
  let out = "";
  inString = false;
  escaped = false;
  for (let j = 0; j < stripped.length; j++) {
    const c = stripped[j]!;
    if (inString) {
      out += c;
      if (escaped) escaped = false;
      else if (c === "\\") escaped = true;
      else if (c === '"') inString = false;
      continue;
    }
    if (c === '"') {
      inString = true;
      out += c;
      continue;
    }
    if (c === ",") {
      let k = j + 1;
      while (k < stripped.length && /\s/.test(stripped[k]!)) k++;
      if (stripped[k] === "}" || stripped[k] === "]") {
        continue;
      }
    }
    out += c;
  }
  return JSON.parse(out);
}

function stringMap(raw: unknown): Record<string, string> | undefined {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return undefined;
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(raw as Record<string, unknown>)) {
    if (typeof v === "string") out[k] = v;
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

function stringList(raw: unknown): string[] | undefined {
  if (!Array.isArray(raw)) return undefined;
  const out = raw.filter((item): item is string => typeof item === "string" && item.trim().length > 0);
  return out.length > 0 ? out : undefined;
}

function normalizeServerEntry(raw: unknown): CodemodeServerConfig | null {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
  const obj = raw as Record<string, unknown>;
  if (obj.enabled === false) return null;
  const out: CodemodeServerConfig = {};
  if (typeof obj.command === "string" && obj.command.trim()) {
    out.command = obj.command.trim();
    if (Array.isArray(obj.args)) {
      out.args = obj.args.filter((item): item is string => typeof item === "string");
    }
    const env = stringMap(obj.env);
    if (env) out.env = env;
    if (typeof obj.cwd === "string" && obj.cwd.trim()) out.cwd = obj.cwd.trim();
  }
  const urlVal = typeof obj.url === "string" && obj.url.trim()
    ? obj.url.trim()
    : typeof obj.httpUrl === "string" && obj.httpUrl.trim()
      ? obj.httpUrl.trim()
      : "";
  if (urlVal) {
    out.url = urlVal;
    const headers = stringMap(obj.headers);
    if (headers) out.headers = headers;
  }
  const toolsObj = obj.tools && typeof obj.tools === "object" && !Array.isArray(obj.tools)
    ? (obj.tools as Record<string, unknown>)
    : undefined;
  const includeTools =
    stringList(obj.includeTools) ??
    stringList(obj.include_tools) ??
    stringList(obj.enabled_tools) ??
    stringList(toolsObj?.include);
  if (includeTools) out.includeTools = includeTools;
  const excludeTools =
    stringList(obj.excludeTools) ??
    stringList(obj.exclude_tools) ??
    stringList(obj.disabled_tools) ??
    stringList(toolsObj?.exclude);
  if (excludeTools) out.excludeTools = excludeTools;

  return isValidServer(out) ? out : null;
}

function parseJsonServers(raw: unknown): Record<string, CodemodeServerConfig> {
  const out: Record<string, CodemodeServerConfig> = {};
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return out;
  for (const [name, value] of Object.entries(raw as Record<string, unknown>)) {
    const entry = normalizeServerEntry(value);
    if (entry && !isSelfServer(name, entry)) {
      out[name] = entry;
    }
  }
  return out;
}

function isPlainRecord(raw: unknown): raw is Record<string, unknown> {
  return Boolean(raw) && typeof raw === "object" && !Array.isArray(raw);
}

function looksLikeSingleOpenCodeServer(raw: unknown): boolean {
  if (!isPlainRecord(raw)) return false;
  return (
    raw.type === "local" ||
    raw.type === "remote" ||
    typeof raw.url === "string" ||
    Array.isArray(raw.command) ||
    typeof raw.command === "string"
  );
}

function isOpenCodeV2McpObject(mcp: Record<string, unknown>): boolean {
  return isPlainRecord(mcp.servers) && !looksLikeSingleOpenCodeServer(mcp.servers);
}

function parseOpenCodeServerEntry(name: string, value: unknown): CodemodeServerConfig | null {
  if (!isPlainRecord(value)) return null;
  if (value.enabled === false || value.disabled === true) return null;
  const entry: CodemodeServerConfig = {};
  if (typeof value.url === "string" && value.url.trim()) {
    entry.url = value.url.trim();
    const headers = stringMap(value.headers);
    if (headers) entry.headers = headers;
  } else if (Array.isArray(value.command) && value.command.length > 0 && typeof value.command[0] === "string") {
    entry.command = value.command[0].trim();
    if (value.command.length > 1) {
      entry.args = value.command.slice(1).filter((item): item is string => typeof item === "string");
    }
    const env = stringMap(value.environment ?? value.env);
    if (env) entry.env = env;
  } else if (typeof value.command === "string" && value.command.trim()) {
    entry.command = value.command.trim();
    if (Array.isArray(value.args)) {
      entry.args = value.args.filter((item): item is string => typeof item === "string");
    }
    const env = stringMap(value.environment ?? value.env);
    if (env) entry.env = env;
  }
  return isValidServer(entry) && !isSelfServer(name, entry) ? entry : null;
}

function parseOpenCodeMcp(raw: unknown): Record<string, CodemodeServerConfig> {
  const out: Record<string, CodemodeServerConfig> = {};
  if (!isPlainRecord(raw)) return out;
  const hasV2Servers = isOpenCodeV2McpObject(raw);
  if (hasV2Servers) {
    for (const [name, value] of Object.entries(raw.servers as Record<string, unknown>)) {
      const entry = parseOpenCodeServerEntry(name, value);
      if (entry) out[name] = entry;
    }
  }
  for (const [name, value] of Object.entries(raw)) {
    if (hasV2Servers && name === "servers") continue;
    const entry = parseOpenCodeServerEntry(name, value);
    if (entry && !out[name]) out[name] = entry;
  }
  return out;
}

function stripTomlComment(line: string): string {
  let inSingle = false;
  let inDouble = false;
  let escaped = false;
  for (let i = 0; i < line.length; i++) {
    const c = line[i]!;
    if (escaped) {
      escaped = false;
      continue;
    }
    if (inDouble && c === "\\") {
      escaped = true;
      continue;
    }
    if (c === "'" && !inDouble) {
      inSingle = !inSingle;
      continue;
    }
    if (c === '"' && !inSingle) {
      inDouble = !inDouble;
      continue;
    }
    if (c === "#" && !inSingle && !inDouble) {
      return line.slice(0, i);
    }
  }
  return line;
}

/**
 * YAML 1.2 comment stripping: `#` starts a comment ONLY outside quotes AND at
 * column 0 or immediately after whitespace (` ` or `\t`). Preserves URL
 * `#fragments` such as `https://example.com/mcp#v2`.
 */
function stripYamlComment(line: string): string {
  let inSingle = false;
  let inDouble = false;
  let escaped = false;
  for (let i = 0; i < line.length; i++) {
    const c = line[i]!;
    if (escaped) {
      escaped = false;
      continue;
    }
    if (inDouble && c === "\\") {
      escaped = true;
      continue;
    }
    if (c === "'" && !inDouble) {
      inSingle = !inSingle;
      continue;
    }
    if (c === '"' && !inSingle) {
      inDouble = !inDouble;
      continue;
    }
    if (c === "#" && !inSingle && !inDouble) {
      if (i === 0 || line[i - 1] === " " || line[i - 1] === "\t") {
        return line.slice(0, i);
      }
    }
  }
  return line;
}

function unquoteKey(raw: string): string {
  const s = raw.trim();
  if (s.length >= 2 && ((s.startsWith('"') && s.endsWith('"')) || (s.startsWith("'") && s.endsWith("'")))) {
    return s.slice(1, -1);
  }
  return s;
}

function parseTomlString(raw: string): string | null {
  const s = raw.trim();
  if (s.length < 2) return null;
  if (s.startsWith('"') && s.endsWith('"')) {
    try {
      const parsed = JSON.parse(s);
      return typeof parsed === "string" ? parsed : null;
    } catch {
      return null;
    }
  }
  if (s.startsWith("'") && s.endsWith("'")) {
    return s.slice(1, -1);
  }
  return null;
}

function splitList(raw: string): string[] {
  const parts: string[] = [];
  let start = 0;
  let inSingle = false;
  let inDouble = false;
  let escaped = false;
  for (let i = 0; i < raw.length; i++) {
    const c = raw[i]!;
    if (escaped) {
      escaped = false;
      continue;
    }
    if (inDouble && c === "\\") {
      escaped = true;
      continue;
    }
    if (c === "'" && !inDouble) inSingle = !inSingle;
    else if (c === '"' && !inSingle) inDouble = !inDouble;
    else if (c === "," && !inSingle && !inDouble) {
      const p = raw.slice(start, i).trim();
      if (p) parts.push(p);
      start = i + 1;
    }
  }
  const tail = raw.slice(start).trim();
  if (tail) parts.push(tail);
  return parts;
}

function splitTomlHeader(header: string): string[] {
  const parts: string[] = [];
  let start = 0;
  let inSingle = false;
  let inDouble = false;
  let escaped = false;
  for (let i = 0; i < header.length; i++) {
    const c = header[i]!;
    if (escaped) {
      escaped = false;
      continue;
    }
    if (inDouble && c === "\\") {
      escaped = true;
      continue;
    }
    if (c === "'" && !inDouble) inSingle = !inSingle;
    else if (c === '"' && !inSingle) inDouble = !inDouble;
    else if (c === "." && !inSingle && !inDouble) {
      parts.push(unquoteKey(header.slice(start, i)));
      start = i + 1;
    }
  }
  parts.push(unquoteKey(header.slice(start)));
  return parts;
}

type YamlFlowSpanResult =
  | { ok: true; endLineIdx: number; rawFlow: string; rawLines: string[] }
  | { ok: false; reason: string };

function scanYamlFlowSpan(lines: string[], startLineIdx: number, startCol: number): YamlFlowSpanResult {
  const opener = lines[startLineIdx]?.[startCol];
  if (opener !== "{" && opener !== "[") {
    return { ok: false, reason: "flow span must begin with '{' or '['" };
  }
  const stack: string[] = [];
  let inSingle = false;
  let inDouble = false;
  let escaped = false;
  const collected: string[] = [];

  for (let lineIdx = startLineIdx; lineIdx < lines.length; lineIdx++) {
    const line = lines[lineIdx]!;
    const from = lineIdx === startLineIdx ? startCol : 0;
    let sliceEnd = line.length;
    for (let i = from; i < line.length; i++) {
      const c = line[i]!;
      if (inDouble) {
        if (escaped) {
          escaped = false;
        } else if (c === "\\") {
          escaped = true;
        } else if (c === '"') {
          inDouble = false;
        }
        continue;
      }
      if (inSingle) {
        if (c === "'") {
          if (line[i + 1] === "'") {
            i++;
          } else {
            inSingle = false;
          }
        }
        continue;
      }
      if (c === '"') {
        inDouble = true;
        continue;
      }
      if (c === "'") {
        inSingle = true;
        continue;
      }
      if (c === "#" && (i === 0 || line[i - 1] === " " || line[i - 1] === "\t")) {
        sliceEnd = i;
        break;
      }
      if (c === "{" || c === "[") {
        stack.push(c);
      } else if (c === "}" || c === "]") {
        const expected = c === "}" ? "{" : "[";
        if (stack.length === 0 || stack[stack.length - 1] !== expected) {
          return { ok: false, reason: `mismatched '${c}' in YAML flow collection` };
        }
        stack.pop();
        if (stack.length === 0) {
          const rest = stripYamlComment(line.slice(i + 1)).trim();
          if (rest) {
            return { ok: false, reason: `unexpected trailing content after flow collection: ${rest}` };
          }
          collected.push(line.slice(from, i + 1));
          return {
            ok: true,
            endLineIdx: lineIdx,
            rawFlow: collected.join("\n"),
            rawLines: lines.slice(startLineIdx, lineIdx + 1),
          };
        }
      }
    }
    collected.push(line.slice(from, sliceEnd));
  }
  return { ok: false, reason: "unterminated YAML flow collection" };
}

function hasUnsupportedYamlToken(text: string): string | null {
  let inSingle = false;
  let inDouble = false;
  let escaped = false;
  for (let i = 0; i < text.length; i++) {
    const c = text[i]!;
    if (inDouble) {
      if (escaped) escaped = false;
      else if (c === "\\") escaped = true;
      else if (c === '"') inDouble = false;
      continue;
    }
    if (inSingle) {
      if (c === "'") {
        if (text[i + 1] === "'") i++;
        else inSingle = false;
      }
      continue;
    }
    if (c === '"') {
      inDouble = true;
      continue;
    }
    if (c === "'") {
      inSingle = true;
      continue;
    }
    if (c === "#" && (i === 0 || text[i - 1] === " " || text[i - 1] === "\t" || text[i - 1] === "\n")) {
      while (i < text.length && text[i] !== "\n") i++;
      continue;
    }
    const prev = i === 0 ? "" : text[i - 1]!;
    const atBoundary = i === 0 || prev === " " || prev === "\t" || prev === "\n" || prev === "\r" || prev === "{" || prev === "[" || prev === "," || prev === ":";
    if (!atBoundary) continue;
    const next = text[i + 1] ?? "";
    if (c === "&" && /[A-Za-z0-9_-]/.test(next)) return "YAML anchors (&) are not supported in merge";
    if (c === "*" && /[A-Za-z0-9_-]/.test(next)) return "YAML aliases (*) are not supported in merge";
    if (c === "!" && next !== "") return "YAML tags (!) are not supported in merge";
    if (c === "?" && (next === "" || next === " " || next === "\t" || next === "\n" || next === "\r")) {
      return "YAML explicit mapping keys (?) are not supported in merge";
    }
    if (c === "<" && next === "<" && (text[i + 2] === ":" || text[i + 2] === " ")) {
      return "YAML merge keys (<<) are not supported in merge";
    }
  }
  return null;
}

function splitYamlFlowItems(inner: string): { ok: true; items: string[] } | { ok: false; reason: string } {
  const items: string[] = [];
  let start = 0;
  let depth = 0;
  let inSingle = false;
  let inDouble = false;
  let escaped = false;
  for (let i = 0; i < inner.length; i++) {
    const c = inner[i]!;
    if (inDouble) {
      if (escaped) escaped = false;
      else if (c === "\\") escaped = true;
      else if (c === '"') inDouble = false;
      continue;
    }
    if (inSingle) {
      if (c === "'") {
        if (inner[i + 1] === "'") i++;
        else inSingle = false;
      }
      continue;
    }
    if (c === '"') {
      inDouble = true;
      continue;
    }
    if (c === "'") {
      inSingle = true;
      continue;
    }
    if (c === "#" && (i === 0 || inner[i - 1] === " " || inner[i - 1] === "\t" || inner[i - 1] === "\n")) {
      while (i < inner.length && inner[i] !== "\n") i++;
      continue;
    }
    if (c === "{" || c === "[") depth++;
    else if (c === "}" || c === "]") depth--;
    else if (c === "," && depth === 0) {
      const seg = inner.slice(start, i).trim();
      if (!seg) return { ok: false, reason: "empty item between commas in YAML flow collection" };
      items.push(seg);
      start = i + 1;
    }
  }
  if (depth !== 0 || inSingle || inDouble) {
    return { ok: false, reason: "unbalanced YAML flow collection" };
  }
  const tail = inner.slice(start).trim();
  if (tail) items.push(tail);
  return { ok: true, items };
}

function findYamlFlowColon(entry: string): number {
  let depth = 0;
  let inSingle = false;
  let inDouble = false;
  let escaped = false;
  let sawQuotedKeyEnd = false;
  for (let i = 0; i < entry.length; i++) {
    const c = entry[i]!;
    if (inDouble) {
      if (escaped) escaped = false;
      else if (c === "\\") escaped = true;
      else if (c === '"') {
        inDouble = false;
        if (depth === 0) sawQuotedKeyEnd = true;
      }
      continue;
    }
    if (inSingle) {
      if (c === "'") {
        if (entry[i + 1] === "'") i++;
        else {
          inSingle = false;
          if (depth === 0) sawQuotedKeyEnd = true;
        }
      }
      continue;
    }
    if (c === '"') {
      inDouble = true;
      continue;
    }
    if (c === "'") {
      inSingle = true;
      continue;
    }
    if (c === "{" || c === "[") depth++;
    else if (c === "}" || c === "]") depth--;
    else if (c === ":" && depth === 0) {
      const next = entry[i + 1];
      if (
        sawQuotedKeyEnd ||
        next === undefined ||
        next === " " ||
        next === "\t" ||
        next === "\n" ||
        next === "\r" ||
        next === "," ||
        next === "}" ||
        next === "]"
      ) {
        return i;
      }
    }
  }
  return -1;
}

type YamlFlowMappingEntry = {
  key: string;
  rawKey: string;
  rawValue: string;
};

function parseYamlFlowMappingEntries(
  rawFlow: string,
): { ok: true; entries: YamlFlowMappingEntry[] } | { ok: false; reason: string } {
  const trimmed = rawFlow.trim();
  if (!trimmed.startsWith("{") || !trimmed.endsWith("}")) {
    return { ok: false, reason: "expected YAML flow mapping enclosed in '{...}'" };
  }
  const unsupported = hasUnsupportedYamlToken(trimmed);
  if (unsupported) return { ok: false, reason: unsupported };
  const inner = trimmed.slice(1, -1).trim();
  if (!inner) return { ok: true, entries: [] };
  const split = splitYamlFlowItems(inner);
  if (!split.ok) return split;
  const entries: YamlFlowMappingEntry[] = [];
  for (const item of split.items) {
    const colon = findYamlFlowColon(item);
    if (colon <= 0) {
      return { ok: false, reason: `invalid YAML flow mapping entry: ${item.slice(0, 60)}` };
    }
    const rawKey = item.slice(0, colon).trim();
    const rawValue = item.slice(colon + 1).trim();
    const key = unquoteKey(rawKey);
    if (!key) return { ok: false, reason: "empty key in YAML flow mapping" };
    entries.push({ key, rawKey, rawValue });
  }
  return { ok: true, entries };
}

function parseYamlFlowValue(raw: string): unknown {
  const s = raw.trim();
  if (!s || s === "null" || s === "~") return null;
  if (s === "true") return true;
  if (s === "false") return false;
  if (s.startsWith("{") && s.endsWith("}")) {
    const parsed = parseYamlFlowMappingEntries(s);
    if (!parsed.ok) return undefined;
    const out: Record<string, unknown> = {};
    for (const entry of parsed.entries) {
      out[entry.key] = parseYamlFlowValue(entry.rawValue);
    }
    return out;
  }
  if (s.startsWith("[") && s.endsWith("]")) {
    const inner = s.slice(1, -1).trim();
    if (!inner) return [];
    const split = splitYamlFlowItems(inner);
    if (!split.ok) return undefined;
    return split.items.map((item) => parseYamlFlowValue(item));
  }
  if (s.length >= 2 && s.startsWith('"') && s.endsWith('"')) {
    try {
      return JSON.parse(s);
    } catch {
      return s.slice(1, -1);
    }
  }
  if (s.length >= 2 && s.startsWith("'") && s.endsWith("'")) {
    return s.slice(1, -1).replace(/''/g, "'");
  }
  return s;
}

function parseYamlFlowMap(val: string): Record<string, string> | undefined {
  const s = val.trim();
  if (!s.startsWith("{") || !s.endsWith("}")) return undefined;
  const parsed = parseYamlFlowMappingEntries(s);
  if (!parsed.ok || parsed.entries.length === 0) return undefined;
  const out: Record<string, string> = {};
  for (const entry of parsed.entries) {
    const v = parseYamlFlowValue(entry.rawValue);
    if (typeof v === "string") out[entry.key] = v;
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

export function parseCodexMcpServersToml(toml: string): Record<string, CodemodeServerConfig> {
  const raw: Record<string, Partial<CodemodeServerConfig>> = {};
  const disabled = new Set<string>();
  let current = "";
  let subSection: "" | "env" | "headers" | "env_headers" = "";
  let pendingArrayKey: "" | "args" | "env_vars" | "enabled_tools" | "disabled_tools" = "";
  let pendingArrayBuf = "";

  const applyArray = (cfg: Partial<CodemodeServerConfig>, key: string, rawVal: string) => {
    const inner = rawVal.slice(1, -1).trim();
    const items = inner
      ? splitList(inner).map(parseTomlString).filter((item): item is string => item !== null)
      : [];
    if (key === "args") {
      cfg.args = items;
    } else if (key === "env_vars") {
      for (const v of items) {
        if (v) cfg.env = { ...(cfg.env ?? {}), [v]: `\${${v}}` };
      }
    } else if (key === "enabled_tools" && items.length > 0) {
      cfg.includeTools = items;
    } else if (key === "disabled_tools" && items.length > 0) {
      cfg.excludeTools = items;
    }
  };

  for (const rawLine of toml.split(/\r?\n/)) {
    const line = stripTomlComment(rawLine).trim();
    if (!line) continue;

    if (pendingArrayKey) {
      pendingArrayBuf += " " + line;
      if (line.endsWith("]")) {
        if (current) {
          const cfg = (raw[current] ??= {});
          applyArray(cfg, pendingArrayKey, pendingArrayBuf.trim());
        }
        pendingArrayKey = "";
        pendingArrayBuf = "";
      }
      continue;
    }

    if (line.startsWith("[") && line.endsWith("]")) {
      const header = line.slice(1, -1).trim();
      current = "";
      subSection = "";
      const parts = splitTomlHeader(header);
      if (parts[0] === "mcp_servers") {
        if (parts.length === 2 && parts[1]) {
          current = parts[1];
        } else if (parts.length === 3 && parts[1]) {
          if (parts[2] === "env") {
            current = parts[1];
            subSection = "env";
          } else if (parts[2] === "headers" || parts[2] === "http_headers") {
            current = parts[1];
            subSection = "headers";
          } else if (parts[2] === "env_http_headers") {
            current = parts[1];
            subSection = "env_headers";
          }
        }
      }
      continue;
    }
    if (!current) continue;
    const eq = line.indexOf("=");
    if (eq <= 0) continue;
    const key = unquoteKey(line.slice(0, eq));
    const val = line.slice(eq + 1).trim();
    const cfg = (raw[current] ??= {});
    if (subSection === "env") {
      const s = parseTomlString(val);
      if (s !== null) cfg.env = { ...(cfg.env ?? {}), [key]: s };
      continue;
    }
    if (subSection === "headers") {
      const s = parseTomlString(val);
      if (s !== null) cfg.headers = { ...(cfg.headers ?? {}), [key]: s };
      continue;
    }
    if (subSection === "env_headers") {
      const s = parseTomlString(val);
      if (s !== null && s) cfg.headers = { ...(cfg.headers ?? {}), [key]: `\${${s}}` };
      continue;
    }
    if (key === "enabled" && val === "false") {
      disabled.add(current);
      continue;
    }
    if (key === "command") {
      const s = parseTomlString(val);
      if (s !== null) cfg.command = s;
    } else if (key === "url") {
      const s = parseTomlString(val);
      if (s !== null) cfg.url = s;
    } else if (key === "cwd") {
      const s = parseTomlString(val);
      if (s !== null) cfg.cwd = s;
    } else if (key === "bearer_token_env_var") {
      const s = parseTomlString(val);
      if (s !== null && s) {
        cfg.headers = { ...(cfg.headers ?? {}), Authorization: `Bearer \${${s}}` };
      }
    } else if (key === "args" || key === "env_vars" || key === "enabled_tools" || key === "disabled_tools") {
      if (val.startsWith("[") && val.endsWith("]")) {
        applyArray(cfg, key, val);
      } else if (val.startsWith("[")) {
        pendingArrayKey = key;
        pendingArrayBuf = val;
      }
    } else if (
      (key === "env" || key === "headers" || key === "http_headers" || key === "env_http_headers") &&
      val.startsWith("{") &&
      val.endsWith("}")
    ) {
      const inner = val.slice(1, -1).trim();
      if (inner) {
        const map: Record<string, string> = {};
        for (const item of splitList(inner)) {
          const itemEq = item.indexOf("=");
          if (itemEq <= 0) continue;
          const k = unquoteKey(item.slice(0, itemEq));
          const v = parseTomlString(item.slice(itemEq + 1));
          if (v !== null) map[k] = key === "env_http_headers" ? `\${${v}}` : v;
        }
        if (key === "env") cfg.env = { ...(cfg.env ?? {}), ...map };
        else cfg.headers = { ...(cfg.headers ?? {}), ...map };
      }
    }
  }
  const out: Record<string, CodemodeServerConfig> = {};
  for (const [name, cfg] of Object.entries(raw)) {
    if (disabled.has(name)) continue;
    const normalized = normalizeServerEntry(cfg);
    if (normalized && !isSelfServer(name, normalized)) {
      out[name] = normalized;
    }
  }
  return out;
}

export function parseHermesMcpServersYaml(yaml: string): Record<string, CodemodeServerConfig> {
  const raw: Record<string, Partial<CodemodeServerConfig>> = {};
  const disabled = new Set<string>();
  let inSection = false;
  let current = "";
  let serverIndent = -1;
  let propIndent = -1;
  let toolsIndent = -1;
  let mode: "" | "args" | "env" | "headers" | "tools" | "tools_include" | "tools_exclude" = "";
  let modeIndent = -1;

  const lines = yaml.split(/\r?\n/);
  for (let lineIdx = 0; lineIdx < lines.length; lineIdx++) {
    const rawLine = lines[lineIdx]!;
    const trimmed = stripYamlComment(rawLine).trim();
    if (!trimmed) continue;
    const indent = rawLine.length - rawLine.trimStart().length;
    if (indent === 0) {
      if (trimmed === "..." || trimmed === "---") {
        inSection = false;
        current = "";
        serverIndent = -1;
        propIndent = -1;
        toolsIndent = -1;
        mode = "";
        modeIndent = -1;
        continue;
      }
      const colon = trimmed.indexOf(":");
      const topKey = colon > 0 ? unquoteKey(trimmed.slice(0, colon)) : "";
      const topRest = colon > 0 ? trimmed.slice(colon + 1).trim() : "";
      if (topKey === "mcp_servers" && topRest.startsWith("{")) {
        const startCol = rawLine.indexOf("{", rawLine.indexOf(":") + 1);
        const span = startCol >= 0 ? scanYamlFlowSpan(lines, lineIdx, startCol) : { ok: false as const, reason: "missing '{'" };
        if (span.ok) {
          lineIdx = span.endLineIdx;
          const entries = parseYamlFlowMappingEntries(span.rawFlow);
          if (entries.ok) {
            for (const entry of entries.entries) {
              const parsed = parseYamlFlowValue(entry.rawValue);
              if (isPlainRecord(parsed)) {
                if (parsed.enabled === false) {
                  disabled.add(entry.key);
                } else {
                  const normalized = normalizeServerEntry(parsed);
                  if (normalized) raw[entry.key] = normalized;
                }
              }
            }
          }
        }
        inSection = false;
        current = "";
        serverIndent = -1;
        propIndent = -1;
        toolsIndent = -1;
        mode = "";
        modeIndent = -1;
        continue;
      }
      inSection = topKey === "mcp_servers" && topRest === "";
      current = "";
      serverIndent = -1;
      propIndent = -1;
      toolsIndent = -1;
      mode = "";
      modeIndent = -1;
      continue;
    }
    if (!inSection) continue;
    if ((serverIndent < 0 || indent === serverIndent) && !trimmed.startsWith("-")) {
      if (trimmed.endsWith(":")) {
        current = unquoteKey(trimmed.slice(0, -1));
        serverIndent = indent;
        propIndent = -1;
        toolsIndent = -1;
        mode = "";
        modeIndent = -1;
        continue;
      }
      const srvColon = trimmed.indexOf(":");
      if (srvColon > 0) {
        const srvName = unquoteKey(trimmed.slice(0, srvColon));
        const srvRest = trimmed.slice(srvColon + 1).trim();
        if (srvName && srvRest.startsWith("{")) {
          const startCol = rawLine.indexOf("{", rawLine.indexOf(":") + 1);
          const span = startCol >= 0 ? scanYamlFlowSpan(lines, lineIdx, startCol) : { ok: false as const, reason: "missing '{'" };
          if (span.ok) {
            lineIdx = span.endLineIdx;
            const parsed = parseYamlFlowValue(span.rawFlow);
            if (isPlainRecord(parsed)) {
              if (parsed.enabled === false) disabled.add(srvName);
              else {
                const normalized = normalizeServerEntry(parsed);
                if (normalized) raw[srvName] = normalized;
              }
            }
          }
          serverIndent = indent;
          current = "";
          propIndent = -1;
          toolsIndent = -1;
          mode = "";
          modeIndent = -1;
          continue;
        }
      }
    }
    if (!current || indent <= serverIndent) continue;
    const cfg = (raw[current] ??= {});

    // Exit nested block modes when indentation dedents back to or above the key that opened the mode.
    if (toolsIndent >= 0 && indent <= toolsIndent) {
      toolsIndent = -1;
    }
    if (mode && modeIndent >= 0 && indent <= modeIndent) {
      if ((mode === "tools_include" || mode === "tools_exclude") && toolsIndent >= 0 && indent > toolsIndent) {
        mode = "tools";
        modeIndent = toolsIndent;
      } else if (!(mode === "args" && trimmed.startsWith("- "))) {
        mode = "";
        modeIndent = -1;
      }
    }

    if (mode === "args" && trimmed.startsWith("- ")) {
      cfg.args = [...(cfg.args ?? []), unquoteKey(trimmed.slice(2).trim())];
      continue;
    }
    if (mode === "tools_include" && trimmed.startsWith("- ")) {
      cfg.includeTools = [...(cfg.includeTools ?? []), unquoteKey(trimmed.slice(2).trim())];
      continue;
    }
    if (mode === "tools_exclude" && trimmed.startsWith("- ")) {
      cfg.excludeTools = [...(cfg.excludeTools ?? []), unquoteKey(trimmed.slice(2).trim())];
      continue;
    }

    const colon = trimmed.indexOf(":");
    if (colon <= 0) continue;
    const key = unquoteKey(trimmed.slice(0, colon));
    const val = trimmed.slice(colon + 1).trim();

    if (mode === "env" && indent > modeIndent) {
      cfg.env = { ...(cfg.env ?? {}), [key]: unquoteKey(val) };
      continue;
    }
    if (mode === "headers" && indent > modeIndent) {
      cfg.headers = { ...(cfg.headers ?? {}), [key]: unquoteKey(val) };
      continue;
    }
    if ((mode === "tools" || mode === "tools_include" || mode === "tools_exclude") && indent > modeIndent) {
      if (key === "include") {
        if (!val) {
          mode = "tools_include";
          modeIndent = indent;
        } else if (val.startsWith("[") && val.endsWith("]")) {
          const inner = val.slice(1, -1).trim();
          cfg.includeTools = inner ? splitList(inner).map(unquoteKey) : [];
        }
        continue;
      }
      if (key === "exclude") {
        if (!val) {
          mode = "tools_exclude";
          modeIndent = indent;
        } else if (val.startsWith("[") && val.endsWith("]")) {
          const inner = val.slice(1, -1).trim();
          cfg.excludeTools = inner ? splitList(inner).map(unquoteKey) : [];
        }
        continue;
      }
    }

    if (propIndent < 0) propIndent = indent;
    if (indent > propIndent) continue;

    mode = "";
    modeIndent = -1;
    if (key === "enabled" && val.toLowerCase() === "false") {
      disabled.add(current);
    } else if (key === "command") {
      cfg.command = unquoteKey(val);
    } else if (key === "url") {
      cfg.url = unquoteKey(val);
    } else if (key === "cwd") {
      cfg.cwd = unquoteKey(val);
    } else if (key === "args") {
      if (!val) {
        mode = "args";
        modeIndent = indent;
      } else if (val.startsWith("[") && val.endsWith("]")) {
        const inner = val.slice(1, -1).trim();
        cfg.args = inner ? splitList(inner).map(unquoteKey) : [];
      }
    } else if (key === "env") {
      const inline = parseYamlFlowMap(val);
      if (inline) cfg.env = { ...(cfg.env ?? {}), ...inline };
      else if (!val) {
        mode = "env";
        modeIndent = indent;
      }
    } else if (key === "headers") {
      const inline = parseYamlFlowMap(val);
      if (inline) cfg.headers = { ...(cfg.headers ?? {}), ...inline };
      else if (!val) {
        mode = "headers";
        modeIndent = indent;
      }
    } else if (key === "tools") {
      if (!val) {
        mode = "tools";
        modeIndent = indent;
        toolsIndent = indent;
      }
    }
  }

  const out: Record<string, CodemodeServerConfig> = {};
  for (const [name, cfg] of Object.entries(raw)) {
    if (disabled.has(name)) continue;
    const normalized = normalizeServerEntry(cfg);
    if (normalized && !isSelfServer(name, normalized)) out[name] = normalized;
  }
  return out;
}

function expandHomeOverride(raw: string | undefined, homeDir: string, env: NodeJS.ProcessEnv): string | null {
  const trimmed = raw?.trim();
  if (!trimmed) return null;
  let expanded = trimmed.replace(/\$(\w+)|\$\{([^{}$]*)\}/g, (whole, bare, braced) => env[bare ?? braced] ?? whole);
  if (process.platform === "win32") {
    expanded = expanded.replace(/%([^%]+)%/g, (whole, name) => env[name] ?? whole);
  }
  if (homeDir) {
    expanded = expanded.replace(/^~(?=$|[\\/])/, homeDir);
  }
  return resolve(expanded);
}

function openCodeConfigPaths(
  workspaceDir: string,
  homeDir: string,
  env: NodeJS.ProcessEnv,
  trustWorkspace = false,
): string[] {
  const paths: string[] = [];
  if (homeDir) {
    const configDir = expandHomeOverride(env.OPENCODE_CONFIG_DIR, homeDir, env)
      ?? join(expandHomeOverride(env.XDG_CONFIG_HOME, homeDir, env) ?? join(homeDir, ".config"), "opencode");
    paths.push(join(configDir, "opencode.json"), join(configDir, "opencode.jsonc"));
  }
  const explicit = expandHomeOverride(env.OPENCODE_CONFIG, homeDir, env);
  if (explicit) paths.push(explicit);
  if (trustWorkspace && workspaceDir) {
    paths.push(join(workspaceDir, "opencode.json"), join(workspaceDir, "opencode.jsonc"));
  }
  return paths;
}

function kiloConfigPaths(
  workspaceDir: string,
  homeDir: string,
  env: NodeJS.ProcessEnv,
  trustWorkspace = false,
): string[] {
  const paths: string[] = [];
  if (homeDir) {
    const configDir = expandHomeOverride(env.KILO_CONFIG_DIR, homeDir, env)
      ?? join(expandHomeOverride(env.XDG_CONFIG_HOME, homeDir, env) ?? join(homeDir, ".config"), "kilo");
    paths.push(join(configDir, "kilo.json"), join(configDir, "kilo.jsonc"));
  }
  const explicit = expandHomeOverride(env.KILO_CONFIG, homeDir, env);
  if (explicit) paths.push(explicit);
  if (trustWorkspace && workspaceDir) {
    paths.push(join(workspaceDir, "kilo.json"), join(workspaceDir, "kilo.jsonc"));
  }
  return paths;
}

/**
 * Returns only the external MCP server names configured in OpenCode's own
 * config files. Used by `applyOpenCodeCodemodeOverlay` so we never inject
 * `{ enabled: false }` entries for foreign (Claude/Cursor/Codex) servers,
 * which would fail OpenCode's Zod discriminated-union config schema validation.
 */
export function discoverOpenCodeOwnServerNames(
  workspaceDir: string,
  homeDir: string,
  env: NodeJS.ProcessEnv = process.env,
  trustWorkspace = false,
): string[] {
  const names = new Set<string>();
  for (const p of openCodeConfigPaths(workspaceDir, homeDir, env, trustWorkspace)) {
    try {
      const doc = parseJsonRelaxed(readFileSync(p, "utf8")) as { mcp?: unknown };
      for (const name of Object.keys(parseOpenCodeMcp(doc?.mcp))) {
        names.add(name);
      }
    } catch { /* absent or unreadable */ }
  }
  return [...names];
}

/**
 * Returns only the external MCP server names configured in Kilo's own config
 * files so `applyKiloCodemodeOverlay` disables only Kilo-owned servers.
 */
export function discoverKiloOwnServerNames(
  workspaceDir: string,
  homeDir: string,
  env: NodeJS.ProcessEnv = process.env,
  trustWorkspace = false,
): string[] {
  const names = new Set<string>();
  for (const p of kiloConfigPaths(workspaceDir, homeDir, env, trustWorkspace)) {
    try {
      const doc = parseJsonRelaxed(readFileSync(p, "utf8")) as { mcp?: unknown };
      for (const name of Object.keys(parseOpenCodeMcp(doc?.mcp))) {
        names.add(name);
      }
    } catch { /* absent or unreadable */ }
  }
  return [...names];
}

/**
 * Discovers user MCP servers across all major coding agents (Cursor, OpenClaw,
 * Hermes, OpenCode, Kilo, Gemini CLI, Qwen Code, Codex, and Claude Code),
 * supporting both stdio commands and remote Streamable HTTP URLs while
 * excluding disabled and self-referential Caveman servers.
 *
 * Workspace-local executable configs (`.mcp.json`, `.codex/config.toml`,
 * `.gemini/settings.json`, `.qwen/settings.json`, `opencode.json*`, `kilo.json*`)
 * are SKIPPED by default (`trustWorkspace = false`) so untrusted checkouts
 * cannot execute arbitrary commands on `caveman wrap --codemode`. Pass
 * `trustWorkspace = true` (via `--trust-workspace-mcp` or
 * `CAVEMAN_TRUST_WORKSPACE_MCP=1`) to explicitly opt in for trusted repos.
 */
export function discoverCodemodeServers(
  workspaceDir: string,
  homeDir: string,
  env: NodeJS.ProcessEnv = process.env,
  trustWorkspace = false,
): Record<string, CodemodeServerConfig> {
  const merged: Record<string, CodemodeServerConfig> = {};
  const merge = (src: Record<string, CodemodeServerConfig>) => {
    for (const [name, cfg] of Object.entries(src)) {
      if (isValidServer(cfg) && !isSelfServer(name, cfg)) merged[name] = cfg;
    }
  };

  if (homeDir) {
    try {
      const cursorDoc = parseJsonRelaxed(readFileSync(join(homeDir, ".cursor", "mcp.json"), "utf8")) as {
        mcpServers?: unknown;
        servers?: unknown;
      };
      merge(parseJsonServers(cursorDoc?.servers));
      merge(parseJsonServers(cursorDoc?.mcpServers));
    } catch { /* absent */ }

    const openclawPath = expandHomeOverride(env.OPENCLAW_CONFIG_PATH, homeDir, env)
      ?? join(expandHomeOverride(env.OPENCLAW_STATE_DIR, homeDir, env) ?? join(homeDir, ".openclaw"), "openclaw.json");
    try {
      const openclawDoc = parseJsonRelaxed(readFileSync(openclawPath, "utf8")) as { mcp?: { servers?: unknown } };
      merge(parseJsonServers(openclawDoc?.mcp?.servers));
    } catch { /* absent */ }

    const hermesDir = expandHomeOverride(env.HERMES_HOME, homeDir, env)
      ?? (process.platform === "win32"
        ? join(env.LOCALAPPDATA?.trim() || join(homeDir, "AppData", "Local"), "hermes")
        : join(homeDir, ".hermes"));
    try {
      const hermesYaml = readFileSync(join(hermesDir, "config.yaml"), "utf8");
      merge(parseHermesMcpServersYaml(hermesYaml));
    } catch { /* absent */ }

    for (const p of openCodeConfigPaths(workspaceDir, homeDir, env, trustWorkspace)) {
      try {
        const opencodeDoc = parseJsonRelaxed(readFileSync(p, "utf8")) as { mcp?: unknown };
        merge(parseOpenCodeMcp(opencodeDoc?.mcp));
      } catch { /* absent */ }
    }

    for (const p of kiloConfigPaths(workspaceDir, homeDir, env, trustWorkspace)) {
      try {
        const kiloDoc = parseJsonRelaxed(readFileSync(p, "utf8")) as { mcp?: unknown };
        merge(parseOpenCodeMcp(kiloDoc?.mcp));
      } catch { /* absent */ }
    }

    const geminiHome = expandHomeOverride(env.GEMINI_CLI_HOME, homeDir, env) ?? homeDir;
    try {
      const geminiDoc = parseJsonRelaxed(readFileSync(join(geminiHome, ".gemini", "settings.json"), "utf8")) as {
        mcpServers?: unknown;
      };
      merge(parseJsonServers(geminiDoc?.mcpServers));
    } catch { /* absent */ }

    try {
      const qwenDoc = parseJsonRelaxed(readFileSync(join(homeDir, ".qwen", "settings.json"), "utf8")) as {
        mcpServers?: unknown;
      };
      merge(parseJsonServers(qwenDoc?.mcpServers));
    } catch { /* absent */ }

    const codexDir = expandHomeOverride(env.CODEX_HOME, homeDir, env) ?? join(homeDir, ".codex");
    try {
      const codexToml = readFileSync(join(codexDir, "config.toml"), "utf8");
      merge(parseCodexMcpServersToml(codexToml));
    } catch { /* absent */ }

    const claudePaths: string[] = [join(homeDir, ".claude.json")];
    const claudeDir = expandHomeOverride(env.CLAUDE_CONFIG_DIR, homeDir, env);
    if (claudeDir) claudePaths.push(join(claudeDir, ".claude.json"));
    for (const claudePath of claudePaths) {
      try {
        const claudeDoc = parseJsonRelaxed(readFileSync(claudePath, "utf8")) as {
          mcpServers?: unknown;
          projects?: Record<string, { mcpServers?: unknown }>;
        };
        merge(parseJsonServers(claudeDoc?.mcpServers));
        if (workspaceDir && claudeDoc?.projects && typeof claudeDoc.projects === "object") {
          const cleanWs = resolve(workspaceDir);
          for (const [projPath, proj] of Object.entries(claudeDoc.projects)) {
            if (resolve(projPath) === cleanWs) merge(parseJsonServers(proj?.mcpServers));
          }
        }
      } catch { /* absent */ }
    }
  }

  if (trustWorkspace && workspaceDir) {
    try {
      const projCodex = readFileSync(join(workspaceDir, ".codex", "config.toml"), "utf8");
      merge(parseCodexMcpServersToml(projCodex));
    } catch { /* absent */ }

    try {
      const projGemini = parseJsonRelaxed(readFileSync(join(workspaceDir, ".gemini", "settings.json"), "utf8")) as {
        mcpServers?: unknown;
      };
      merge(parseJsonServers(projGemini?.mcpServers));
    } catch { /* absent */ }

    try {
      const projQwen = parseJsonRelaxed(readFileSync(join(workspaceDir, ".qwen", "settings.json"), "utf8")) as {
        mcpServers?: unknown;
      };
      merge(parseJsonServers(projQwen?.mcpServers));
    } catch { /* absent */ }

    try {
      const projectDoc = parseJsonRelaxed(readFileSync(join(workspaceDir, ".mcp.json"), "utf8")) as {
        mcpServers?: unknown;
        servers?: unknown;
      };
      merge(parseJsonServers(projectDoc?.servers));
      merge(parseJsonServers(projectDoc?.mcpServers));
    } catch { /* absent */ }
  }

  return merged;
}

/**
 * Writes an ephemeral `codemode-servers.json` file (0600) in `dir` and returns
 * its path. Used inside ephemeral wrap packs so cleanup happens on exit.
 */
export function writeEphemeralCodemodeConfig(dir: string, servers: Record<string, CodemodeServerConfig>): string {
  const path = join(dir, "codemode-servers.json");
  writeFileSync(path, JSON.stringify({ mcpServers: servers }, null, 2) + "\n", { mode: 0o600 });
  return path;
}

/**
 * Strips `[mcp_servers.*]` tables from an ephemeral Codex `config.toml` when
 * `--codemode` bridges those servers behind `caveman-codemode`, preventing
 * Codex from loading 50+ downstream schemas twice.
 */
export function stripExternalCodexMcpTables(toml: string): string {
  const out: string[] = [];
  let skipping = false;
  for (const line of toml.split(/\r?\n/)) {
    const trimmed = stripTomlComment(line).trim();
    if (trimmed.startsWith("[") && trimmed.endsWith("]")) {
      const header = trimmed.slice(1, -1).trim();
      const parts = splitTomlHeader(header);
      skipping = parts[0] === "mcp_servers";
    }
    if (!skipping) out.push(line);
  }
  return out.join("\n").replace(/\n{3,}/g, "\n\n").trim();
}

/**
 * Builds an ephemeral Gemini CLI system settings file for
 * `GEMINI_CLI_SYSTEM_SETTINGS_PATH`. Preserves any existing system settings
 * (when `baseSystemSettingsPath` is non-empty), registers `caveman-codemode`,
 * and sets `mcp.allowed = ["caveman-codemode"]` so Gemini's additive settings
 * merge does not double-load bridged user/workspace MCP servers.
 */
export function writeGeminiCodemodeSettingsOverlay(
  outDir: string,
  baseSystemSettingsPath: string,
  cmEntry: CodemodeLaunchEntry,
): string {
  let base: Record<string, unknown> = {};
  if (baseSystemSettingsPath) {
    try {
      const parsed = parseJsonRelaxed(readFileSync(baseSystemSettingsPath, "utf8"));
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) base = parsed as Record<string, unknown>;
    } catch { /* no existing system settings */ }
  }
  const existingMcp =
    base.mcp && typeof base.mcp === "object" && !Array.isArray(base.mcp)
      ? { ...(base.mcp as Record<string, unknown>) }
      : {};
  const overlaid = {
    ...base,
    mcp: {
      ...existingMcp,
      allowed: ["caveman-codemode"],
    },
    mcpServers: {
      "caveman-codemode": {
        command: cmEntry.command,
        args: cmEntry.args,
      },
    },
  };
  const outPath = join(outDir, "gemini-codemode-settings.json");
  writeFileSync(outPath, JSON.stringify(overlaid, null, 2) + "\n", { mode: 0o600 });
  return outPath;
}

export type OpenCodeMcpSchema = "v1" | "v2";

function detectOpenCodeDocSchema(doc: unknown): OpenCodeMcpSchema | "unknown" {
  if (!isPlainRecord(doc)) return "unknown";
  if (isPlainRecord(doc.mcp) && isOpenCodeV2McpObject(doc.mcp)) return "v2";
  if (isPlainRecord(doc.providers) && !isPlainRecord(doc.provider)) return "v2";
  if (isPlainRecord(doc.mcp)) {
    for (const value of Object.values(doc.mcp)) {
      if (looksLikeSingleOpenCodeServer(value)) return "v1";
    }
  }
  if (isPlainRecord(doc.provider)) return "v1";
  return "unknown";
}

/**
 * Determines whether OpenCode uses V2 (`mcp.servers.<name>.disabled`) or V1
 * (`mcp.<name>.enabled`) MCP schema for the active launch.
 *
 * Host binary major version takes precedence because OpenCode V2 drops
 * V1-style enabled-only overrides (`{ enabled: false }`) during normalization
 * even when the user's on-disk config still uses the flat V1 shape.
 */
export function detectOpenCodeConfigSchema(
  workspaceDir: string,
  homeDir: string,
  env: NodeJS.ProcessEnv = process.env,
  trustWorkspace = false,
  hostMajor?: number,
): OpenCodeMcpSchema | "unknown" {
  if (typeof hostMajor === "number" && Number.isFinite(hostMajor)) {
    return hostMajor >= 2 ? "v2" : "v1";
  }
  let sawV1 = false;
  if (env.OPENCODE_CONFIG_CONTENT) {
    try {
      const schema = detectOpenCodeDocSchema(parseJsonRelaxed(env.OPENCODE_CONFIG_CONTENT));
      if (schema === "v2") return "v2";
      if (schema === "v1") sawV1 = true;
    } catch { /* unreadable inline config */ }
  }
  for (const p of openCodeConfigPaths(workspaceDir, homeDir, env, trustWorkspace)) {
    try {
      const schema = detectOpenCodeDocSchema(parseJsonRelaxed(readFileSync(p, "utf8")));
      if (schema === "v2") return "v2";
      if (schema === "v1") sawV1 = true;
    } catch { /* absent or unreadable */ }
  }
  return sawV1 ? "v1" : "unknown";
}

/**
 * Injects `caveman-codemode` into an ephemeral `OPENCODE_CONFIG_CONTENT` JSON
 * payload and disables already-bridged OpenCode-owned MCP servers in the
 * overlay ONLY (never touching `~/.config/opencode/opencode.json`).
 *
 * Recognizes both OpenCode V1 (`mcp.<name> = { enabled: false }`) and OpenCode
 * V2 (`mcp.servers.<name> = { disabled: true }`) formats. Throws when V2 is
 * requested but the existing overlay cannot safely carry `mcp.servers`.
 */
export function applyOpenCodeCodemodeOverlay(
  rawConfigJson: string | undefined,
  bridgedOpenCodeServerNames: readonly string[],
  cmEntry: CodemodeLaunchEntry,
  opts?: { schema?: OpenCodeMcpSchema },
): string {
  let doc: Record<string, unknown> = {};
  if (rawConfigJson && rawConfigJson.trim()) {
    try {
      const parsed = parseJsonRelaxed(rawConfigJson);
      if (isPlainRecord(parsed)) {
        doc = parsed;
      } else if (opts?.schema === "v2") {
        throw new Error("OpenCode V2 overlay cannot disable bridged MCP servers: OPENCODE_CONFIG_CONTENT must be a JSON object");
      }
    } catch (err) {
      if (opts?.schema === "v2") {
        const msg = err instanceof Error ? err.message : String(err);
        throw new Error(`OpenCode V2 overlay cannot disable bridged MCP servers: ${msg}`);
      }
    }
  }
  const detected = detectOpenCodeDocSchema(doc);
  const schema: OpenCodeMcpSchema = opts?.schema ?? (detected === "v2" ? "v2" : "v1");

  if (schema === "v2") {
    if (doc.mcp !== undefined && !isPlainRecord(doc.mcp)) {
      throw new Error("OpenCode V2 overlay cannot disable bridged MCP servers: existing mcp field is not a JSON object");
    }
    const existingMcp = isPlainRecord(doc.mcp) ? { ...doc.mcp } : {};
    if (existingMcp.servers !== undefined && !isPlainRecord(existingMcp.servers)) {
      throw new Error("OpenCode V2 overlay cannot disable bridged MCP servers: existing mcp.servers field is not a JSON object");
    }
    const servers = isPlainRecord(existingMcp.servers) ? { ...existingMcp.servers } : {};
    for (const name of bridgedOpenCodeServerNames) {
      const prev = isPlainRecord(servers[name]) ? servers[name] : {};
      servers[name] = { ...prev, disabled: true };
    }
    servers["caveman-codemode"] = {
      type: "local",
      command: [cmEntry.command, ...cmEntry.args],
      disabled: false,
    };
    existingMcp.servers = servers;
    return JSON.stringify({ ...doc, mcp: existingMcp });
  }

  const existingMcp = isPlainRecord(doc.mcp) ? { ...doc.mcp } : {};
  for (const name of bridgedOpenCodeServerNames) {
    existingMcp[name] = { enabled: false };
  }
  existingMcp["caveman-codemode"] = {
    type: "local",
    command: [cmEntry.command, ...cmEntry.args],
    enabled: true,
  };
  return JSON.stringify({ ...doc, mcp: existingMcp });
}

/**
 * Injects `mcp["caveman-codemode"]` into an ephemeral `KILO_CONFIG_CONTENT`
 * JSON payload and disables already-bridged Kilo-owned MCP servers without
 * mutating user settings on disk.
 */
export function applyKiloCodemodeOverlay(
  rawConfigJson: string | undefined,
  cmEntry: CodemodeLaunchEntry,
  bridgedKiloServerNames: readonly string[] = [],
): string {
  let doc: Record<string, unknown> = {};
  if (rawConfigJson) {
    try {
      const parsed = parseJsonRelaxed(rawConfigJson);
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) doc = parsed as Record<string, unknown>;
    } catch { /* fallback */ }
  }
  const existingMcp =
    doc.mcp && typeof doc.mcp === "object" && !Array.isArray(doc.mcp)
      ? { ...(doc.mcp as Record<string, unknown>) }
      : {};
  for (const name of bridgedKiloServerNames) {
    existingMcp[name] = { enabled: false };
  }
  existingMcp["caveman-codemode"] = {
    type: "local",
    command: [cmEntry.command, ...cmEntry.args],
    enabled: true,
  };
  return JSON.stringify({ ...doc, mcp: existingMcp });
}

/**
 * Replaces ONLY `mcp.servers` inside an already-ephemeral OpenClaw wrap config
 * file (`OPENCLAW_CONFIG_PATH`), never touching `~/.openclaw/openclaw.json`.
 */
export function patchEphemeralOpenClawCodemode(
  ephemeralConfigPath: string,
  cmEntry: CodemodeLaunchEntry,
): void {
  let doc: Record<string, unknown> = {};
  try {
    const parsed = parseJsonRelaxed(readFileSync(ephemeralConfigPath, "utf8"));
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) doc = parsed as Record<string, unknown>;
  } catch { /* fresh */ }
  const mcp =
    doc.mcp && typeof doc.mcp === "object" && !Array.isArray(doc.mcp)
      ? { ...(doc.mcp as Record<string, unknown>) }
      : {};
  mcp.servers = {
    "caveman-codemode": {
      command: cmEntry.command,
      args: cmEntry.args,
    },
  };
  writeFileSync(ephemeralConfigPath, JSON.stringify({ ...doc, mcp }, null, 2) + "\n", { mode: 0o600 });
}

/**
 * Replaces ONLY `mcpServers` and sets `mcp.allowed = ["caveman-codemode"]`
 * inside an already-ephemeral Qwen Code system settings overlay
 * (`QWEN_CODE_SYSTEM_SETTINGS_PATH`), preventing double-loading of bridged
 * user/workspace MCP servers while preserving auth/model/security policies.
 */
export function patchEphemeralQwenCodemode(
  ephemeralSystemSettingsPath: string,
  cmEntry: CodemodeLaunchEntry,
): void {
  let doc: Record<string, unknown> = {};
  try {
    const parsed = parseJsonRelaxed(readFileSync(ephemeralSystemSettingsPath, "utf8"));
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) doc = parsed as Record<string, unknown>;
  } catch { /* fresh */ }
  const existingMcp =
    doc.mcp && typeof doc.mcp === "object" && !Array.isArray(doc.mcp)
      ? { ...(doc.mcp as Record<string, unknown>) }
      : {};
  delete existingMcp.excluded;
  const next = {
    ...doc,
    mcp: {
      ...existingMcp,
      allowed: ["caveman-codemode"],
    },
    mcpServers: {
      "caveman-codemode": {
        command: cmEntry.command,
        args: cmEntry.args,
      },
    },
  };
  writeFileSync(ephemeralSystemSettingsPath, JSON.stringify(next, null, 2) + "\n", { mode: 0o600 });
}

function stripHermesMcpServersSection(sourceYaml: string): string[] {
  const lines = sourceYaml.split(/\r?\n/);
  const kept: string[] = [];
  let skipping = false;
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]!;
    const trimmed = stripYamlComment(line).trim();
    const indent = line.length - line.trimStart().length;
    if (trimmed && indent === 0) {
      if (trimmed === "..." || trimmed === "---") {
        skipping = false;
      } else {
        const colon = trimmed.indexOf(":");
        const key = colon > 0 ? unquoteKey(trimmed.slice(0, colon)) : "";
        if (key === "mcp_servers") {
          const rest = trimmed.slice(colon + 1).trim();
          if (rest.startsWith("{") || rest.startsWith("[")) {
            const openIdx = line.indexOf(rest[0]!, line.indexOf(":") + 1);
            const span = openIdx >= 0 ? scanYamlFlowSpan(lines, i, openIdx) : { ok: false as const, reason: "" };
            if (span.ok) {
              i = span.endLineIdx;
              skipping = false;
              continue;
            }
          }
          skipping = true;
          continue;
        }
        skipping = false;
      }
    }
    if (!skipping) kept.push(line);
  }
  return kept;
}

/**
 * Strips any top-level `mcp_servers:` YAML entry (block style, single-line or
 * multi-line flow style `{...}`, inline `null` / `~`, and quoted
 * `"mcp_servers":`) from an ephemeral Hermes `config.yaml` and inserts
 * `caveman-codemode` before any trailing `...` YAML document-end marker,
 * leaving auth/models/plugins untouched and keeping `ruamel.yaml` happy.
 */
export function rewriteHermesCodemodeYaml(sourceYaml: string, cmEntry: CodemodeLaunchEntry): string {
  const kept = stripHermesMcpServersSection(sourceYaml);

  const block = [
    "mcp_servers:",
    "  caveman-codemode:",
    `    command: ${JSON.stringify(cmEntry.command)}`,
    `    args: [${cmEntry.args.map((a) => JSON.stringify(a)).join(", ")}]`,
  ].join("\n");

  // If the source document has a top-level `...` document-end marker, insert
  // `mcp_servers:` before it so `ruamel.yaml` parses a single valid document.
  const docEndIdx = kept.findIndex((line) => {
    const t = stripYamlComment(line).trim();
    return t === "..." && line.length - line.trimStart().length === 0;
  });
  if (docEndIdx >= 0) {
    const before = kept.slice(0, docEndIdx).join("\n").replace(/\n{3,}/g, "\n\n").trimEnd();
    const after = kept.slice(docEndIdx).join("\n").trimEnd();
    return before ? `${before}\n\n${block}\n${after}\n` : `${block}\n${after}\n`;
  }

  const base = kept.join("\n").replace(/\n{3,}/g, "\n\n").trimEnd();
  return base ? `${base}\n\n${block}\n` : `${block}\n`;
}

const HERMES_CODEMODE_BASELINE_FILE = ".caveman-codemode-baseline.yaml";

type ExtractedHermesMcpServers =
  | {
      ok: true;
      kind: "absent" | "empty" | "block" | "flow";
      entries: Map<string, string[]>;
      rawFlowLines?: string[];
    }
  | { ok: false; reason: string };

function formatFlowEntryAsBlockLines(entry: YamlFlowMappingEntry): string[] {
  const valueLines = entry.rawValue.split(/\r?\n/);
  if (valueLines.length === 1) {
    return [`  ${entry.rawKey}: ${valueLines[0]}`];
  }
  const out = [`  ${entry.rawKey}: ${valueLines[0]}`];
  for (let i = 1; i < valueLines.length; i++) {
    const ln = valueLines[i]!;
    out.push(ln.trim() ? `    ${ln.trimStart()}` : "");
  }
  return out;
}

function extractHermesMcpServers(yaml: string): ExtractedHermesMcpServers {
  const blocks = new Map<string, string[]>();
  let seenSection = false;
  let kind: "absent" | "empty" | "block" | "flow" = "absent";
  let rawFlowLines: string[] | undefined;
  let inSection = false;
  let serverIndent = -1;
  let currentName = "";
  let currentLines: string[] = [];

  const flush = () => {
    if (currentName && currentLines.length > 0) {
      while (currentLines.length > 1 && currentLines[currentLines.length - 1]!.trim() === "") {
        currentLines.pop();
      }
      blocks.set(currentName, currentLines);
    }
    currentName = "";
    currentLines = [];
  };

  const lines = yaml.split(/\r?\n/);
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]!;
    const trimmed = stripYamlComment(line).trim();
    const indent = line.length - line.trimStart().length;
    if (trimmed && indent === 0) {
      if (trimmed === "..." || trimmed === "---") {
        if (inSection) {
          flush();
          inSection = false;
        }
        continue;
      }
      const colon = trimmed.indexOf(":");
      const key = colon > 0 ? unquoteKey(trimmed.slice(0, colon)) : "";
      if (key === "mcp_servers") {
        if (seenSection) {
          return { ok: false, reason: "duplicate top-level mcp_servers sections" };
        }
        seenSection = true;
        const rest = trimmed.slice(colon + 1).trim();
        if (rest === "" || rest === "null" || rest === "~") {
          inSection = rest === "";
          kind = "empty";
          serverIndent = -1;
          continue;
        }
        if (rest.startsWith("{")) {
          const openIdx = line.indexOf("{", line.indexOf(":") + 1);
          const span = openIdx >= 0 ? scanYamlFlowSpan(lines, i, openIdx) : { ok: false as const, reason: "missing '{'" };
          if (!span.ok) return { ok: false, reason: span.reason };
          const parsed = parseYamlFlowMappingEntries(span.rawFlow);
          if (!parsed.ok) return { ok: false, reason: parsed.reason };
          i = span.endLineIdx;
          if (parsed.entries.length === 0) {
            kind = "empty";
          } else {
            kind = "flow";
            rawFlowLines = span.rawLines;
            for (const entry of parsed.entries) {
              blocks.set(entry.key, formatFlowEntryAsBlockLines(entry));
            }
          }
          inSection = false;
          continue;
        }
        return { ok: false, reason: `unsupported mcp_servers value shape: ${rest.slice(0, 60)}` };
      }
      if (inSection) {
        flush();
        inSection = false;
      }
      continue;
    }
    if (!inSection) continue;
    if (!trimmed) {
      if (currentName) currentLines.push(line);
      continue;
    }
    const unsupported = hasUnsupportedYamlToken(trimmed);
    if (unsupported) return { ok: false, reason: unsupported };
    if (serverIndent < 0) serverIndent = indent;
    if (indent === serverIndent) {
      flush();
      if (trimmed.startsWith("-") || trimmed.startsWith("?")) {
        return { ok: false, reason: `unsupported block entry under mcp_servers: ${trimmed.slice(0, 40)}` };
      }
      const colon = trimmed.indexOf(":");
      if (colon <= 0) {
        return { ok: false, reason: `unparseable mcp_servers entry: ${trimmed.slice(0, 40)}` };
      }
      currentName = unquoteKey(trimmed.slice(0, colon));
      const rest = trimmed.slice(colon + 1).trim();
      if (rest.startsWith("{") || rest.startsWith("[")) {
        const openIdx = line.indexOf(rest[0]!, line.indexOf(":") + 1);
        const span = openIdx >= 0 ? scanYamlFlowSpan(lines, i, openIdx) : { ok: false as const, reason: "missing opener" };
        if (!span.ok) return { ok: false, reason: span.reason };
        const flowUnsupported = hasUnsupportedYamlToken(span.rawFlow);
        if (flowUnsupported) return { ok: false, reason: flowUnsupported };
        currentLines = span.rawLines;
        i = span.endLineIdx;
        kind = "block";
        flush();
        continue;
      }
      currentLines = [line];
      kind = "block";
      continue;
    }
    if (currentName && indent > serverIndent) {
      currentLines.push(line);
    } else {
      return { ok: false, reason: "unexpected indentation inside mcp_servers" };
    }
  }
  if (inSection) flush();
  if (blocks.size > 0 && kind === "empty") kind = "block";
  return { ok: true, kind, entries: blocks, ...(rawFlowLines ? { rawFlowLines } : {}) };
}

/**
 * Merges in-session Hermes `config.yaml` edits (e.g., `hermes mcp add ...` or
 * model/provider changes) back onto the user's pre-wrap `config.yaml` while
 * removing the ephemeral `caveman-codemode` overlay entry and preserving all
 * pre-existing user `mcp_servers` entries (both block-style and flow-style)
 * that were masked during the wrap.
 *
 * Throws if either input contains an `mcp_servers` shape that cannot be safely
 * preserved, so callers leave the original file untouched and warn.
 */
export function mergeHermesConfigEdits(editedYaml: string, originalYaml: string): string {
  const editedExtracted = extractHermesMcpServers(editedYaml);
  if (!editedExtracted.ok) {
    throw new Error(`cannot preserve edited Hermes mcp_servers: ${editedExtracted.reason}`);
  }
  const origExtracted = extractHermesMcpServers(originalYaml);
  if (!origExtracted.ok) {
    throw new Error(`cannot preserve original Hermes mcp_servers: ${origExtracted.reason}`);
  }

  const editedEntries = editedExtracted.entries;
  editedEntries.delete("caveman-codemode");
  const origEntries = origExtracted.entries;

  const kept = stripHermesMcpServersSection(editedYaml);

  const insertBlock = (blockText: string): string => {
    const docEndIdx = kept.findIndex((line) => {
      const t = stripYamlComment(line).trim();
      return t === "..." && line.length - line.trimStart().length === 0;
    });
    if (docEndIdx >= 0) {
      const before = kept.slice(0, docEndIdx).join("\n").replace(/\n{3,}/g, "\n\n").trimEnd();
      const after = kept.slice(docEndIdx).join("\n").trimEnd();
      return before ? `${before}\n\n${blockText}\n${after}\n` : `${blockText}\n${after}\n`;
    }
    const base = kept.join("\n").replace(/\n{3,}/g, "\n\n").trimEnd();
    return base ? `${base}\n\n${blockText}\n` : `${blockText}\n`;
  };

  if (
    editedEntries.size === 0 &&
    origExtracted.kind === "flow" &&
    origExtracted.rawFlowLines &&
    !origEntries.has("caveman-codemode")
  ) {
    return insertBlock(origExtracted.rawFlowLines.join("\n"));
  }

  const mergedServerLines: string[] = [];
  for (const [name, lines] of origEntries) {
    if (!editedEntries.has(name)) {
      mergedServerLines.push(...lines);
    }
  }
  for (const [, lines] of editedEntries) {
    mergedServerLines.push(...lines);
  }

  if (mergedServerLines.length === 0) {
    const base = kept.join("\n").replace(/\n{3,}/g, "\n\n").trimEnd();
    return base ? `${base}\n` : "";
  }

  return insertBlock(["mcp_servers:", ...mergedServerLines].join("\n"));
}

/**
 * Builds an ephemeral `HERMES_HOME` directory linking user plugins/auth/skills/
 * sessions/state into `outDir` and replacing ONLY `mcp_servers:` in the
 * ephemeral `config.yaml`. Uses Windows junctions for directories when needed.
 */
export function buildHermesEphemeralCodemodeHome(
  sourceHome: string,
  outDir: string,
  cmEntry: CodemodeLaunchEntry,
): string {
  let sourceYaml = "";
  try {
    sourceYaml = readFileSync(join(sourceHome, "config.yaml"), "utf8");
  } catch { /* no existing config.yaml */ }
  const overlayYaml = rewriteHermesCodemodeYaml(sourceYaml, cmEntry);
  writeFileSync(join(outDir, "config.yaml"), overlayYaml, { mode: 0o600 });
  writeFileSync(join(outDir, HERMES_CODEMODE_BASELINE_FILE), overlayYaml, { mode: 0o600 });
  if (existsSync(sourceHome)) {
    try {
      for (const entry of readdirSync(sourceHome)) {
        if (entry === "config.yaml" || entry === "codemode-servers.json" || entry === HERMES_CODEMODE_BASELINE_FILE) continue;
        const src = join(sourceHome, entry);
        const dst = join(outDir, entry);
        if (!existsSync(dst)) {
          try {
            if (process.platform === "win32") {
              const isDir = statSync(src).isDirectory();
              symlinkSync(src, dst, isDir ? "junction" : "file");
            } else {
              symlinkSync(src, dst);
            }
          } catch { /* ignore unreadable/unsupported entries */ }
        }
      }
    } catch { /* ignore unreadable sourceHome */ }
  }
  return outDir;
}

/**
 * Persists any non-symlink files or directories created or atomically replaced
 * inside an ephemeral `HERMES_HOME` (such as rotated OAuth `auth.json`, newly
 * created `state.db*`, `.env`, `SOUL.md`, `memories/`, `sessions/`, `cron/`)
 * back into `sourceHome` before the ephemeral directory is removed.
 * If the user modified `config.yaml` during the session (for example via
 * `hermes mcp add`), merges those edits back while removing the ephemeral
 * `caveman-codemode` overlay and keeping pre-existing `mcp_servers` intact.
 * Unmodified `config.yaml` leaves `sourceHome/config.yaml` byte-identical.
 */
export function syncBackHermesEphemeralHome(outDir: string, sourceHome: string): void {
  if (!outDir || !sourceHome || !existsSync(outDir)) return;
  try {
    const entries = readdirSync(outDir);
    for (const entry of entries) {
      if (entry === "codemode-servers.json" || entry === HERMES_CODEMODE_BASELINE_FILE) continue;
      if (entry === "config.yaml") {
        try {
          const baselinePath = join(outDir, HERMES_CODEMODE_BASELINE_FILE);
          if (!existsSync(baselinePath)) continue;
          const baselineYaml = readFileSync(baselinePath, "utf8");
          const currentYaml = readFileSync(join(outDir, "config.yaml"), "utf8");
          if (currentYaml === baselineYaml) continue;
          let origYaml = "";
          try {
            origYaml = readFileSync(join(sourceHome, "config.yaml"), "utf8");
          } catch { /* fresh config.yaml */ }
          const merged = mergeHermesConfigEdits(currentYaml, origYaml);
          mkdirSync(sourceHome, { recursive: true });
          const dst = join(sourceHome, "config.yaml");
          const tmp = `${dst}.caveman-sync-${process.pid}-${Date.now()}`;
          writeFileSync(tmp, merged, { mode: 0o600 });
          renameSync(tmp, dst);
        } catch (err) {
          const reason = err instanceof Error ? err.message : String(err);
          process.stderr.write(`caveman: warning: Hermes config.yaml sync-back skipped (${reason}); original config.yaml left untouched\n`);
        }
        continue;
      }
      const src = join(outDir, entry);
      let lst;
      try {
        lst = lstatSync(src);
      } catch {
        continue;
      }
      // Symlinks point directly at sourceHome — mutations through them already landed.
      if (lst.isSymbolicLink()) continue;
      try {
        mkdirSync(sourceHome, { recursive: true });
      } catch { /* ignore */ }
      const dst = join(sourceHome, entry);
      if (lst.isDirectory()) {
        try {
          cpSync(src, dst, { recursive: true, force: true });
        } catch { /* best-effort */ }
      } else if (lst.isFile()) {
        try {
          const tmp = `${dst}.caveman-sync-${process.pid}-${Date.now()}`;
          copyFileSync(src, tmp);
          renameSync(tmp, dst);
        } catch {
          try {
            copyFileSync(src, dst);
          } catch { /* best-effort */ }
        }
      }
    }
  } catch { /* best-effort on exit */ }
}
