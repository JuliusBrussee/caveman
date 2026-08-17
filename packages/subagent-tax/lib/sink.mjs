#!/usr/bin/env node
// subagent-tax capture sink — a local HTTP server that impersonates an LLM
// provider endpoint just well enough for a coding harness to send it the first
// request of a session. It logs every request (auth-redacted) to a capture
// directory and replies with a minimal canned completion so the harness ends
// its turn instead of retrying. Nothing ever leaves the machine.
//
// Protocols: anthropic-messages · openai-responses · openai-chat ·
// gemini-generatecontent (JSON and SSE for each).
//
// Usage: node sink.mjs [--port N] [--capture DIR] [--verbose]
// stdout contract (machine-parseable, one line each):
//   SINK_READY port=<n> capture=<dir>
//   CAPTURE seq=<n> kind=<protocol> bytes=<body bytes> path=<url path>

import { createServer } from "node:http";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { createHash, randomUUID } from "node:crypto";

// Header names whose values must never reach disk — credentials AND the
// account/device/session identifiers that would let a shared repro pack be
// linked back to a person. Values are replaced with redacted:sha256:<12> so
// equality across requests stays checkable.
export const REDACT_HEADERS = new Set([
  // credentials
  "authorization",
  "proxy-authorization",
  "x-api-key",
  "api-key",
  "x-goog-api-key",
  "x-auth-token",
  "x-api-token",
  "cookie",
  "set-cookie",
  // account / org identity
  "chatgpt-account-id",
  "openai-organization",
  "openai-project",
  "anthropic-organization-id",
  "x-gemini-api-privileged-user-id",
  "x-goog-user-project",
  // per-install / per-session identifiers (link a pack to a machine)
  "session-id",
  "x-session-id",
  "x-session-affinity",
  "x-claude-code-session-id",
  "x-codex-turn-metadata",
  "x-codex-window-id",
  "x-client-request-id",
  "thread-id",
  "x-request-id",
  "x-stainless-retry-count",
]);

// Body fields carrying the same class of identifier. Removed by key, wherever
// they appear in the JSON tree.
export const REDACT_BODY_KEYS = new Set([
  "user_id",
  "device_id",
  "account_uuid",
  "prompt_cache_key",
  "safety_identifier",
  "conversation_id",
  "session_id",
  "sessionId",
  "installation_id",
]);

// Query params that can carry credentials (Gemini supports ?key=).
export const REDACT_QUERY_PARAMS = new Set(["key", "api_key", "apikey", "access_token"]);

export function redactValue(value) {
  const hash = createHash("sha256").update(String(value)).digest("hex").slice(0, 12);
  return `redacted:sha256:${hash}`;
}

export function redactHeaders(headers) {
  const out = {};
  for (const [name, value] of Object.entries(headers)) {
    const key = name.toLowerCase();
    out[key] = REDACT_HEADERS.has(key)
      ? Array.isArray(value) ? value.map(redactValue) : redactValue(value)
      : value;
  }
  return out;
}

