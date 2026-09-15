// Capture analysis: turn a sink capture record into the numbers the report
// prints. All sizes are JSON-serialized character counts of what the harness
// actually sent — nothing is inferred here except the chars themselves.

const LLM_KINDS = new Set([
  "anthropic-messages",
  "openai-chat",
  "openai-responses",
  "gemini-generatecontent",
]);

const jsonChars = (value) => (value === undefined || value === null ? 0 : JSON.stringify(value).length);

const isSystemRole = (m) => m && (m.role === "system" || m.role === "developer");

// A field present in an unexpected shape is reported as unparsed, never
// silently counted as zero — a false "0 tools" reads as a real measurement.
function expectArray(value, field) {
  if (value === undefined || value === null) return [];
  if (!Array.isArray(value)) throw new Error(`${field} is not an array (got ${typeof value})`);
  return value;
}

function anthropicParts(body) {
  const tools = expectArray(body.tools, "tools").map((t) => ({ name: t?.name ?? t?.type ?? "?", chars: jsonChars(t) }));
  // The fixed instruction payload arrives as `system` AND, in some harnesses,
  // as system-role entries inside `messages` — count both or the flagship row
  // under-reports its own prefix.
  const messages = expectArray(body.messages, "messages");
  const sysMessages = messages.filter(isSystemRole);
  const rest = messages.filter((m) => !isSystemRole(m));
  return {
    system_chars: jsonChars(body.system) + (sysMessages.length ? jsonChars(sysMessages) : 0),
    tools,
    messages_chars: jsonChars(rest.length ? rest : undefined),
  };
}

function openaiChatParts(body) {
  const messages = expectArray(body.messages, "messages");
  const system = messages.filter(isSystemRole);
  const rest = messages.filter((m) => !isSystemRole(m));
  const tools = expectArray(body.tools, "tools").map((t) => ({
    name: t?.function?.name ?? t?.type ?? "?",
    chars: jsonChars(t),
  }));
  return { system_chars: jsonChars(system.length ? system : undefined), tools, messages_chars: jsonChars(rest.length ? rest : undefined) };
}

function openaiResponsesParts(body) {
  const tools = expectArray(body.tools, "tools").map((t) => ({ name: t?.name ?? t?.function?.name ?? t?.type ?? "?", chars: jsonChars(t) }));
  // The system prompt arrives either as `instructions` (codex) or as
  // system/developer-role input items (opencode via @ai-sdk). Count both.
  const input = expectArray(body.input, "input");
  const sysItems = input.filter(isSystemRole);
  const rest = input.filter((i) => !isSystemRole(i));
  return {
    system_chars: jsonChars(body.instructions) + (sysItems.length ? jsonChars(sysItems) : 0),
    tools,
    messages_chars: jsonChars(rest.length ? rest : undefined),
  };
}

function geminiParts(body) {
  const groups = expectArray(body.tools, "tools");
  const tools = groups.flatMap((group) => {
    const decls = group?.functionDeclarations;
    if (Array.isArray(decls)) return decls.map((d) => ({ name: d?.name ?? "?", chars: jsonChars(d) }));
    return [{ name: (group && Object.keys(group)[0]) ?? "?", chars: jsonChars(group) }];
  });
  return {
    system_chars: jsonChars(body.systemInstruction ?? body.system_instruction),
    tools,
    messages_chars: jsonChars(body.contents),
  };
}

// MCP-tool classification is per-harness and must be VALIDATED before it is
// trusted: only Claude Code is known to name MCP tools `mcp__server__tool`.
// A harness with no validated pattern reports null (printed as "-"), never 0 —
// a false zero would read as "no MCP tools loaded".
export const CLAUDE_MCP_PATTERN = /^mcp__/;

export function analyzeCapture(record, { mcpPattern = null } = {}) {
  const body = record?.body;
  if (!body || typeof body !== "object") return null;
  let parts;
  try {
    switch (record.kind) {
      case "anthropic-messages":
        parts = anthropicParts(body);
        break;
      case "openai-chat":
        parts = openaiChatParts(body);
        break;
      case "openai-responses":
        parts = openaiResponsesParts(body);
        break;
      case "gemini-generatecontent":
        parts = geminiParts(body);
        break;
      default:
        return null;
    }
  } catch (err) {
    // A body we cannot parse is skipped, never fatal: one malformed capture
    // must not discard every other harness's measurement.
    return { kind: record.kind, seq: record.seq, unparsed: true, error: String(err?.message ?? err), body_bytes: record.body_bytes ?? 0 };
  }
  const tools_chars = parts.tools.reduce((sum, t) => sum + t.chars, 0);
  const mcpTools = mcpPattern ? parts.tools.filter((t) => mcpPattern.test(t.name)) : null;
  return {
    kind: record.kind,
    seq: record.seq,
    model: typeof body.model === "string" ? body.model : (String(record.url ?? "").match(/\/models\/([^/:?]+)/)?.[1] ?? null),
    body_bytes: record.body_bytes,
    total_chars: jsonChars(body),
    system_chars: parts.system_chars,
    messages_chars: parts.messages_chars,
    tools_count: parts.tools.length,
    tools_chars,
    tools: parts.tools,
    mcp_tools_count: mcpTools ? mcpTools.length : null,
    mcp_tools_chars: mcpTools ? mcpTools.reduce((sum, t) => sum + t.chars, 0) : null,
  };
}

// The primary prefix request is the capture carrying the MOST tool schemas.
// Harnesses interleave small warmup/title/router calls — some of which carry a
// tool or two — with the real agent turn; picking "first with any tools" hands
// back a router call's 500 bytes as if it were the prefix. Ties break toward
// the earliest capture. If nothing carries tools, the largest body wins and
// the row says so via pick_rule.
export function pickPrimary(records, opts) {
  const analyzed = records
    .filter((r) => LLM_KINDS.has(r.kind))
    .map((r) => analyzeCapture(r, opts))
    .filter(Boolean);
  const usable = analyzed.filter((a) => !a.unparsed);
  const skipped = analyzed.filter((a) => a.unparsed);
  if (usable.length === 0) return { primary: null, all: analyzed, skipped, pick_rule: null };

  const maxTools = Math.max(...usable.map((a) => a.tools_count));
  if (maxTools === 0) {
    const primary = usable.reduce((best, cur) => (cur.body_bytes > best.body_bytes ? cur : best));
    return { primary, all: analyzed, skipped, pick_rule: "largest-body (no capture carried tools)" };
  }
  const primary = usable.filter((a) => a.tools_count === maxTools).reduce((best, cur) => (cur.seq < best.seq ? cur : best));
  return { primary, all: analyzed, skipped, pick_rule: "most-tools" };
}

export { LLM_KINDS };
