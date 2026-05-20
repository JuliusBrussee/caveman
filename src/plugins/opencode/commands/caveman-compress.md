---
description: Compress a memory/context file into terse caveman style while preserving code, URLs, and paths.
---

# caveman-compress

Compress a file using the `caveman-compress` skill.

Usage:
- `/caveman-compress <file>`

Steps:
1. Load `caveman-compress` skill.
2. Run its validator/detector first if available.
3. Rewrite prose terse, keep technical meaning.
4. Preserve code blocks, URLs, paths, commands, JSON/YAML/TOML, and frontmatter byte-for-byte unless the skill says otherwise.
5. Report original tokens, compressed tokens, and percent saved.

If no file path supplied, ask for one concise question.
