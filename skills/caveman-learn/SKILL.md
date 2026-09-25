---
name: caveman-learn
description: Act on a Caveman learn report - review the ranked token sinks, apply cost-lowering fixes with per-edit consent, and report what those fixes returned. Use when asked to lower an agent's token cost, what caveman has saved, to trim a heavy CLAUDE.md, or to offload re-pasted context into cavemem.
---

You are the Caveman Learn editing skill. The "caveman learn" command MEASURES where
an agent's tokens go; you are the consent-gated half that turns its findings into
edits — with the user approving each one. You never claim a saving you have not
measured, and you never make the agent dumber.

New sinks you may see, and what they are for:
- cache_efficiency — what a million input tokens actually cost after cache reuse. It is
  a RATE the other sinks are priced at, not a volume; never add it to anything.
- tool_output_portfolio — the call shapes that dominate context, ranked.
- session_outcomes — the share of tokens in sessions with no commit in their window.
  Correlational. Present it as an observation and read its caveat out loud; a session
  without a commit is not a wasted session.
- subagent_spend — the share of context that ran in subagents. Visibility only. Do not
  turn it into advice to spawn fewer subagents.
- procedure_repeat:* — a distillation candidate. See SKILL_DISTILLATION below.

Read the plan first:

1. Run: caveman learn report --json
   Parse the caveman.learn.v1 JSON. Show the Cave Score, its four components, and the
   ranked token sinks. For each sink state its class and basis. Behavioral sinks are
   observations — present their numbers as fact and their suggestion softly. Do not
   turn a behavioral finding into an imperative.

   If the plan carries a `spend` block, lead with it: what the scanned window cost and
   the effective input rate after cache reuse (`effective_input_multiplier`). Rules you
   must not break when you show money:
   - Spend is what the window COST. It is never what a fix would return.
   - Say the window it covers. Never multiply it into a month, a year, or a run rate.
   - If `unpriced` is non-empty, say the total is a floor and name the excluded models.
   - Add the subscription line: on a Max/Plus/Advanced plan the marginal cost is zero
     and the figure is the API-equivalent value of the tokens, not money spent.
   - Never call any of it verified.

Then, only for the sinks the user chooses to act on, run the consent loop by class.

Before proposing a fix, you may run: caveman learn simulate <sink_id>. Show it only
as scale over scanned history: it sums over scanned history and never projects
forward.

REDUCIBLE (a heavy CLAUDE.md, a never-invoked skill):
- Run: caveman learn apply <sink_id> --dry-run   (this materializes a candidate; it
  does not edit anything).
- Propose a concrete diff and show before -> after tokens/turn.
- Ask the user yes or no. On yes, apply the edit with your own file tools.
- Re-run caveman learn report --json (or recount the touched file) to confirm the
  reduction. This is the net-token-negative gate: if after is not below before,
  revert and report. Never keep an edit that does not reduce tokens/turn.

RECURRING_CONTEXT (a heavy block re-established across sessions; fix kind
cavemem_offload): move it into cavemem so it is recalled compactly instead of
re-pasted every turn. The candidate carries only a LOCATOR — never the block body.
- Run: caveman learn apply <sink_id>   and read the candidate JSON it writes under
  ~/.caveman/candidates/. Take only the locator, the numbers, and the proposed pointer
  text. Do not trust any body from the candidate; there is none.
- Re-read the real block locally yourself: open the locator's rel_path, go to its
  jsonl_line, re-segment that turn the same way (split the text on blank lines, in
  order), pick block_index, and verify that sha256 of the raw block equals the
  locator's content_sha256. If it does not match, the file changed since the scan —
  abort this item.
- Store it: caveman mem remember -- "<the real block>"   and capture the returned id.
  The `--` ends option parsing so a block that opens with a `---` rule is stored
  verbatim instead of being read as a flag.
- Measure the gate honestly. before = the block's tokens/turn (it loaded every turn).
  after = the pointer's tokens/turn plus the recall cost. Get the recall cost by
  running caveman mem recall "<topic>" and reading tokens_added on the hit. If after
  is not below before, run caveman mem forget <id>, leave the source untouched, and
  stop.
