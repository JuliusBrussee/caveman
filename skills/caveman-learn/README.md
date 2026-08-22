# caveman-learn skill

Close the loop on `caveman learn`. The command measures where your agent's tokens
go; this skill reviews that plan with you and applies the fixes — one approved edit
at a time.

## Install

    caveman skills install caveman-learn            # this repo's .claude/skills
    caveman skills install caveman-learn --user      # all repos (~/.claude/skills)
    caveman skills install caveman-learn --agent codex

## What it does

1. Runs `caveman learn report --json` and shows your Cave Score + ranked token sinks.
2. For each sink you pick, proposes a fix and asks yes/no:
   - **reducible** (heavy CLAUDE.md, never-invoked skill) → a concrete trim, applied
     only if it measurably lowers tokens/turn.
   - **recurring_context** (context you re-establish every session) → offload it to
     **cavemem** (`cavemem_offload`): stored raw, compacted at recall on demand, with a
     cheap pointer left behind. Applied only when it beats re-pasting, and only after
     a confirming recall proves the content still comes back.
   - **load_bearing** → never touched.

After an approved edit passes its re-measure gate, `caveman learn applied
<sink_id>` records the sink, fix kind, application time, and before-value in
Caveman's own outcome store. Later scans compare post-fix sessions and report
`improved`, `unchanged`, `regressed`, or `insufficient_data`. This ledger does
not edit user or repository config.

## Honesty

Everything is `inferred` — no currency, no "verified". Every edit is consent-gated and
reversible, and an offload that would leave the agent unable to recall the content is
rejected. The analyzer never edits your files; this skill does, only with your yes.