// Body-level scrub: request bodies are the harness's real system prompt and
// can embed the account email (Claude Code does) or credential-shaped strings.
// High-precision patterns only — replacements keep a comparable hash. Sizes
// shift by a few chars; body_bytes always records the raw pre-redaction length.
const BODY_PATTERNS = [
  { re: /[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+/g, tag: "email" },
  { re: /\b(?:sk-[A-Za-z0-9_-]{10,}|ghp_[A-Za-z0-9]{20,}|gho_[A-Za-z0-9]{20,}|AKIA[0-9A-Z]{16}|xox[baprs]-[A-Za-z0-9-]{10,}|AIza[0-9A-Za-z_-]{20,})\b/g, tag: "key" },
];

export function redactBodyString(text) {
  let out = text;
  for (const { re, tag } of BODY_PATTERNS) {
    out = out.replace(re, (m) => `redacted:${tag}:${createHash("sha256").update(m).digest("hex").slice(0, 8)}`);
  }
  return out;
}

function redactKeys(value) {
  if (Array.isArray(value)) return value.map(redactKeys);
  if (value && typeof value === "object") {
    const out = {};
    for (const [k, v] of Object.entries(value)) {
      out[k] = REDACT_BODY_KEYS.has(k)
        ? redactValue(typeof v === "string" ? v : JSON.stringify(v))
        : redactKeys(v);
    }
    return out;
  }
  return value;
}

export function redactBody(body) {
  if (body === undefined || body === null) return body;
  if (typeof body === "string") return redactBodyString(body);
  const keyed = redactKeys(body);
  const redacted = redactBodyString(JSON.stringify(keyed));
  try {
    return JSON.parse(redacted);
  } catch {
    return redacted;
  }
}

export function redactUrl(rawUrl) {
  const [path, query] = String(rawUrl).split("?", 2);
  if (!query) return rawUrl;
  const parts = query.split("&").map((pair) => {
    const eq = pair.indexOf("=");
    if (eq === -1) return pair;
    const name = decodeURIComponent(pair.slice(0, eq)).toLowerCase();
    return REDACT_QUERY_PARAMS.has(name) ? `${pair.slice(0, eq)}=${redactValue(pair.slice(eq + 1))}` : pair;
  });
  return `${path}?${parts.join("&")}`;
}

// Route classification by path shape. Order matters: count_tokens before messages.
export function classifyRequest(method, url) {
  const path = url.split("?", 2)[0];
  if (method === "GET" || method === "HEAD") {
    if (/\/models\/?$/.test(path)) return path.includes("/v1beta") ? "gemini-models" : "openai-models";
    return "other-get";
  }
  if (/\/count_tokens$/.test(path)) return "anthropic-count-tokens";
  if (/\/messages$/.test(path)) return "anthropic-messages";
  if (/\/chat\/completions$/.test(path)) return "openai-chat";
  if (/\/responses$/.test(path)) return "openai-responses";
  if (/:(streamG|g)enerateContent$/.test(path)) return "gemini-generatecontent";
  return "other";
}

export function wantsStream(kind, url, headers, body) {
  if (body && body.stream === true) return true;
  if (/:streamGenerateContent/.test(url)) return true;
  if (/alt=sse/.test(url)) return true;
  const accept = String(headers.accept || "");
  return accept.includes("text/event-stream") && kind !== "other-get";
}

const TEXT = "DONE";

function anthropicMessage(model) {
  return {
    id: `msg_${randomUUID().replaceAll("-", "")}`,
    type: "message",
    role: "assistant",
    model: model || "sink",
    content: [{ type: "text", text: TEXT }],
    stop_reason: "end_turn",
    stop_sequence: null,
    usage: { input_tokens: 1, output_tokens: 1 },
  };
}

function anthropicSse(model) {
  const start = { ...anthropicMessage(model), content: [], stop_reason: null };
  return [
    ["message_start", { type: "message_start", message: start }],
    ["content_block_start", { type: "content_block_start", index: 0, content_block: { type: "text", text: "" } }],
    ["content_block_delta", { type: "content_block_delta", index: 0, delta: { type: "text_delta", text: TEXT } }],
    ["content_block_stop", { type: "content_block_stop", index: 0 }],
    ["message_delta", { type: "message_delta", delta: { stop_reason: "end_turn", stop_sequence: null }, usage: { output_tokens: 1 } }],
    ["message_stop", { type: "message_stop" }],
  ];
}

function openaiChat(model) {
  return {
    id: `chatcmpl-${randomUUID().slice(0, 8)}`,
    object: "chat.completion",
    created: Math.floor(Date.now() / 1000),
    model: model || "sink",
    choices: [{ index: 0, message: { role: "assistant", content: TEXT }, finish_reason: "stop" }],
    usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
  };
}

function openaiChatSse(model) {
  const base = { id: `chatcmpl-${randomUUID().slice(0, 8)}`, object: "chat.completion.chunk", created: Math.floor(Date.now() / 1000), model: model || "sink" };
  return [
    { ...base, choices: [{ index: 0, delta: { role: "assistant", content: TEXT }, finish_reason: null }] },
    { ...base, choices: [{ index: 0, delta: {}, finish_reason: "stop" }] },
  ];
}

function responsesPayload(model) {
  const id = `resp_${randomUUID().replaceAll("-", "")}`;
  const item = {
    id: `msg_${randomUUID().replaceAll("-", "")}`,
    type: "message",
    status: "completed",
    role: "assistant",
    content: [{ type: "output_text", annotations: [], text: TEXT }],
  };
  return {
    id,
    object: "response",
    created_at: Math.floor(Date.now() / 1000),
    status: "completed",
    model: model || "sink",
    output: [item],
    usage: { input_tokens: 1, input_tokens_details: { cached_tokens: 0 }, output_tokens: 1, output_tokens_details: { reasoning_tokens: 0 }, total_tokens: 2 },
    item,
  };
}

function openaiResponsesSse(model) {
  const full = responsesPayload(model);
  const { item } = full;
  const response = { ...full };
  delete response.item;
  const inProgress = { ...response, status: "in_progress", output: [], usage: null };
  return [
    ["response.created", { type: "response.created", response: inProgress, sequence_number: 0 }],
    ["response.in_progress", { type: "response.in_progress", response: inProgress, sequence_number: 1 }],
    ["response.output_item.added", { type: "response.output_item.added", output_index: 0, item: { ...item, status: "in_progress", content: [] }, sequence_number: 2 }],
    ["response.content_part.added", { type: "response.content_part.added", item_id: item.id, output_index: 0, content_index: 0, part: { type: "output_text", annotations: [], text: "" }, sequence_number: 3 }],
    ["response.output_text.delta", { type: "response.output_text.delta", item_id: item.id, output_index: 0, content_index: 0, delta: TEXT, sequence_number: 4 }],
    ["response.output_text.done", { type: "response.output_text.done", item_id: item.id, output_index: 0, content_index: 0, text: TEXT, sequence_number: 5 }],
    ["response.content_part.done", { type: "response.content_part.done", item_id: item.id, output_index: 0, content_index: 0, part: { type: "output_text", annotations: [], text: TEXT }, sequence_number: 6 }],
    ["response.output_item.done", { type: "response.output_item.done", output_index: 0, item, sequence_number: 7 }],
    ["response.completed", { type: "response.completed", response, sequence_number: 8 }],
  ];
}

function geminiPayload() {
  return {
    candidates: [{ content: { parts: [{ text: TEXT }], role: "model" }, finishReason: "STOP", index: 0 }],
    usageMetadata: { promptTokenCount: 1, candidatesTokenCount: 1, totalTokenCount: 2 },
    modelVersion: "sink",
  };
}

function modelFrom(kind, url, body) {
  if (body && typeof body.model === "string") return body.model;
  const match = url.match(/\/models\/([^/:?]+)/);
  return match ? match[1] : undefined;
}

export function buildResponse(kind, url, headers, body) {
  const stream = wantsStream(kind, url, headers, body);
  const model = modelFrom(kind, url, body);
  const sse = (events) => ({
    stream: true,
    body: events
      .map((e) => (Array.isArray(e) ? `event: ${e[0]}\ndata: ${JSON.stringify(e[1])}\n\n` : `data: ${JSON.stringify(e)}\n\n`))
      .join(""),
  });
  switch (kind) {
    case "anthropic-count-tokens":
      return { stream: false, body: JSON.stringify({ input_tokens: 1 }) };
    case "anthropic-messages":
      return stream ? sse(anthropicSse(model)) : { stream: false, body: JSON.stringify(anthropicMessage(model)) };
    case "openai-chat": {
      if (!stream) return { stream: false, body: JSON.stringify(openaiChat(model)) };
      const out = sse(openaiChatSse(model));
      out.body += "data: [DONE]\n\n";
      return out;
    }
    case "openai-responses": {
      if (!stream) {
        const payload = responsesPayload(model);
        delete payload.item;
        return { stream: false, body: JSON.stringify(payload) };
      }
      return sse(openaiResponsesSse(model));
    }
    case "gemini-generatecontent":
      return stream ? sse([geminiPayload()]) : { stream: false, body: JSON.stringify(geminiPayload()) };
    case "openai-models":
      return {
        stream: false,
        body: JSON.stringify({
          object: "list",
          data: ["gpt-5.5", "gpt-5.5-codex", "gpt-4o", "claude-sonnet-5", "claude-opus-5"].map((id) => ({ id, object: "model", created: 0, owned_by: "sink" })),
        }),
      };
    case "gemini-models":
      return {
        stream: false,
        body: JSON.stringify({
          models: ["gemini-2.5-pro", "gemini-2.5-flash"].map((name) => ({
            name: `models/${name}`,
            supportedGenerationMethods: ["generateContent", "streamGenerateContent"],
          })),
        }),
      };
    default:
      return { stream: false, body: "{}" };
  }
}

export function startSink({ port = 0, captureDir, onCapture, verbose = false } = {}) {
  if (captureDir) mkdirSync(captureDir, { recursive: true });
  let seq = 0;

  const server = createServer((req, res) => {
    const chunks = [];
    // A harness killed mid-upload resets the socket; that must not crash the
    // measurement run.
    req.on("error", () => {});
    res.on("error", () => {});
    req.on("data", (c) => chunks.push(c));
    req.on("end", () => {
      const raw = Buffer.concat(chunks);
      const kind = classifyRequest(req.method, req.url);
      let body;
      try {
        body = raw.length ? JSON.parse(raw.toString("utf8")) : undefined;
      } catch {
        body = undefined;
      }

      // Capture every request that carries a body (plus GETs when verbose):
      // the analyzer decides later which one is the primary prefix.
      if (captureDir && (raw.length > 0 || verbose)) {
        seq += 1;
        const record = {
          seq,
          ts: new Date().toISOString(),
          method: req.method,
          url: redactUrl(req.url),
          kind,
          headers: redactHeaders(req.headers),
          body_bytes: raw.length,
          body: redactBody(body ?? (raw.length ? raw.toString("utf8") : null)),
        };
        const slug = kind.replace(/[^a-z-]/g, "");
        writeFileSync(join(captureDir, `${String(seq).padStart(3, "0")}-${slug}.json`), JSON.stringify(record, null, 1));
        const line = `CAPTURE seq=${seq} kind=${kind} bytes=${raw.length} path=${req.url.split("?")[0]}`;
        if (onCapture) onCapture({ seq, kind, bytes: raw.length, path: req.url });
        else process.stdout.write(`${line}\n`);
      }

      const reply = buildResponse(kind, req.url, req.headers, body);
      res.writeHead(200, reply.stream
        ? { "content-type": "text/event-stream", "cache-control": "no-store", connection: "keep-alive" }
        : { "content-type": "application/json" });
      res.end(reply.body);
      if (verbose) process.stderr.write(`sink: ${req.method} ${req.url} → ${kind}${reply.stream ? " (sse)" : ""}\n`);
    });
  });

  return new Promise((resolve, reject) => {
    server.on("error", reject);
    server.listen(port, "127.0.0.1", () => {
      resolve({ server, port: server.address().port });
    });
  });
}

const isMain = process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href;
if (isMain) {
  const argv = process.argv.slice(2);
  const flag = (name, fallback) => {
    const i = argv.indexOf(name);
    return i !== -1 && argv[i + 1] !== undefined ? argv[i + 1] : fallback;
  };
  const port = Number(flag("--port", "0"));
  const captureDir = flag("--capture", join(process.cwd(), "subagent-tax-captures"));
  const verbose = argv.includes("--verbose");
  startSink({ port, captureDir, verbose }).then(({ port: boundPort }) => {
    process.stdout.write(`SINK_READY port=${boundPort} capture=${captureDir}\n`);
  }).catch((err) => {
    process.stderr.write(`sink: failed to listen: ${err.message}\n`);
    process.exit(1);
  });
  process.on("SIGTERM", () => process.exit(0));
  process.on("SIGINT", () => process.exit(0));
}
