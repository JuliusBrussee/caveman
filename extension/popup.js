/* Caveman Mode popup. */

// Fallback shim so the popup also renders when opened directly (file://) for a
// design preview; the real extension always has chrome.storage.
const store =
  typeof chrome !== "undefined" && chrome.storage
    ? chrome.storage.sync
    : {
        _d: JSON.parse(localStorage.getItem("caveman") || "{}"),
        get(defs, cb) {
          const o = {};
          for (const k in defs) o[k] = k in this._d ? this._d[k] : defs[k];
          cb(o);
        },
        set(obj, cb) {
          Object.assign(this._d, obj);
          localStorage.setItem("caveman", JSON.stringify(this._d));
          cb && cb();
        },
      };

// ── pixel-flame logo (the CaveMark, 8×8) — white on onyx, no colour ─────────
const FLAME = [
  "00011000",
  "00111100",
  "00111100",
  "01100110",
  "01100110",
  "11000011",
  "01100110",
  "00111100",
];
function buildFlame(target) {
  FLAME.forEach((row) => {
    row.split("").forEach((ch) => {
      const s = document.createElement("span");
      s.style.background = ch === "0" ? "transparent" : "#ffffff";
      target.appendChild(s);
    });
  });
}
buildFlame(document.getElementById("flame"));

// ── settings wiring ──────────────────────────────────────────────────────
const HINTS = {
  lite: "No filler or hedging. Keeps full sentences.",
  full: "Classic caveman. Drops articles, fragments OK.",
  ultra: "Max compression. No invented abbreviations or causal arrows.",
};

function load() {
  store.get({ enabled: true, level: "full", sites: {} }, (s) => {
    document.getElementById("master").checked = s.enabled;
    document.body.dataset.enabled = s.enabled ? "1" : "0";
    document.querySelectorAll('input[name="level"]').forEach((r) => {
      r.checked = r.value === s.level;
    });
    document.getElementById("levelHint").textContent = HINTS[s.level] || "";
    document.querySelectorAll("input[data-site]").forEach((c) => {
      c.checked = s.sites[c.dataset.site] !== false; // default on
    });
  });
}

document.getElementById("master").addEventListener("change", (e) => {
  store.set({ enabled: e.target.checked });
  document.body.dataset.enabled = e.target.checked ? "1" : "0";
});

document.querySelectorAll('input[name="level"]').forEach((r) => {
  r.addEventListener("change", (e) => {
    if (!e.target.checked) return;
    store.set({ level: e.target.value });
    document.getElementById("levelHint").textContent = HINTS[e.target.value] || "";
  });
});

document.querySelectorAll("input[data-site]").forEach((c) => {
  c.addEventListener("change", (e) => {
    store.get({ sites: {} }, (s) => {
      const sites = s.sites || {};
      sites[e.target.dataset.site] = e.target.checked;
      store.set({ sites });
    });
  });
});

document.getElementById("brand").addEventListener("click", (e) => e.preventDefault());

// Review CTA → the Chrome Web Store reviews tab for THIS install. The id is read
// from chrome.runtime at runtime (in a published install it is the store id), so
// there is no hardcoded extension id to drift; the file:// preview shim falls
// back to the store home.
(() => {
  const link = document.getElementById("review");
  if (!link) return;
  const id = typeof chrome !== "undefined" && chrome.runtime && chrome.runtime.id;
  link.href = id ? `https://chromewebstore.google.com/detail/${id}/reviews` : "https://chromewebstore.google.com/";
})();

load();
