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
      editor: ['textarea[data-mobile-composer-prompt]', "#prompt-textarea", 'div.ProseMirror[contenteditable="true"]'],
      send: ['button[data-composer-submit]', 'button[data-testid="send-button"]', "#composer-submit-button", 'button[aria-label="Send prompt"]', 'button[aria-label="Send message"]'],
      message: ["[data-message-author-role]"],
    },
    "chat.openai.com": {
      editor: ['textarea[data-mobile-composer-prompt]', "#prompt-textarea", 'div.ProseMirror[contenteditable="true"]'],
      send: ['button[data-composer-submit]', 'button[data-testid="send-button"]', "#composer-submit-button", 'button[aria-label="Send prompt"]', 'button[aria-label="Send message"]'],
      message: ["[data-message-author-role]"],
    },
    "claude.ai": {
      editor: ['[data-testid="chat-input"][contenteditable="true"]', 'div.ProseMirror[contenteditable="true"]'],
      send: ['button[data-testid="chat-input-send"]', 'button[aria-label="Send message"]', 'button[aria-label="Send Message"]'],
      message: ['[data-testid="user-message"]', "div.font-claude-message"],
    },
    "gemini.google.com": {
      // `button.send-button` was removed in a Gemini redesign; the live control is
      // a Material icon button labelled "Send message" — keep both, but never
      // match unrelated actions such as "Send feedback".
      editor: ['.ql-editor[contenteditable="true"]', 'rich-textarea div[contenteditable="true"]'],
      send: ['button[aria-label="Send message"]', "button.send-button", 'button[mattooltip="Send message"]'],
      message: ["user-query", "model-response"],
    },
  };

  const cfg = SITES[HOST];
  if (!cfg) return;

  // ---- live state from storage ----
  let enabled = false;
  let level = "full";
  let bypass = false; // true only during our synchronous button click
  let pending = null;
  let settingsVersion = 0;

  function cancelPending() {
    if (pending) clearTimeout(pending.timer);
    pending = null;
  }

  function refresh() {
    const version = ++settingsVersion;
    cancelPending();
    chrome.storage.sync.get({ enabled: true, level: "full", sites: {} }, (s) => {
      if (version !== settingsVersion) return;
      const siteOn = (s.sites || {})[HOST === "chat.openai.com" ? "chatgpt.com" : HOST] !== false;
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
      let matches = [];
      try {
        matches = document.querySelectorAll(sel);
      } catch (_e) {
        continue; // a selector the browser can't parse — skip it
      }
      for (const el of matches) if (isVisible(el)) return el;
    }
    return null;
  }
  function getEditor() {
    return pick(cfg.editor);
  }
  // A button is a usable send target only if it is visible, NOT disabled (the
  // sites gate with either the `disabled` prop OR `aria-disabled`), and is not the
  // Stop/abort button — during generation the sites swap Send for a Stop control
  // that can share the composer's button slot; clicking it would abort the reply
  // and never send.
  const SEND_NEG = /\b(stop|abort|cancel)\b/i;
  function looksSendable(btn, allowDisabled = false) {
    if (!btn || !isVisible(btn)) return false;
    if (!allowDisabled && (btn.disabled || btn.getAttribute("aria-disabled") === "true")) return false;
    const meta =
      (btn.getAttribute("aria-label") || "") + " " + (btn.getAttribute("data-testid") || "") + " " + (btn.title || "");
    return !SEND_NEG.test(meta);
  }
  function composerBox(ed) {
    // Stop at the editor's form, or the nearest container with a known send
    // control. Never search the document/body for a loosely named action.
    const form = ed.closest("form");
    // Only when the form actually holds the send control. A composer whose
    // button lives outside its form (portal, sibling toolbar) would otherwise
    // stop the search at a container getSend can never resolve in, and the
    // extension would be silently inert on that site.
    if (form && cfg.send.some((selector) => form.querySelector(selector))) return form;
    let box = ed.parentElement;
    for (let i = 0; i < 10 && box && box !== document.body && box !== document.documentElement; i++, box = box.parentElement) {
      if (cfg.send.some((selector) => box.querySelector(selector))) return box;
    }
    return null;
  }
  function getSend(box, allowDisabled = false) {
    if (!box) return null;
    for (const selector of cfg.send) {
      for (const button of box.querySelectorAll(selector)) {
        if (looksSendable(button, allowDisabled)) return button;
      }
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

  // Prepend through the editor's input path. Returns true on success.
  //
  // For rich editors (ProseMirror / Quill / Lexical) we drive the SAME path the
  // framework listens to — focus, collapsed selection, insertText — so its
  // document model updates. Never reconstruct the existing rich document from
  // innerText: that discards embedded nodes and can multiply blank lines.
  function prependText(el, prefix) {
    el.focus();
    if (isTextarea(el)) {
      const setter = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, "value").set;
      setter.call(el, prefix + el.value);
      el.dispatchEvent(new Event("input", { bubbles: true }));
      return true;
    }
    const sel = window.getSelection();
    const range = document.createRange();
    range.selectNodeContents(el);
    range.collapse(true);
    sel.removeAllRanges();
    sel.addRange(range);
    try {
      return document.execCommand("insertText", false, prefix) === true;
    } catch (_e) {
      sel.collapseToEnd();
      return false;
    }
  }

  // A delayed render must never redirect the user's send to a different draft
  // or conversation. If the original transaction changes, leave the draft for
  // the user to send explicitly. An unknown button is not a reason to synthesize
  // Enter: it can mean newline, Stop, or another action in the current site state.
  function fireSend(transaction) {
    cancelPending();
    pending = transaction;
    let tries = 0;
    const tick = () => {
      if (pending !== transaction) return;
      const { editor, box, href, draft, richDraft, version } = transaction;
      if (!enabled || version !== settingsVersion || location.href !== href ||
          !editor.isConnected || !box.isConnected || !box.contains(editor) ||
          getEditor() !== editor || getText(editor) !== draft ||
          (richDraft !== undefined && editor.innerHTML !== richDraft)) {
        cancelPending();
        return;
      }
      const btn = getSend(box);
      if (btn) {
        pending = null;
        bypass = true;
        try { btn.click(); } finally { bypass = false; }
        return;
      }
      if (tries++ < 16) {
        transaction.timer = setTimeout(tick, 50);
        return;
      }
      cancelPending();
    };
    transaction.timer = setTimeout(tick, 20);
  }

  function injectAndSend(el, box) {
    const original = getText(el);
    const transaction = { editor: el, box, href: location.href, version: settingsVersion };
    // "First message of this conversation" — the only state that earns the full
    // primer — is when no messages have rendered yet. Keying off the live message
    // count (not a per-load flag) means reloading or deep-linking into an existing
    // chat correctly gets the short reminder, not another full primer.
    const isFirst = messageCount() === 0;
    const prefix = isFirst ? D.buildPrimer(level) : D.buildReminder(level);
    let ok = false;
    try { ok = prependText(el, prefix + "\n\n"); } catch (_e) { /* check the draft below */ }
    if (!ok) {
      // Only replay an unsuccessful edit when the original draft is intact.
      // A partial edit stays visible for the user to review; do not destroy rich
      // content while trying to restore it from plain text.
      if (getText(el) !== original) return;
    }
    transaction.draft = getText(el);
    if (!isTextarea(el)) transaction.richDraft = el.innerHTML;
    if (ok && (!transaction.draft.startsWith(prefix) || !transaction.draft.endsWith(original) ||
        !/^\n{2,}$/.test(transaction.draft.slice(prefix.length, -original.length)))) return;
    if (!transaction.draft.trim()) return;
    fireSend(transaction);
  }

  function stopRequested(text) {
    if (!/^\s*stop caveman[.!]?\s*$/i.test(text)) return false;
    ++settingsVersion;
    cancelPending();
    enabled = false;
    renderIndicator();
    chrome.storage.sync.set({ enabled: false });
    return true;
  }

  function intercept(e, el, box) {
    cancelPending();
    const text = getText(el);
    // Send the user's stop command unchanged and persist the same switch used by
    // the popup. Merely asking the model to stop would be undone next turn.
    if (stopRequested(text) || !text.trim() || D.isPrefixed(text)) return;
    e.preventDefault();
    e.stopImmediatePropagation();
    try { injectAndSend(el, box); } catch (_e) { cancelPending(); }
  }

  // ---- intercept the send gesture (capture phase, so we beat the app) ----
  function onKeydown(e) {
    if (bypass || !enabled) return;
    // At an IME composition boundary isComposing can already be false while the
    // Enter event still carries keyCode 229. It confirms text, not a chat send.
    if (e.key !== "Enter" || e.shiftKey || e.ctrlKey || e.metaKey || e.altKey || e.isComposing || e.keyCode === 229) return;
    const el = getEditor();
    if (!el) return;
    if (!(e.target === el || el.contains(e.target))) return;
    const box = composerBox(el);
    if (!box || !getSend(box, true)) return;
    intercept(e, el, box);
  }

  function onClick(e) {
    if (bypass || !enabled) return;
    const el = getEditor();
    if (!el) return;
    const box = composerBox(el);
    const btn = getSend(box);
    if (!btn || !(e.target === btn || btn.contains(e.target))) return;
    intercept(e, el, box);
  }

  document.addEventListener("keydown", onKeydown, true);
  document.addEventListener("click", onClick, true);
  document.addEventListener("input", (e) => {
    // Formatting and reference changes can keep innerText identical. Any new
    // input in this editor invalidates the send the user requested earlier.
    if (pending && (e.target === pending.editor || pending.editor.contains(e.target))) cancelPending();
  }, true);
  window.addEventListener("popstate", cancelPending);
  window.addEventListener("hashchange", cancelPending);
  window.addEventListener("pagehide", cancelPending);
  window.navigation?.addEventListener("navigate", cancelPending);

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
      enabled = false;
      cancelPending();
      clearInterval(heartbeat);
      if (indicatorEl) indicatorEl.remove();
      return;
    }
    if (enabled) renderIndicator();
  }, 1000);
})();
