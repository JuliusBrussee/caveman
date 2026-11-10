---
description: Compress a markdown/text file into caveman format to save tokens
---
Compress the file at: $ARGUMENTS

Run the `caveman-compress` skill against the given filepath. The skill rewrites
prose into terse caveman style — drops articles, filler, hedging — while
preserving code blocks, inline code, URLs, file paths, commands, and markdown
structure exactly. Original is backed up as `<file>.original.md` before
overwrite.

Only compress natural-language files (`.md`, `.txt`, `.typ`, `.tex`,
extensionless). Refuse source/config files (`.py`, `.js`, `.ts`, `.json`,
`.yaml`, `.toml`, `.sh`, etc.). Never compress an existing `*.original.md`
backup.

사용자에게는 한국어로 답한다. 기술 내용·코드·명령·경로·에러는 원문 그대로 보존한다.
