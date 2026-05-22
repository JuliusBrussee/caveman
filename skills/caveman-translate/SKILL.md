---
name: caveman-translate
description: >
  Translate wenyan (Classical Chinese) or caveman-compressed output back into clear English.
  Useful when running in wenyan mode — get token-efficient output, then translate on demand.
  Preserves all code blocks, technical terms, file paths, and URLs exactly unchanged.
  Trigger: /caveman-translate or "translate that", "translate wenyan", "what does this mean in English".
---

Translate the provided text into clear, readable English. Full sentences. Normal grammar. Professional tone.

## What to translate

- **Wenyan / Classical Chinese** (文言文): `池reuse conn。skip handshake → fast。` → "Connection pooling reuses open connections instead of creating new ones per request, avoiding repeated handshake overhead."
- **Caveman compressed English**: `Bug auth middleware. Token expiry check use < not <=. Fix:` → "There is a bug in the authentication middleware. The token expiry check uses `<` instead of `<=`. Here is the fix:"
- **Mixed wenyan+code output**: translate prose portions, leave code blocks exactly as-is

## Rules

**Translate:**
- Classical Chinese characters and particles (之/乃/為/其) → English prose
- Caveman fragments → full sentences with articles restored
- Abbreviations (DB, auth, config, req, res, fn) → spelled out where helpful: "database", "authentication", "configuration", "request", "response", "function"
- Arrows used for causality (`→`) → "which causes", "resulting in", or "so"
- Omitted subjects → restore from context

**Never touch:**
- Code blocks (fenced ` ``` ` and indented) — copy exactly
- Inline code (`` `backtick content` ``) — copy exactly
- URLs and markdown links — copy exactly
- File paths, commands, environment variables — copy exactly
- Technical names: library names, API names, function names, protocol names
- Proper nouns: project names, company names, people

## Examples

**Wenyan-full input:**
> 物出新參照，致重繪。useMemo Wrap之。

**Translation:**
> Creating a new object reference on each render triggers a re-render. Wrap the value in `useMemo`.

---

**Caveman-ultra input:**
> Pool = reuse DB conn. Skip handshake → fast under load.

**Translation:**
> Connection pooling reuses open database connections instead of creating new ones per request. This skips the handshake overhead, resulting in faster response times under load.

---

**Mixed input:**
> 池reuse open connection。不每req新開。skip handshake overhead。
> ```js
> const pool = new Pool({ max: 10 });
> ```
> ultra快。

**Translation:**
> The pool reuses open connections and does not open a new one per request, skipping handshake overhead.
> ```js
> const pool = new Pool({ max: 10 });
> ```
> This is extremely fast.

## Format

Output the translation as a clean prose block — same heading structure and bullet hierarchy as the original if present, but with full English sentences inside each item. Do not add commentary like "Here is the translation:" — just output the translated text directly.

## Boundaries

Translates text only — does not re-run the original query, does not change caveman mode, does not modify any files. After translating, caveman mode stays at whatever level it was set to. "stop caveman-translate" or "done translating": acknowledge and resume normal behavior.
