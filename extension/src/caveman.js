/*
 * Caveman Mode — content script.
 *
 * When enabled, intercepts the "send" gesture (Enter or the send button) on
 * ChatGPT / Claude / Gemini and prepends a caveman directive to the outgoing
 * message, then re-fires the original send. Smart-hybrid injection:
 *   - first message of a conversation  -> full primer (the caveman skill, compact)
 *   - every message after              -> short "stay caveman" reminder
 *
 * It only ever adds text to YOUR outgoing message — it never touches the page's
 * network requests or the model's replies. The directive is plainly visible in
 * the chat, by design (honest about what it injects). The primer/reminder text
 * lives in directive.js (loaded first), which keeps it unit-testable.
 */
(() => {
  "use strict";

  const D = self.CavemanDirective;
  if (!D) return; // directive.js failed to load; do nothing rather than misbehave

  const HOST = location.hostname.replace(/^www\./, "");

  // ---- per-site selectors (resilient: first hit wins, fallbacks follow) ----
  // Send selectors are intentionally over-specified: the sites render the send
  // button only once the composer has text and rename it across redesigns, so
  // each site keeps an aria-label fallback after its primary selector.
  const SITES = {
    "chatgpt.com": {
      editor: ["#prompt-textarea", 'div.ProseMirror[contenteditable="true"]'],
      send: ['button[data-testid="send-button"]', "#composer-submit-button", 'button[aria-label="Send prompt"]'],
      message: ["[data-message-author-role]"],
    },
    "chat.openai.com": {
      editor: ["#prompt-textarea", 'div.ProseMirror[contenteditable="true"]'],
      send: ['button[data-testid="send-button"]', "#composer-submit-button", 'button[aria-label="Send prompt"]'],
      message: ["[data-message-author-role]"],
    },
    "claude.ai": {
      editor: ['div.ProseMirror[contenteditable="true"]', 'div[contenteditable="true"]'],
      send: ['button[aria-label="Send message"]', 'button[aria-label="Send Message"]', 'button[aria-label*="Send" i]'],
      message: ['[data-testid="user-message"]', "div.font-claude-message"],
    },
    "gemini.google.com": {
      // `button.send-button` was removed in a Gemini redesign; the live control is
      // a Material icon button labelled "Send message" — keep both plus a loose
      // aria fallback so a future rename still resolves.
      editor: ['.ql-editor[contenteditable="true"]', 'rich-textarea div[contenteditable="true"]', 'div[contenteditable="true"]'],
      send: ['button[aria-label="Send message"]', "button.send-button", 'button[aria-label*="Send" i]', 'button[mattooltip*="Send" i]'],
      message: ["user-query", "model-response"],
    },
  };

  const cfg = SITES[HOST] || SITES[Object.keys(SITES).find((k) => HOST.endsWith(k)) || ""];
  if (!cfg) return;

  // ---- live state from storage ----
  let enabled = false;
  let level = "full";
  let bypass = false; // true while we re-fire the user's own send

  function refresh() {
    chrome.storage.sync.get({ enabled: true, level: "full", sites: {} }, (s) => {
      const siteOn = s.sites[HOST] !== false; // default on per site
      enabled = !!s.enabled && siteOn;
      level = D.normLevel(s.level);
      renderIndicator();
    });
  }
  chrome.storage.onChanged.addListener(refresh);
  refresh();

  // ---- DOM helpers ----
  function isVisible(el) {
    if (!el) return false;
    const r = el.getBoundingClientRect();
    return r.width > 4 && r.height > 4;
  }
  function pick(selectors) {
    for (const sel of selectors) {
      let el = null;
      try {
        el = document.querySelector(sel);
      } catch (_e) {
        continue; // a selector the browser can't parse — skip it
      }
      if (el && isVisible(el)) return el;
    }
    return null;
  }
  function getEditor() {
    const el = pick(cfg.editor);
    if (el) return el;
    // fallback: the last visible textarea / contenteditable (usually the composer)
    const list = [...document.querySelectorAll('textarea, [contenteditable="true"]')].filter(isVisible);
    return list.length ? list[list.length - 1] : null;
  }
  // A button is a usable send target only if it is visible, NOT disabled (the
  // sites gate with either the `disabled` prop OR `aria-disabled`), and is not the
  // Stop/abort button — during generation the sites swap Send for a Stop control
  // that can share the composer's button slot; clicking it would abort the reply
  // and never send.
  const SEND_NEG = /\b(stop|abort|cancel)\b/i;
  function looksSendable(btn) {
    if (!btn || !isVisible(btn)) return false;
    if (btn.disabled || btn.getAttribute("aria-disabled") === "true") return false;
    const meta =
      (btn.getAttribute("aria-label") || "") + " " + (btn.getAttribute("data-testid") || "") + " " + (btn.title || "");
    return !SEND_NEG.test(meta);
  }
  function composerBox() {
    const ed = getEditor();
    if (!ed) return null;
    let box = ed;
    for (let i = 0; i < 6 && box.parentElement; i++) box = box.parentElement;
    return box;
  }
  function getSend() {
    // 1) precise per-site selectors (send-specific), document-wide.
    for (const sel of cfg.send) {
      let el = null;
      try {
        el = document.querySelector(sel);
      } catch (_e) {
        continue; // unparseable selector — skip
      }
      if (looksSendable(el)) return el;
    }
    // 2) loose fallback, scoped to the composer only so a page-level "Send
    //    feedback" / "Resend" button can never be mistaken for the composer send.
    const box = composerBox();
    if (box) {
      const cand = [...box.querySelectorAll("button")].find(
        (b) => /send/i.test(b.getAttribute("aria-label") || "") && looksSendable(b)
      );
      if (cand) return cand;
    }
    return null;
  }
  const isTextarea = (el) => el && el.tagName === "TEXTAREA";
  const getText = (el) => (isTextarea(el) ? el.value : el.innerText);
  function messageCount() {
    let n = 0;
    for (const sel of cfg.message) {
      try {
        n += document.querySelectorAll(sel).length;
      } catch (_e) {
        /* ignore an unparseable selector */
      }
    }
    return n;
  }

  // Replace the composer's content with `text`. Returns true on success.
  //
  // For rich editors (ProseMirror / Quill / Lexical) we drive the SAME path the
  // framework listens to — focus, select-all, execCommand("insertText") — so its
  // internal document model updates and the send button re-enables. We do NOT
  // fall back to `el.textContent = text`: writing the DOM directly desyncs those
  // editors from their model and can wipe the draft on the next keystroke. On any
  // failure we return false and the caller fails closed (sends the original).
  function setText(el, text) {
    el.focus();
    if (isTextarea(el)) {
      const setter = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, "value").set;
      setter.call(el, text);
      el.dispatchEvent(new Event("input", { bubbles: true }));
      return true;
    }
    const sel = window.getSelection();
    const range = document.createRange();
    range.selectNodeContents(el);
    sel.removeAllRanges();
    sel.addRange(range);
    try {
      return document.execCommand("insertText", false, text) === true;
    } catch (_e) {
      sel.collapseToEnd();
      return false;
    }
  }

  // dispatchEnter — a full synthetic Enter key sequence on the editor. Fallback
  // for the rare site/state where no send button can be resolved.
  function dispatchEnter(el) {
    for (const type of ["keydown", "keypress", "keyup"]) {
      el.dispatchEvent(
        new KeyboardEvent(type, { key: "Enter", code: "Enter", keyCode: 13, which: 13, bubbles: true, cancelable: true })
      );
    }
  }

  // fireSend — send the (already-injected) draft. The site renders/enables the
  // send button a render tick AFTER setText, so we POLL for an enabled, visible
  // send button (up to ~820ms) and click it; only if none resolves do we fall
  // back to dispatching Enter. `bypass` keeps our own interceptors off our
  // synthetic gesture. Exactly one trigger fires per gesture (click OR Enter,
  // never both), so a successful suppression can't become a double-send.
  function fireSend(el) {
    bypass = true;
    let tries = 0;
    const release = () => setTimeout(() => (bypass = false), 200);
    const tick = () => {
      const btn = getSend(); // already filtered: visible, enabled, not Stop
      if (btn) {
        btn.click();
        release();
        return;
      }
      if (tries++ < 16) {
        setTimeout(tick, 50); // ~820ms budget for the button to mount + enable
        return;
      }
      dispatchEnter(el); // last resort: no send button resolved
      release();
    };
    setTimeout(tick, 20);
  }

  function injectAndSend(el) {
    const original = getText(el);
    // "First message of this conversation" — the only state that earns the full
    // primer — is when no messages have rendered yet. Keying off the live message
    // count (not a per-load flag) means reloading or deep-linking into an existing
    // chat correctly gets the short reminder, not another full primer.
    const isFirst = messageCount() === 0;
    const prefix = isFirst ? D.buildPrimer(level) : D.buildReminder(level);
    const ok = setText(el, prefix + "\n\n" + original);
    if (!ok) {
      // Injection failed. Restore the original and send it un-prefixed — but only
      // if the restore left a real draft; never fire send on an empty/garbled box.
      setText(el, original);
      if (!getText(el).trim()) return;
    }
    fireSend(el);
  }

  // ---- intercept the send gesture (capture phase, so we beat the app) ----
  // run injectAndSend after we've already cancelled the native gesture. If
  // anything throws, still attempt a plain send so cancelling the user's Enter can
  // never leave them unable to send (a thrown error here would otherwise swallow
  // every send until reload).
  function safeInjectAndSend(el) {
    try {
      injectAndSend(el);
    } catch (_e) {
      try {
        fireSend(el);
      } catch (_e2) {
        /* give up silently; the draft is untouched and the user can retry */
      }
    }
  }

  function onKeydown(e) {
    if (bypass || !enabled) return;
    if (e.key !== "Enter" || e.shiftKey || e.ctrlKey || e.metaKey || e.altKey || e.isComposing) return;
    const el = getEditor();
    if (!el) return;
    if (!(e.target === el || el.contains(e.target))) return;
    const text = getText(el);
    if (!text.trim() || D.isPrefixed(text)) return;
    e.preventDefault();
    e.stopImmediatePropagation();
    safeInjectAndSend(el);
  }

  function onClick(e) {
    if (bypass || !enabled) return;
    const btn = getSend();
    if (!btn) return;
    if (!(e.target === btn || btn.contains(e.target))) return;
    const el = getEditor();
    if (!el) return;
    const text = getText(el);
    if (!text.trim() || D.isPrefixed(text)) return;
    e.preventDefault();
    e.stopImmediatePropagation();
    safeInjectAndSend(el);
  }

  document.addEventListener("keydown", onKeydown, true);
  document.addEventListener("click", onClick, true);

  // ---- on-page indicator: dark-glass pill + ember flame (click to toggle off) ----
  const FLAME = ["00011000", "00111100", "00111100", "01122110", "01122110", "11222211", "01122110", "00111100"];
  const STOPS = [
    [0.0, [255, 206, 107]],
    [0.55, [242, 121, 43]],
    [1.0, [207, 74, 31]],
  ];
  function ember(t) {
    for (let i = 1; i < STOPS.length; i++) {
      if (t <= STOPS[i][0]) {
        const [t0, c0] = STOPS[i - 1];
        const [t1, c1] = STOPS[i];
        const k = (t - t0) / (t1 - t0);
        return c0.map((v, j) => Math.round(v + (c1[j] - v) * k));
      }
    }
    return STOPS[STOPS.length - 1][1];
  }
  function flameEl() {
    const g = document.createElement("span");
    g.className = "cm-flame";
    FLAME.forEach((row, r) => {
      row.split("").forEach((ch) => {
        const s = document.createElement("span");
        if (ch !== "0") {
          const lift = ch === "2" ? 28 : 0;
          const [R, G, B] = ember(r / (FLAME.length - 1));
          s.style.background =
            "rgb(" + Math.min(255, R + lift) + "," + Math.min(255, G + lift) + "," + Math.min(255, B + lift) + ")";
        }
        g.appendChild(s);
      });
    });
    return g;
  }

  let indicatorEl = null;
  let indicatorLvl = null;
  function renderIndicator() {
    if (!document.body) {
      document.addEventListener("DOMContentLoaded", renderIndicator, { once: true });
      return;
    }
    if (!indicatorEl) {
      indicatorEl = document.createElement("div");
      indicatorEl.id = "caveman-indicator";
      indicatorEl.setAttribute("role", "button");
      indicatorEl.title = "Caveman mode is on — click to turn off";
      indicatorEl.addEventListener("click", () => {
        chrome.storage.sync.get({ enabled: true }, (s) => chrome.storage.sync.set({ enabled: !s.enabled }));
      });
      indicatorEl.appendChild(flameEl());
      const label = document.createElement("span");
      label.textContent = "Caveman";
      indicatorEl.appendChild(label);
      indicatorLvl = document.createElement("span");
      indicatorLvl.className = "cm-lvl";
      indicatorEl.appendChild(indicatorLvl);
    }
    if (!document.body.contains(indicatorEl)) document.body.appendChild(indicatorEl);
    indicatorEl.style.display = enabled ? "flex" : "none";
    indicatorLvl.textContent = "· " + level;
  }

  // Heartbeat: keep the indicator attached across SPA navigations, and stop
  // cleanly when the extension is reloaded and this script is orphaned.
  const heartbeat = setInterval(() => {
    if (!chrome.runtime || !chrome.runtime.id) {
      clearInterval(heartbeat);
      if (indicatorEl) indicatorEl.remove();
      return;
    }
    if (enabled) renderIndicator();
  }, 1000);
})();
