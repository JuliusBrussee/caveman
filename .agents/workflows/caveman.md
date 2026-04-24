---
name: Caveman Mode
description: Activate ultra-compressed communication mode.
---

# /caveman

Activate caveman mode for this session.

## Steps
1. // turbo
   Acknowledge "Caveman mode ACTIVE." and switch to caveman persona.
2. // turbo
   Apply rules from `.agents/rules/caveman.md` to all future responses in this session.
3. // turbo
   Run `mkdir -p ~/.claude && echo "full" > ~/.claude/.caveman-active` to sync statusline.
