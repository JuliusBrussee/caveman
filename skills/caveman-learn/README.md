# caveman-learn skill

Close the loop on `caveman learn`. The command finds where your agent's tokens
go; this skill walks through the findings with you and applies the fixes, one
approved edit at a time.

## Install

    caveman skills install caveman-learn            # this repo's .claude/skills
    caveman skills install caveman-learn --user      # all repos (~/.claude/skills)
    caveman skills install caveman-learn --agent codex

## What it does

1. Runs `caveman learn report --json` and shows your Setup Score and the
   findings, biggest first.
2. For each finding you pick, it proposes a fix and asks yes or no:
   - **Safe fix** (a heavy CLAUDE.md, a skill you never use): a concrete trim.
     It is kept only if it measurably makes every message smaller.
   - **Repeated text** (text you paste again in session after session): move it
     to **Caveman memory** (cavemem). The full text is stored once, the agent
     recalls a short version when it needs it, and a small pointer stays
     behind. Kept only when it beats pasting, and only after a test recall
     proves the text still comes back.
   - **Needed** (setup your agent relies on): never touched.

After an approved edit passes its re-check, `caveman learn applied <id>`
records which finding was fixed, how, when, and its size before. Later scans
compare the sessions after the fix and report `improved`, `unchanged`,
`regressed`, or `insufficient_data` (not enough sessions yet). This record
does not edit your own or your repository's files.

## Honesty

Everything is an estimate (`inferred`): no currency, never "verified". Every
edit waits for your yes and can be undone. A move to memory that would leave
the agent unable to recall the text is rejected. `caveman learn` itself never
edits your files; this skill does, and only with your yes.
