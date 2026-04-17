---
name: caveman-compress
description: >
  Compress natural language memory files (CLAUDE.md, todos, preferences) into caveman format
  to save input tokens. Preserves all technical substance, code, URLs, and structure.
  Compressed version overwrites the original file. Human-readable backup saved as FILE.original.md.
  Trigger: /caveman:compress <filepath> or "compress memory file"
---

# Caveman Compress

Compress natural language files into caveman-speak to reduce input tokens. Backup saved as `<filename>.original.md`.

## Trigger

`/caveman:compress <filepath>` or when user asks to compress a memory file.

## Process

1. Find the `caveman-compress/scripts/` directory relative to this project root.
2. Run: `cd <project_root>/caveman-compress && python3 -m scripts <absolute_filepath>`
3. The CLI detects file type, calls Claude to compress, validates output, retries up to 2x on errors.
4. Return result to user.

## Compression Rules

### Remove
- Articles: a, an, the
- Filler: just, really, basically, actually, simply, essentially
- Pleasantries, hedging, redundant phrasing
- Connective fluff: however, furthermore, additionally

### Preserve EXACTLY
- Code blocks (fenced and indented)
- Inline code (`backtick content`)
- URLs, file paths, commands
- Technical terms, proper nouns
- Dates, version numbers, numeric values
- Environment variables

### Preserve Structure
- All markdown headings (exact text, compress body below)
- Bullet/numbered list hierarchy
- Tables (compress cell text, keep structure)
- Frontmatter/YAML headers

### Compress
- Short synonyms: "big" not "extensive", "fix" not "implement a solution for"
- Fragments OK: "Run tests before commit" not "You should always run tests before committing"
- Merge redundant bullets saying same thing differently

## Boundaries

- ONLY compress natural language files (.md, .txt)
- NEVER modify: .py, .js, .ts, .json, .yaml, .yml, .toml, .env, .lock, .css, .html
- Never compress FILE.original.md (skip it)
- Original backed up before overwriting
