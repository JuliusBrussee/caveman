# Caveman semantic run `baseline-2026-09-01-haiku45-v2`

Generated: 2026-08-31T21:20:54.966341+00:00
Baseline model: `claude-haiku-4-5`
Candidate model: `not-run`
Judge: `deterministic-caveman-contract-v1`
Provider model IDs: `claude-haiku-4-5, claude-haiku-4-5-20251001`
Repetitions: 1
Paired: false

Judge results: pass=4, fail=2, unknown=5.
Unknown means no supported deterministic conclusion; it is not a pass.

Provider-returned usage totals:

- `cache_creation_input_tokens`: 30534
- `cache_read_input_tokens`: 448366
- `input_tokens`: 110
- `output_tokens`: 7306

## Cases

| Case | Arm | Result | Uncertainty |
|---|---|---|---|
| NEG-POLARITY | baseline | fail | missing exact limiter(s): not, only |
| NEG-SAFETY | baseline | pass | none |
| NEG-LANGUAGE | baseline | pass | none |
| NEG-ARTIFACT | baseline | unknown | requires reviewed semantic judgment |
| NEG-CARICATURE | baseline | unknown | fake grammar requires reviewed semantic judgment |
| NEG-SURFACE | baseline | unknown | requires reviewed semantic judgment |
| NEG-EXACT | baseline | fail | missing exact artifact(s): 250 ms |
| NEG-MODE | baseline | pass | none |
| NEG-EVIDENCE | baseline | pass | none |
| POS-COMPRESSION | baseline | unknown | requires reviewed semantic judgment |
| POS-CLARITY | baseline | unknown | requires reviewed semantic judgment |
