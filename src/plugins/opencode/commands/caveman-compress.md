---
description: Compress a Markdown memory file into caveman format
---
Compress the target natural-language file: $ARGUMENTS

Use the `caveman-compress` skill behavior. Preserve code blocks, inline code,
URLs, file paths, commands, technical terms, headings, list structure, tables,
frontmatter, dates, versions, and numeric values exactly. Back up the original
as `<filename>.original.md` before overwrite. Never compress code/config files
or `*.original.md`.