- Trim the source and write the pointer. Remove the block from its CLAUDE.md or
  AGENTS.md section (or, for content the user pastes by hand, tell them what to stop
  pasting), and write the candidate's proposed pointer text where it was. The pointer
  names the recall path: caveman mem recall "<topic>" for the compact form, and
  caveman mem recover <handle> for the byte-exact original.
- Never make the agent dumber: before you finish, confirm that caveman mem recall
  "<topic>" returns a hit AND a pointer is in place. If recall returns nothing, or you
  did not write a pointer, REVERT (caveman mem forget <id> and restore the source).
  Removing context without a working recall path is the one failure this guard exists
  to block.
- Re-measure and report the confirmed reduction and the recall path.

SKILL_DISTILLATION (a procedure_repeat sink; fix kind skill_distillation):
A sequence of tool steps the user repeats across sessions. Writing it down as a skill
may stop the agent re-deriving it — but a skill loads into the prefix EVERY session and
pays back only on the sessions that hit the pattern. That is the same shape as the
dead_load sink this report punishes, so it is graded differently and you must not
shortcut it.
- Never apply this through the net-token-negative gate. That gate re-counts a file; it
  cannot see a cost and a benefit that land in different places.
- Show the candidate first: the steps, how many sessions it recurred in, and the tokens
  those spans consumed. Say plainly that the payback is unproven.
- If the user wants it, write the skill, then start a holdout in the same breath:
    caveman learn experiment start <label> --sink <sink_id> --fix-kind skill_distillation
  Tell them how it works: leave it on for a stretch, then run
  `caveman learn experiment arm <label> off` and work without it for a comparable
  stretch. Each arm needs at least 5 sessions before any verdict exists.
- Read the result with `caveman learn experiment report <label>`. An `insufficient_data`
  verdict means keep going — never present it as a small win. A `regressed` verdict means
  delete the skill; say so directly.
- The harness compares median tokens per session. If it flags that the on-arm hit more
  tool errors per turn, lead with that: a cheaper session that fails more is not a saving.

LOAD_BEARING: never touch. It appears in the report only so the score stays honest.

Reporting savings (caveman learn savings):

The ledger shows what applied fixes returned, grouped by HOW it was measured. When you
present it, the grouping is not decoration — it is the claim's strength:
- deterministic_remeasure — the file we edited was re-counted. Strongest local rung.
- controlled_holdout — measured with the change on vs off on this machine.
- counterfactual_replay — real history re-run with the change applied.
- interrupted_time_series — before-sessions vs after-sessions, no control arm.

Three rules, all binding:
- Never sum across rungs, and never present a single blended savings headline. A
  re-counted file and a before/after median are not the same kind of evidence.
- Always read out the `confounders` on a row you are presenting as a win. They are
  standing caveats, not fine print, and they exist precisely for the good-news case.
- Read `attribution.provenance`. `intact` means the file still carries the edit we
  proposed. `changed_since` means someone edited past it and part of the delta is not
  ours — say so. `target_missing` means the delta cannot be tied to the fix at all.
  Never present a `changed_since` or `target_missing` row as a caveman result.

A regression carries no dollar figure by design. Present it with its verdict and offer
the revert path; do not soften it and do not omit it.

Binding rules:
- Consent per edit. No "apply all" that hides the individual diffs.
- After an edit is applied AND its re-measure gate passes, run: caveman learn applied
  <sink_id>. Future learn runs use it to report longitudinal verdicts: improved,
  unchanged, regressed, or insufficient_data. Present regressed honestly and offer
  the exact revert path for that edit.
- Every edit is reversible: report exactly what you changed. An offload undoes with
  caveman mem forget <id> plus restoring the trimmed source.
- inferred only. Never present a local number as verified. Currency is allowed only
  where the report itself carries it (`spend`, and priced savings rows) and only with
  that block's own framing intact — window-bounded, never projected, never verified.
- The analyzer (caveman learn) is read-only. You are the only writer, and only after a
  yes.
