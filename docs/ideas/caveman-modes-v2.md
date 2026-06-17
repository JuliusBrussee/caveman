# Caveman Modes v2

## Problem Statement
How might we make Caveman mode behavior predictable and measurable for maintainers and subscribers, while removing legacy Chinese-mode residue?

## Recommended Direction
Adopt B-C-D-G as one product track:

- B (strict mode contract): define exact behavior boundaries for `ultra`, `super-compress`, `silence`.
- C (silence by need): in silence, output only blocker/question/safety/final result.
- D (symbol-first super-compress): allow concise prose symbols (`+`, `->`, `=`, `&`) only when unambiguous.
- G (internal validation): enforce comparable outputs and measure by mode.

This keeps implementation simple, aligned with current architecture, and testable.

## Key Assumptions to Validate
- [ ] Symbol-first compression improves brevity without harming comprehension.
- [ ] Silence mode with mandatory final summary is useful, not frustrating.
- [x] Removing legacy Chinese references eliminates product ambiguity.

## MVP Scope
In:
- Contract update for `super-compress` and `silence` in skill docs.
- Remove remaining legacy non-target language mode references from behavior-facing tests/docs.
- Internal validation rule for silence final summary.
- Minimal test updates for parser behavior and fallback.

Out:
- New modes beyond current five levels.
- Large docs redesign unrelated to mode behavior.
- Broad architecture refactor.

## Not Doing (and Why)
- Adding more mode presets now: would increase complexity before validating current behavior.
- Reworking installer architecture: unnecessary for this objective.
- Publishing external benchmark claims before internal validation is stable.

## Open Questions
- Should final summary in silence always use a fixed schema tag format?
- Should super-compress symbol use be capped by readability heuristics?
- When internal validation passes, which rules should be relaxed for public usage?

## Internal Validation Rule (Confirmed)
For `silence` mode during internal validation:

- Final summary is mandatory on 100% completed tasks.
- Summary length is 1-3 lines.
- Summary content is: status, result, next action.
- If blocked, ask the blocking question first; once resolved, final summary becomes mandatory again.
