---
name: caveman-compress
description: >
  Compress natural language memory files (CLAUDE.md, todos, preferences) into caveman format
  to save input tokens. Preserves all technical substance, code, URLs, and structure.
  Compressed version overwrites the original file. Human-readable backup saved as FILE.original.md.
  Trigger: /caveman-compress FILEPATH or "compress memory file"
---

# Caveman Compress

## Purpose

Compress natural language files (CLAUDE.md, todos, preferences) into caveman-speak to reduce input tokens. Compressed version overwrites original. Human-readable backup saved as `<filename>.original.md`, but NOT beside the source file — it lives in an out-of-tree data dir (`$XDG_DATA_HOME/caveman-compress/backups/<parent-dir-name>/`, or `%LOCALAPPDATA%\caveman-compress\backups\<parent-dir-name>\` on Windows) so skill auto-loaders don't re-ingest it as a live file.

## Trigger

`/caveman-compress <filepath>` or when user asks to compress a memory file.

## Process

1. The compression scripts live in `scripts/` (adjacent to this SKILL.md). If the path is not immediately available, search for `scripts/__main__.py` next to this SKILL.md.

2. From the directory containing this SKILL.md, run:

python3 -m scripts <absolute_filepath>

3. The CLI will:
- detect file type (no tokens)
- collapse known wordy phrases via a fixed lookup table (no tokens, no model call — see "Deterministic phrase pre-pass" below)
- call Claude to compress what's left
- validate output (no tokens)
- if errors: cherry-pick fix with Claude (targeted fixes only, no recompression)
- retry up to 2 times
- if still failing after 2 retries: report error to user, leave original file untouched

4. Return result to user

## Deterministic Phrase Pre-Pass

Before the file ever reaches Claude, `scripts/phrase_map.py` runs a fixed
find-and-replace over the prose (skipping code blocks and inline code): known
wordy phrases collapse to their single-word equivalent — `"due to the fact
that"` → `"because"`, `"in order to"` → `"to"`, `"with regard to"` → `"about"`,
and about 215 more in `PHRASE_MAP` — spanning plain-language style guides,
legal/government plain-English guidance, academic writing conciseness
guides, and technical-documentation style guides (Microsoft's and Google's
own developer-docs guides) — hand-filtered so every replacement is a
grammatically safe drop-in regardless of what follows it (see the module
docstring for what was excluded and why, including several plain-language-
guide staples that turned out to break on common follow-on prepositions).
Matching is word-boundary-anchored (won't fire mid-word, e.g. inside
"checkpoint") and runs to a fixed point per prose segment, so a
replacement that creates a new matchable phrase gets caught too.

**Why a table instead of just letting Claude do it:** Claude already collapses
phrases like this per the Compression Rules below — this pre-pass exists to
do the part that's decidable ahead of time for free, shrinking the prompt
Claude receives instead of paying a model call to reach the same conclusion.
It only ever fires on multi-word phrases, not single-word synonyms — swapping
`"extensive"` for `"big"` costs about the same number of tokens either way, so
that kind of swap saves nothing and isn't in the table. A 3-word phrase
collapsing to a 1-word replacement is a real, mechanical token cut.

**Measured impact is conditional on writing style.** Run against this
repo's own `tests/caveman-compress/*.original.md` fixtures (terse
engineering notes and PR-style writing), it cuts ~0.02% — essentially
nothing, because that style of prose doesn't use the formal phrases in
the table. Run against deliberately wordy/corporate-style prose, it cut
~32% of tokens before Claude even saw the text. Run against instructional
README-style prose ("please make sure that...", "this allows you to...",
"it is recommended that you..."), it cut ~26%. It helps most on verbose
writing people don't realize is bloated (meeting notes, policy docs,
corporate email, over-explained setup instructions pasted into a memory
file); it does close to nothing on prose that's already terse. See
`tests/test_phrase_map.py`'s
`PhraseMapTokenReductionTests` for the reproducible measurement.

## Compression Rules

### Remove
- Articles: a, an, the
- Filler: just, really, basically, actually, simply, essentially, generally
- Pleasantries: "sure", "certainly", "of course", "happy to", "I'd recommend"
- Hedging: "it might be worth", "you could consider", "it would be good to"
- Redundant phrasing: "in order to" → "to", "make sure to" → "ensure", "the reason is because" → "because"
- Connective fluff: "however", "furthermore", "additionally", "in addition"

### Preserve EXACTLY (never modify)
- Code blocks (fenced ``` and indented)
- Inline code (`backtick content`)
- URLs and links (full URLs, markdown links)
- File paths (`/src/components/...`, `./config.yaml`)
- Commands (`npm install`, `git commit`, `docker build`)
- Technical terms (library names, API names, protocols, algorithms)
- Proper nouns (project names, people, companies)
- Dates, version numbers, numeric values
- Environment variables (`$HOME`, `NODE_ENV`)

### Preserve Structure
- All markdown headings (keep exact heading text, compress body below)
- Bullet point hierarchy (keep nesting level)
- Numbered lists (keep numbering)
- Tables (compress cell text, keep structure)
- Frontmatter/YAML headers in markdown files

### Compress
- Use short synonyms: "big" not "extensive", "fix" not "implement a solution for", "use" not "utilize"
- Fragments OK: "Run tests before commit" not "You should always run tests before committing"
- Drop "you should", "make sure to", "remember to" — just state the action
- Merge redundant bullets that say the same thing differently
- Keep one example where multiple examples show the same pattern

CRITICAL RULE:
Anything inside ``` ... ``` must be copied EXACTLY.
Do not:
- remove comments
- remove spacing
- reorder lines
- shorten commands
- simplify anything

Inline code (`...`) must be preserved EXACTLY.
Do not modify anything inside backticks.

If file contains code blocks:
- Treat code blocks as read-only regions
- Only compress text outside them
- Do not merge sections around code

## Pattern

Original:
> You should always make sure to run the test suite before pushing any changes to the main branch. This is important because it helps catch bugs early and prevents broken builds from being deployed to production.

Compressed:
> Run tests before push to main. Catch bugs early, prevent broken prod deploys.

Original:
> The application uses a microservices architecture with the following components. The API gateway handles all incoming requests and routes them to the appropriate service. The authentication service is responsible for managing user sessions and JWT tokens.

Compressed:
> Microservices architecture. API gateway route all requests to services. Auth service manage user sessions + JWT tokens.

## Boundaries

- ONLY compress natural language files (.md, .txt, .typ, .typst, .tex, extensionless)
- NEVER modify: .py, .js, .ts, .json, .yaml, .yml, .toml, .env, .lock, .css, .html, .xml, .sql, .sh
- If file has mixed content (prose + code), compress ONLY the prose sections
- If unsure whether something is code or prose, leave it unchanged
- Original file is backed up as FILE.original.md before overwriting — in the out-of-tree backup data dir (see Purpose), not beside the source file
- Never compress FILE.original.md (skip it)
