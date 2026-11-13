---
name: verify-and-stop
description: Prove existing work meets acceptance conditions without expanding scope. Use for validation-only tasks, completion checks, focused gate runs, and last-mile proof.
---

# Verify and stop

Translate acceptance conditions into smallest sufficient proof set.

- Reuse still-current results with matching repository state.
- Run focused checks before wider gates.
- Distinguish pass, fail, unavailable, and blocked exactly.
- Do not edit product code unless verification request includes fixes.
- Do not add polish, cleanup, or unrelated tests after criteria pass.

While a check is running:

- Keep one active attempt. Resume its returned session until it exits. If the session is lost, inspect the existing process and repository state before retrying; a restricted process view is unavailable evidence.
- Treat quiet output as pending. Use configured deadlines and recent successful durations before diagnosing a stall; silence alone does not justify cancellation, a duplicate run, or changing memory limits.
- Poll at useful intervals, typically 10–30 seconds, and report meaningful progress. Restart only after an observed failure, an exceeded deadline, or evidence that the process cannot progress; confirm the previous attempt has stopped first.

Stop immediately when acceptance proof is complete. Report commands, results, and unresolved risk only.
