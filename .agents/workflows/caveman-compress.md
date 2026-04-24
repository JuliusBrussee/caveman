---
name: Caveman Compress
description: Compress a file into caveman format to save tokens.
---

# /caveman-compress

Compress the specified file(s) into caveman-speak.

## Steps
1. // turbo
   Run `mkdir -p ~/.claude && echo "compress" > ~/.claude/.caveman-active` to sync statusline.
2. // turbo
   Identify the file to compress.
3. // turbo
   Backup original file to `FILE.original.md`.
4. // turbo
   Compress the content of the file:
   - Remove articles, filler, pleasantries.
   - Preserve all code blocks, URLs, and technical terms EXACTLY.
   - Use short synonyms and fragments.
5. // turbo
   Overwrite the original file with the compressed version.
