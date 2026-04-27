---
name: caveman-compress
description: >
  Alias skill for caveman memory compression. Same behavior and trigger as compress skill.
  Trigger: /caveman:compress <filepath> or "compress memory file"
---

# Caveman Compress Alias

Use same behavior as sibling skill at `../compress/SKILL.md`.

## Process

1. Use scripts in `../compress/scripts/`.
2. Run:

cd ../compress && python3 -m scripts <absolute_filepath>

3. Apply exact compression rules from `../compress/SKILL.md`.
