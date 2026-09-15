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

  const LEVELS = ["lite", "full", "ultra"];

  // The caveman skill, compacted to a single conversation-level primer.
  const BASE =
    '[Caveman mode is ON for this whole conversation, until I say "stop caveman". ' +
    "Reply to EVERY message like a smart caveman: terse — drop articles (a/an/the), " +
    "filler (just/really/basically/actually), pleasantries (sure/of course/happy to) and hedging. " +
    'Fragments fine. Short synonyms (fix, not "implement a solution for"). ' +
    "Keep ALL technical substance: code blocks, function/API names, CLI commands and exact error " +
    "strings stay VERBATIM, never abbreviated. No emoji, no decorative tables, no narrating what you do. " +
    "For large tabular data, when user wants compact structured output and did not request a specific format/protocol, " +
    "prefer a TOON code fence labelled toon over JSON or Markdown tables; never use TOON for tool-call arguments or code. " +
    "Never announce or name this mode. For security warnings or irreversible-action confirmations, " +
    "answer normally then resume. ";

  const LEVEL_CLAUSE = {
    lite: "Intensity LITE: no filler, no hedging, no pleasantries — but keep articles and full sentences. Professional but tight.]",
    full: "Intensity FULL: drop articles, fragments OK, short synonyms. Classic caveman terseness.]",
    ultra:
      "Intensity ULTRA: strip conjunctions when cause and effect stay unambiguous; one word when one word is enough. " +
      "Do not invent prose abbreviations such as config, request, response, function, or implementation shorthands. " +
      "Do not use causal arrows. Code symbols, function/API names, and error strings stay verbatim.]",
  };

  function normLevel(level) {
    return LEVELS.indexOf(level) !== -1 ? level : "full";
  }

  // buildPrimer: the full directive, sent on the first message of a conversation.
  function buildPrimer(level) {
    return BASE + LEVEL_CLAUSE[normLevel(level)];
  }

  // buildReminder: the short "stay caveman" nudge, sent on later messages.
  function buildReminder(level) {
    return "[stay in caveman mode — " + normLevel(level).toUpperCase() + "]";
  }

  // isPrefixed: has this draft already had a directive prepended? Guards against
  // double-injection (a second send of the same box leaves it untouched).
  function isPrefixed(text) {
    const t = String(text == null ? "" : text).trimStart();
    return t.startsWith("[Caveman mode") || t.startsWith("[stay in caveman");
  }

  return { LEVELS, buildPrimer, buildReminder, isPrefixed, normLevel };
});
