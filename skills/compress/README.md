# compress

Squeeze prose files into caveman speak. Save input tokens forever.

## What it does

Compresses natural language memory files (CLAUDE.md, todos, preferences) into caveman-style prose to cut input tokens on every future session. The compressed version overwrites the original; a human-readable backup is saved as `<filename>.original.md`.

Preserves exactly: code blocks, inline backticks, URLs, file paths, commands, technical terms, proper nouns, version numbers, environment variables, and all markdown structure (headings, bullets, tables, frontmatter). Compresses only the prose around them.

The skill shells out to a Python CLI (`python3 -m scripts <filepath>`) that calls Claude to compress, validates the output, and cherry-picks targeted fixes if validation fails. Retries up to 2 times. If still failing, the original file is left untouched.

Requires Python 3.10+.

## How to invoke

```
/caveman:compress /absolute/path/to/CLAUDE.md
```

Also triggers on "compress memory file" or similar.

## Example output

Original:
> You should always make sure to run the test suite before pushing any changes to the main branch. This is important because it helps catch bugs early and prevents broken builds from being deployed to production.

Compressed:
> Run tests before push to main. Catch bugs early, prevent broken prod deploys.

Typical input-token savings: ~46% on long memory files.

## Boundaries

- Only compresses prose files: `.md`, `.txt`, `.typ`, `.typst`, `.tex`, extensionless
- Never modifies code files: `.py`, `.js`, `.ts`, `.json`, `.yaml`, `.toml`, `.sh`, etc.
- Skips `*.original.md` files (its own backups)

## See also

- [`SKILL.md`](./SKILL.md) — full compression rules and validation logic
- [Caveman README](../../README.md) — repo overview
