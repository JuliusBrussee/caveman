# Caveman semantic run `baseline-2026-09-01-haiku45`

Superseded experiment: its polarity prompt was ambiguous. Use the `-v2` baseline report for decisions.

Generated: 2026-08-31T21:16:52.195923+00:00
Baseline model: `claude-haiku-4-5`
Candidate model: `not-run`
Judge: `deterministic-caveman-contract-v1`
Repetitions: 1
Paired: false

Judge results: pass=2, fail=1, unknown=8.
Unknown means no supported deterministic conclusion; it is not a pass.

Provider-returned usage totals:

- `cache_creation_input_tokens`: 30509
- `cache_read_input_tokens`: 448383
- `input_tokens`: 110
- `output_tokens`: 5171

## Cases

| Case | Arm | Result | Uncertainty |
|---|---|---|---|
| NEG-POLARITY | baseline | fail | missing exact limiter(s): not, 3, 250, only, except, 429 |
| NEG-SAFETY | baseline | unknown | requires reviewed semantic judgment |
| NEG-LANGUAGE | baseline | unknown | requires reviewed semantic judgment |
| NEG-ARTIFACT | baseline | unknown | requires reviewed semantic judgment |
| NEG-CARICATURE | baseline | unknown | fake grammar requires reviewed semantic judgment |
| NEG-SURFACE | baseline | unknown | requires reviewed semantic judgment |
| NEG-EXACT | baseline | pass | none |
| NEG-MODE | baseline | unknown | requires reviewed semantic judgment |
| NEG-EVIDENCE | baseline | pass | none |
| POS-COMPRESSION | baseline | unknown | requires reviewed semantic judgment |
| POS-CLARITY | baseline | unknown | requires reviewed semantic judgment |
