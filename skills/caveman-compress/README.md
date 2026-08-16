<p align="center">
  <img src="https://em-content.zobj.net/source/apple/391/rock_1faa8.png" width="80" />
</p>

<h1 align="center">caveman-compress</h1>

<p align="center">
  <strong>shrink memory file. save token every session.</strong>
</p>

---

A Claude Code skill that compresses project memory files (`CLAUDE.md`, todos,
preferences) into caveman format, reducing repeated input size.

Claude loads `CLAUDE.md` on every session start, so large files add repeated
input tokens. Caveman shortens supported natural-language files.

## What It Do

```
/caveman-compress CLAUDE.md
```

```
CLAUDE.md          ← compressed (Claude reads smaller file each session)
CLAUDE.original.md ← human-readable backup (you edit this)
```

Original remains in data directory rather than next to live file, so skill
auto-loaders do not read it twice. Path is
`$XDG_DATA_HOME/caveman-compress/backups/<parent-dir-name>/` on macOS and Linux,
or `%LOCALAPPDATA%\caveman-compress\backups\<parent-dir-name>\` on Windows. Edit
`.original.md` there, then run skill again to re-compress.

## Benchmarks

Real results on real project files:

| File | Original | Compressed | Saved |
|------|----------:|----------:|------:|
| `claude-md-preferences.md` | 706 | 285 | 59.6% |
| `project-notes.md` | 1145 | 535 | 53.3% |
| `claude-md-project.md` | 1122 | 636 | 43.3% |
| `todo-list.md` | 627 | 388 | 38.1% |
| `mixed-with-code.md` | 888 | 560 | 36.9% |
| Average | 898 | 481 | 46% |

All fixture validations passed: headings, code blocks, URLs, and file paths were
preserved exactly.

## Before / After

<table>
<tr>
<td width="50%">

### Original (706 tokens)

> "I strongly prefer TypeScript with strict mode enabled for all new code. Please don't use `any` type unless there's genuinely no way around it, and if you do, leave a comment explaining the reasoning. I find that taking the time to properly type things catches a lot of bugs before they ever make it to runtime."

</td>
<td width="50%">

### <img src="../../docs/assets/dancing-rock.svg" width="20" height="20" alt="rock"/> Caveman (285 tokens)

> "Prefer TypeScript strict mode always. No `any` unless unavoidable; comment why if used. Proper types catch bugs early."

</td>
</tr>
</table>

This fixture produced 59.6% fewer counted tokens. Structural validation passed;
result does not prove semantic equivalence on other files or models.

## Security

`caveman-compress` is flagged as Snyk High Risk due to subprocess and file I/O
patterns detected by static analysis. See [SECURITY.md](./SECURITY.md) for why
these operations exist and how paths are constrained.

## Install

Compress is built in with the `caveman` plugin. Install `caveman` once, then use `/caveman-compress`.

If you need local files, the compress skill lives at:

```bash
skills/caveman-compress/
```

Requires Python 3.10 or newer.

## Usage

```
/caveman-compress <filepath>
```

Examples:
```
/caveman-compress CLAUDE.md
/caveman-compress docs/preferences.md
/caveman-compress todos.md
```

### What files work

| Type | Compress? |
|------|-----------|
| `.md`, `.txt`, `.rst`, `.typ`, `.typst`, `.tex` | Yes |
| Extensionless natural language | Yes |
| `.py`, `.js`, `.ts`, `.json`, `.yaml` | ❌ Skip (code/config) |
| `*.original.md` | ❌ Skip (backup files) |

## How It Work

```
/caveman-compress CLAUDE.md
        ↓
detect file type        (no tokens)
        ↓
Claude compresses       (tokens: one call)
        ↓
validate output         (no tokens)
  checks: headings, code blocks, URLs, file paths, bullets
        ↓
if errors: Claude fixes cherry-picked issues only   (tokens: targeted fix)
  does NOT recompress; only patches broken parts
        ↓
retry up to 2 times
        ↓
write compressed → CLAUDE.md
write original   → CLAUDE.original.md
```

Only two things use tokens: initial compression + targeted fix if validation fails. Everything else is local Python.

## What Is Preserved

Caveman compress natural language. It never touch:

- Code blocks (` ``` ` fenced or indented)
- Inline code (`` `backtick content` ``)
- URLs and links
- File paths (`/src/components/...`)
- Commands (`npm install`, `git commit`)
- Technical terms, library names, API names
- Headings (exact text preserved)
- Tables (structure preserved, cell text compressed)
- Dates, version numbers, numeric values

## Why This Matter

`CLAUDE.md` loads on every session start. A 1,000-token project memory file adds
1,000 input tokens each time project opens, or 100,000 across 100 sessions.

Caveman reduced counted tokens by about 46% on five listed fixtures. Validators
confirmed headings, code blocks, URLs, and file paths. They did not establish
general semantic or task-quality equivalence.

```
┌────────────────────────────────────────────┐
│  TOKEN SAVINGS PER FILE    █████       46% │
│  FIXTURES IN TABLE                       5 │
│  STRUCTURAL VALIDATION       passed on all │
│  SETUP TIME                █            1x │
└────────────────────────────────────────────┘
```

## Part of Caveman

This skill is part of the [caveman](https://github.com/JuliusBrussee/caveman) toolkit.

- `caveman`: ask Claude to answer in shorter prose
- `caveman-compress`: shorten supported project-memory files with backups and validation
