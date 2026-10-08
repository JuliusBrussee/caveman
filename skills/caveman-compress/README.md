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

The five fixture pairs in [`tests/caveman-compress/`](../../tests/caveman-compress/),
counted with tiktoken o200k. Reproduce with
`uv run --with tiktoken python skills/caveman-compress/scripts/benchmark.py`.

| File | Original | Compressed | Saved |
|------|----------:|----------:|------:|
| `claude-md-preferences.md` | 827 | 421 | 49.1% |
| `claude-md-project.md` | 1628 | 1117 | 31.4% |
| `mixed-with-code.md` | 1432 | 1106 | 22.8% |
| `project-notes.md` | 1431 | 846 | 40.9% |
| `todo-list.md` | 880 | 648 | 26.4% |
| Total | 6198 | 4138 | 33.2% |

All fixture validations passed: headings, code blocks, URLs, and file paths were
preserved exactly.

## Before / After

<table>
<tr>
<td width="50%">

### Original (827 tokens)

> "I strongly prefer TypeScript with strict mode enabled for all new code. Please don't use `any` type unless there's genuinely no way around it, and if you do, leave a comment explaining the reasoning. I find that taking the time to properly type things catches a lot of bugs before they ever make it to runtime."

</td>
<td width="50%">

### <img src="../../docs/assets/dancing-rock.svg" width="20" height="20" alt="rock"/> Caveman (421 tokens)

> "Prefer TypeScript strict mode always. No `any` unless unavoidable; comment why if used. Proper types catch bugs early."

</td>
</tr>
</table>

This fixture produced 49.1% fewer counted tokens. Structural validation passed;
result does not prove semantic equivalence on other files or models.

## Security

`caveman-compress` is flagged as Snyk High Risk due to subprocess and file I/O
patterns detected by static analysis. See [SECURITY.md](./SECURITY.md) for why
these operations exist and how paths are constrained.

## Install

Compress is built into the `caveman` plugin. Install `caveman` once, then use `/caveman-compress`.

If you need local files, the compress skill lives at:

```bash
skills/caveman-compress/
```

Requires Python 3.10 or newer.

### Provider / model

Default path uses Claude: `ANTHROPIC_API_KEY` + Anthropic SDK when set, else `claude --print`.

To use opencode instead (any model opencode can reach, free ones included):

```bash
export CAVEMAN_COMPRESS_PROVIDER=opencode
export CAVEMAN_COMPRESS_MODEL=opencode/big-pickle   # any id from `opencode models`
```

To use a local model (or any OpenAI-compatible server) instead:

```bash
export CAVEMAN_COMPRESS_PROVIDER=openai-compat
export CAVEMAN_COMPRESS_MODEL=qwen3:8b                         # required
export CAVEMAN_COMPRESS_ENDPOINT=http://localhost:11434/v1     # default (Ollama)
# export CAVEMAN_COMPRESS_API_KEY=...                          # optional, sent as Bearer token
```

Common endpoints: Ollama `http://localhost:11434/v1`, LM Studio `http://localhost:1234/v1`, llama.cpp server `http://localhost:8080/v1`, vLLM `http://localhost:8000/v1`. Nothing leaves your machine with a local server. A non-local `http://` endpoint sends the file in plaintext; use `https://` for remote servers. Small local models get it wrong more often; when they do, validation fails and your file stays untouched.

`CAVEMAN_MODEL` is the fallback when `CAVEMAN_COMPRESS_MODEL` is unset. On the Claude path the model also applies to `claude --print`. The opencode path needs opencode 2.x (it uses `opencode run --standalone`).

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
| `.md`, `.mdc`, `.txt`, `.rst`, `.typ`, `.typst`, `.tex` | Yes |
| Extensionless natural language | Yes |
| `.py`, `.js`, `.ts`, `.json`, `.yaml` | ❌ Skip (code/config) |
| `*.original.md` | ❌ Skip (backup files) |

## How It Work

```
/caveman-compress CLAUDE.md
        ↓
basic checks: file exists, under 500KB, not a sensitive filename
        ↓
acquire cross-session lock on the file  (waits up to 15 min if another run holds it, then errors)
        ↓
detect file type        (no tokens)
        ↓
configured provider compresses       (tokens: one call)
        ↓
validate output         (no tokens)
  checks: headings, code blocks, URLs, file paths, bullets
        ↓
if errors: configured provider fixes cherry-picked issues only   (tokens: targeted fix)
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
- Anything you wrap in `<!-- nocompress -->` ... `<!-- /nocompress -->`

Want a part left exactly as written (an `<example>` block, a prompt template)? Wrap it. Each tag go on its own line. Model never change what inside; it come back byte for byte. Model can still read it if first try need fixing, so not hiding place for secrets. Forget the closing tag and compress stop before touching file.

```markdown
<!-- nocompress -->
<example>
Reply in this exact format.
</example>
<!-- /nocompress -->
```

## Why This Matter

`CLAUDE.md` loads on every session start. A 1,000-token project memory file adds
1,000 input tokens each time project opens, or 100,000 across 100 sessions.

Caveman reduced counted tokens by 33.2% in total on the five listed fixtures
(22.8% to 49.1% per file). Validators confirmed headings, code blocks, URLs,
and file paths. They did not establish general semantic or task-quality
equivalence.

```
┌────────────────────────────────────────────┐
│  TOKEN SAVINGS, ALL FIVE   ███       33.2% │
│  FIXTURES IN TABLE                       5 │
│  STRUCTURAL VALIDATION       passed on all │
│  SETUP TIME                █            1x │
└────────────────────────────────────────────┘
```

## Part of Caveman

This skill is part of the [caveman](https://github.com/JuliusBrussee/caveman) toolkit.

- `caveman`: ask Claude to answer in shorter prose
- `caveman-compress`: shorten supported project-memory files with backups and validation
