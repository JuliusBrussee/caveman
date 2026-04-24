---
name: Caveman Commit
description: Generate an ultra-compressed commit message.
---

# /caveman-commit

Generate a terse, high-SNR commit message based on staged changes.

## Steps
1. // turbo
   Run `mkdir -p ~/.claude && echo "commit" > ~/.claude/.caveman-active` to sync statusline.
2. // turbo
   Run `git diff --cached` to see staged changes.
3. // turbo
   Write commit message in Conventional Commits format:
   - `<type>(<scope>): <imperative summary>`
   - Subject ≤ 50 chars.
   - No fluff, no "I", no "This commit...".
   - Include body only for "why" or breaking changes.
4. // turbo
   Output the message as a code block ready to paste.
