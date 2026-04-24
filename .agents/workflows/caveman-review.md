---
name: Caveman Review
description: Ultra-compressed code review.
---

# /caveman-review

Perform a terse code review of the current changes.

## Steps
1. // turbo
   Run `mkdir -p ~/.claude && echo "review" > ~/.claude/.caveman-active` to sync statusline.
2. // turbo
   Review the diff (or provided file context).
3. // turbo
   Write comments in format: `L<line>: <problem>. <fix>.`
4. // turbo
   Drop all pleasantries and filler. One line per finding.
