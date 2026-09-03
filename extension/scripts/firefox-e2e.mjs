/*
 * firefox-e2e.mjs — regression guard for the Firefox port (#810 / PR #936).
 *
 * Proves, in a REAL Firefox, that the staged build's content script injects the
 * indicator and that a trusted Enter sends the full primer. Method per KB #6443:
 * web-ext installs the add-on (release-safe temporary-addon path) while
 * `--args=--remote-debugging-port` opens the WebDriver BiDi agent on the SAME
 * instance; a raw BiDi client (Node >= 22 global WebSocket) drives the page and
 * reads the DOM. Never breaks `npm test` / `test:all` on machines without
 * Firefox or web-ext: those runs print a SKIP line and exit 0.
 *
 * Run: npm run test:firefox            (or: node scripts/firefox-e2e.mjs)
 * Env: FIREFOX_BIN overrides the Firefox executable probe.
 */
import { spawn, spawnSync } from "node:child_process";
import { existsSync, readFileSync, writeFileSync, mkdirSync, cpSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import net from "node:net";
import { createServer as createHttpServer } from "node:http";

const EXT_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");

function log(line) {
  process.stdout.write(line + "\n");
}
// A skip is a clean exit-0 outcome, thrown only BEFORE any resource is acquired,
// so no finally is ever bypassed (KB 5486: process.exit must never preempt
// pending cleanup — the only process.exit calls live at module top level).
class SkipError extends Error {}
function skip(reason) {
  throw new SkipError(reason);
}
function freePort() {
  return new Promise((res, rej) => {
    const srv = net.createServer();
    srv.listen(0, "127.0.0.1", () => {
      const port = srv.address().port;
      srv.close(() => res(port));
    });
    srv.on("error", rej);
  });
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Returns { path } or { missing } where missing explains why nothing was found.
function findFirefox() {
  if (process.env.FIREFOX_BIN) {
    return existsSync(process.env.FIREFOX_BIN)
      ? { path: process.env.FIREFOX_BIN }
      : { missing: `FIREFOX_BIN points at ${process.env.FIREFOX_BIN}, which does not exist` };
  }
  const candidates =
    process.platform === "win32"
      ? [
          "C:/Program Files/Mozilla Firefox/firefox.exe",
          "C:/Program Files (x86)/Mozilla Firefox/firefox.exe",
        ]
      : ["/usr/bin/firefox", "/usr/bin/firefox-esr", "/Applications/Firefox.app/Contents/MacOS/firefox"];
  const hit = candidates.find((p) => existsSync(p));
  return hit ? { path: hit } : { missing: "no Firefox executable found (install Firefox or set FIREFOX_BIN)" };
}

function hasWebExt() {
  // --no-install first (fast when cached); fall back to a plain npx (registry is
  // reachable even where the playwright CDN is not). Any failure → SKIP.
  for (const cmd of [
    "npx --no-install web-ext --version",
    "npx web-ext --version",
  ]) {
    try {
      const r = spawnSync(cmd, { cwd: EXT_ROOT, shell: true, encoding: "utf8", timeout: 90000 });
      if (r.status === 0 && /^\d+\.\d+/.test(String(r.stdout || "").trim())) return true;
    } catch {
      /* try next */
    }
  }
  return false;
}

// ── results ────────────────────────────────────────────────────────────────
const results = [];
function record(name, ok, detail) {
  results.push(ok);
  log(`${ok ? "PASS" : "FAIL"}  ${name}${detail ? "  —  " + detail : ""}`);
}

const ffVersion = (binary) => {
  try {
    const r = spawnSync(`"${binary}" --version`, { shell: true, encoding: "utf8", timeout: 15000 });
    return String(r.stdout || r.stderr || "").trim().split("\n")[0] || "unknown";
  } catch {
    return "unknown";
  }
};

async function main() {
  if (Number(process.versions.node.split(".")[0]) < 22) skip("needs Node >= 22 for the built-in WebSocket client");
  const found = findFirefox();
  if (found.missing) skip(found.missing);
  const ff = found.path;
  if (!hasWebExt()) skip("web-ext unavailable (npx web-ext --version failed)");

  log(`firefox-e2e: ${ff} — ${ffVersion(ff)}`);
  const bidiPort = await freePort();
  const work = join(tmpdir(), `caveman-firefox-e2e-${process.pid}`);
  rmSync(work, { recursive: true, force: true });
  mkdirSync(work + "/scratch", { recursive: true });

  let wex = null;
  let ws = null;
  let server = null;
  try {
    // 1. build the Firefox stage (single source of packaging truth), copy to scratch
    const b = spawnSync("node", ["scripts/build-extension-zip.mjs", "firefox"], { cwd: EXT_ROOT, encoding: "utf8", timeout: 60000 });
    if (b.status !== 0) throw new Error("firefox stage build failed: " + String(b.stderr || b.stdout).slice(-300));
    cpSync(join(EXT_ROOT, "dist/stage"), work + "/scratch", { recursive: true });
    // 2. patch the THROWAWAY copy only: localhost match (NO port — Firefox match
    //    patterns reject ports, KB #6442) + a 127.0.0.1 site entry
    const mp = work + "/scratch/manifest.json";
    const m = JSON.parse(readFileSync(mp, "utf8"));
    m.content_scripts[0].matches.push("http://127.0.0.1/*");
    writeFileSync(mp, JSON.stringify(m, null, 2) + "\n");
    const cp = work + "/scratch/src/caveman.js";
    let c = readFileSync(cp, "utf8");
    const anchor = "\n  };\n\n  const cfg = SITES[HOST]";
    const add = '\n    "127.0.0.1": {\n      editor: ["#prompt-textarea", "div.ProseMirror"],\n      send: ["button[data-testid=send-button]"],\n      message: ["[data-message-author-role]"],\n    },';
    if (!c.includes(anchor)) throw new Error("caveman.js SITES anchor not found; content script changed shape");
    writeFileSync(cp, c.replace(anchor, add + anchor));

    // 3. harness (no chrome stubs — the extension provides storage)
    const harnessPort = await freePort();
    const HARNESS_HTML = `<!DOCTYPE html><html><head><meta charset="utf-8"><title>FF harness</title>
<style>body{font:14px system-ui;max-width:640px;margin:40px auto}#transcript div{padding:6px 10px;margin:4px 0;background:#f0f0f0;white-space:pre-wrap}#prompt-textarea{border:1px solid #ccc;border-radius:8px;padding:10px;min-height:44px}button{margin-top:8px;padding:8px 14px}</style></head>
<body><h3>Mock chat (Firefox e2e)</h3><div id="transcript"></div><div id="prompt-textarea" class="ProseMirror" contenteditable="true" role="textbox"></div><button data-testid="send-button">Send</button>
<script>
const ed=document.getElementById("prompt-textarea"), sb=document.querySelector('button[data-testid="send-button"]'), tr=document.getElementById("transcript");
function appSend(){const t=(ed.innerText||"").replace(/\\u00a0/g," ").trim(); if(!t) return; const m=document.createElement("div"); m.setAttribute("data-message-author-role","user"); m.textContent=t; tr.appendChild(m); ed.innerHTML="";}
ed.addEventListener("keydown",e=>{if(e.key==="Enter"&&!e.shiftKey&&!e.ctrlKey&&!e.metaKey&&!e.altKey){e.preventDefault();appSend();}});
sb.addEventListener("click",appSend);
window.cavemanTest={transcript(){return[...tr.querySelectorAll("div")].map(d=>d.textContent);}};
</script></body></html>`;
    server = createHttpServer((q, s) => {
      if (q.url === "/") { s.writeHead(200, { "content-type": "text/html" }); s.end(HARNESS_HTML); }
      else { s.writeHead(404); s.end("nf"); }
    });
    await new Promise((r) => server.listen(harnessPort, "127.0.0.1", r));

    // 4. web-ext run: installs the add-on, and --args opens the BiDi agent on the same instance
    wex = spawn(
      `npx web-ext run --source-dir ${work}/scratch --firefox "${ff}" --start-url about:blank --no-reload --verbose --args=--remote-debugging-port=${bidiPort}`,
      { cwd: EXT_ROOT, shell: true, stdio: ["ignore", "pipe", "pipe"] },
    );
    let _installed = null;
    const installed = new Promise((r) => { _installed = r; });
    wex.stdout.on("data", (d) => { if (String(d).includes("temporary add-on")) _installed && _installed(); });
    wex.stderr.on("data", () => {});
    await Promise.race([installed, sleep(90000).then(() => { throw new Error("web-ext did not install the add-on within 90s"); })]);

    let agentUp = false;
    for (let i = 0; i < 40; i++) {
      try { await fetch(`http://127.0.0.1:${bidiPort}/session/status`, { signal: AbortSignal.timeout(1200) }); agentUp = true; break; } catch { /* retry */ }
      await sleep(500);
    }
    record("web-ext installed the staged build as a temporary add-on", true, agentUp ? "" : "(agent not reachable)");
    if (!agentUp) throw new Error("BiDi agent never came up");

    await sleep(1500);
    ws = new WebSocket(`ws://127.0.0.1:${bidiPort}/session`);
    await new Promise((res, rej) => { ws.onopen = res; ws.onerror = () => rej(new Error("BiDi websocket error")); });
    let msgId = 0;
    const pending = new Map();
    const events = [];
    ws.onmessage = (m) => {
      const j = JSON.parse(m.data);
      if (j.type === "success" || j.type === "error") { const p = pending.get(j.id); if (p) { pending.delete(j.id); p(j); } }
      else if (j.type === "event") events.push(j);
    };
    const cmd = (method, params = {}) => new Promise((res, rej) => {
      const id = ++msgId; pending.set(id, res); ws.send(JSON.stringify({ id, method, params }));
      setTimeout(() => { if (pending.delete(id)) rej(new Error(`timeout: ${method}`)); }, 20000);
    });
    const sess = await cmd("session.new", { capabilities: {} });
    record("WebDriver BiDi session on the same Firefox instance", !!sess.result?.sessionId, `firefox ${sess.result?.capabilities?.browserVersion}`);
    await cmd("session.subscribe", { events: ["log.entryAdded"] });
    const ctx = (await cmd("browsingContext.create", { type: "tab" })).result?.context;
    await cmd("browsingContext.navigate", { context: ctx, url: `http://127.0.0.1:${harnessPort}/`, wait: "complete" });
    await sleep(3500);

    const evalJs = async (expression) => {
      const r = await cmd("script.evaluate", { expression, target: { context: ctx }, awaitPromise: true, resultOwnership: "none" });
      return r.result?.result?.value ?? JSON.stringify(r.result?.exceptionDetails || r);
    };

    // 5. assertion A: the content script injected the indicator
    const ind = JSON.parse(await evalJs(`JSON.stringify((() => {
      const el = document.getElementById("caveman-indicator");
      return el ? { present: true, text: el.innerText } : { present: false };
    })())`));
    record("content script injected the indicator", ind.present === true, JSON.stringify(ind));
    if (!ind.present) throw new Error("no indicator — content script did not inject");

    // 6. assertion B: a trusted Enter sends the full primer (BiDi insertText can't
    //    type into contenteditable — KB #6443 — so write the draft directly, then
    //    press a real key; the extension's capture → setText → poll-click runs)
    await evalJs(`(() => { const ed = document.getElementById("prompt-textarea"); ed.innerText = "prove firefox e2e"; ed.focus(); return true; })()`);
    await cmd("input.performActions", { context: ctx, actions: [{ type: "key", id: "keyboard", actions: [{ type: "keyDown", value: "\uE007" }, { type: "keyUp", value: "\uE007" }] }] });
    await sleep(3500);
    const transcript = await evalJs(`JSON.stringify(window.cavemanTest ? window.cavemanTest.transcript() : [])`);
    let lines = [];
    try { lines = JSON.parse(transcript); } catch { /* fall through */ }
    record(
      "first Enter sends the full primer",
      lines.length === 1 && lines[0]?.includes("[Caveman mode is ON") && lines[0]?.includes("prove firefox e2e"),
      JSON.stringify((lines[0] || "").slice(0, 90)),
    );

    // 7. assertion C: zero extension-sourced console errors
    await sleep(500);
    const extErrors = events
      .filter((e) => e.method === "log.entryAdded" && (e.params.level === "error" || e.params.level === "warn"))
      .map((e) => `${e.params.level}: ${(e.params.text || "").slice(0, 160)}`)
      .filter((t) => !/favicon/i.test(t) && !/Remote Settings|services\.settings/i.test(t) && !/shell_windows/i.test(t));
    record("zero extension-sourced console errors", extErrors.length === 0, extErrors.slice(0, 4).join(" | "));
  } finally {
    try { ws?.close(); } catch { /* ignore */ }
    try { wex?.kill(); } catch { /* ignore */ }
    if (wex?.pid && process.platform === "win32") {
      try { spawnSync("taskkill", ["/PID", String(wex.pid), "/T", "/F"], { timeout: 10000 }); } catch { /* ignore */ }
    }
    await sleep(800);
    try { server?.close(); } catch { /* ignore */ }
    rmSync(work, { recursive: true, force: true });
  }

  const fails = results.filter((r) => r === false).length;
  log(`\nfirefox-e2e: ${results.length - fails}/${results.length} passed${fails ? `, ${fails} FAILED` : ""}`);
  return fails ? 1 : 0;
}

// Top-level only: every exit happens here, after main()'s finally has run.
main()
  .then((code) => process.exit(code))
  .catch((e) => {
    if (e instanceof SkipError) {
      log(`SKIP firefox-e2e — ${e.message} (exit 0)`);
      process.exit(0);
    }
    log(`firefox-e2e ERROR: ${e.message}`);
    process.exit(1);
  });
