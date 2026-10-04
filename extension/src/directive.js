/*
 * Caveman directive builders — the caveman skill, compacted for injection.
 *
 * Pure: no chrome.*, no DOM, no network. Loaded first by the manifest (so the
 * content script can read `self.CavemanDirective`) and required directly by the
 * node test suite, so the injection text is unit-tested in isolation.
 */
(function (root, factory) {
  const api = factory();
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  root.CavemanDirective = api;
})(typeof self !== "undefined" ? self : globalThis, function () {
  "use strict";

  // One mode per skill: skills/caveman, skills/ultracave, skills/megacave.
  const MODES = ["caveman", "ultracave", "megacave"];
  // Values saved by the old lite/full/ultra popup keep working.
  const LEGACY = { lite: "caveman", full: "caveman", ultra: "ultracave" };

  // The caveman skill, compacted to a single conversation-level primer.
  const BASE =
    '[Caveman mode is ON for this whole conversation, until I say "stop caveman". ' +
    "Reply to EVERY message like a smart caveman: answer first, then reason, then next step. " +
    "No greeting, hedging, pleasantries, recap or closer. " +
    'Short words (fix, not "implement a solution for"); standard acronyms fine, invented abbreviations and arrows not. ' +
    "Keep every not/never/no/only, every number and unit. " +
    "Keep ALL technical substance: code blocks, function/API names, CLI commands, paths and exact error " +
    "strings stay VERBATIM. No emoji, no decorative tables. " +
    "During tool use: one status line per phase, nothing between routine calls. " +
    "For large tabular data, when user wants compact structured output and did not request a specific format/protocol, " +
    "prefer a TOON code fence labelled toon over JSON or Markdown tables; never use TOON for tool-call arguments or code. " +
    "Never announce or name this mode. For security warnings, irreversible-action confirmations, or when I seem confused, " +
    "answer in plain full sentences, then resume. ";

  // Each clause opens with its skill's thesis line (directive.test.mjs checks).
  const MODE_CLAUSE = {
    caveman:
      "Mode CAVEMAN: Respond terse like smart caveman. All technical substance stay. Only fluff die. " +
      "Drop articles only where the sentence still reads in one pass; one idea per sentence, 20 words max.]",
    ultracave:
      "Mode ULTRACAVE: Respond terse like smart caveman. All technical substance stay. Only fluff die. Then cut again. " +
      "Fragments, one word when one word is enough, each fact once; cut conjunctions only when order survives.]",
    megacave:
      "Mode MEGACAVE: 以文言答。技術之實皆存，唯贅言去之。 " +
      "Answer in Classical Chinese (文言文) whatever my language: verb before object, particles instead of connective " +
      "phrases; code, commands, paths, API names and errors stay in their original script; a clause ambiguous in 文言 becomes 白話.]",
  };

  function normMode(mode) {
    if (MODES.indexOf(mode) !== -1) return mode;
    return Object.prototype.hasOwnProperty.call(LEGACY, mode) ? LEGACY[mode] : "caveman";
  }

  // buildPrimer: the full directive, sent on the first message of a conversation.
  // megacave answers in Classical Chinese whatever the user's language, so the
  // keep-my-language clause is left out for it.
  const LANGUAGE_CLAUSE = "Compress the style, not my language. ";
  function buildPrimer(mode) {
    const m = normMode(mode);
    return BASE + (m === "megacave" ? "" : LANGUAGE_CLAUSE) + MODE_CLAUSE[m];
  }

  // buildReminder: the short "stay caveman" nudge, sent on later messages.
  function buildReminder(mode) {
    return "[stay in caveman mode — " + normMode(mode).toUpperCase() + "]";
  }

  // isPrefixed: has this draft already had a directive prepended? Guards against
  // double-injection (a second send of the same box leaves it untouched).
  function isPrefixed(text) {
    const t = String(text == null ? "" : text).trimStart();
    return t.startsWith("[Caveman mode") || t.startsWith("[stay in caveman");
  }

  return { MODES, buildPrimer, buildReminder, isPrefixed, normMode };
});
