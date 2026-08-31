# Caveman semantic run `semantic-core-2026-09-01-haiku45-v2`

Generated: 2026-08-31T21:57:57.945695+00:00
Baseline model: `claude-haiku-4-5`
Candidate model: `claude-haiku-4-5`
Judge: `deterministic-caveman-contract-v1`
Provider model IDs: `claude-haiku-4-5, claude-haiku-4-5-20251001`
Repetitions: 1
Paired: true

Judge results: pass=8, fail=3, unknown=11.
Unknown means no supported deterministic conclusion; it is not a pass.

Provider-returned usage totals:

- `cache_creation_input_tokens`: 336878
- `cache_read_input_tokens`: 615301
- `input_tokens`: 220
- `output_tokens`: 11175

## Cases

| Case | Arm | Result | Uncertainty |
|---|---|---|---|
| NEG-POLARITY | baseline | fail | missing exact limiter(s): not, only, except |
| NEG-POLARITY | candidate | fail | missing exact limiter(s): not, only |
| NEG-SAFETY | baseline | pass | none |
| NEG-SAFETY | candidate | pass | none |
| NEG-LANGUAGE | baseline | pass | none |
| NEG-LANGUAGE | candidate | pass | none |
| NEG-ARTIFACT | baseline | unknown | requires reviewed semantic judgment |
| NEG-ARTIFACT | candidate | unknown | requires reviewed semantic judgment |
| NEG-CARICATURE | baseline | unknown | fake grammar requires reviewed semantic judgment |
| NEG-CARICATURE | candidate | unknown | fake grammar requires reviewed semantic judgment |
| NEG-SURFACE | baseline | unknown | requires reviewed semantic judgment |
| NEG-SURFACE | candidate | unknown | requires reviewed semantic judgment |
| NEG-EXACT | baseline | fail | missing exact artifact(s): 250 ms, "ECONNRESET" |
| NEG-EXACT | candidate | pass | none |
| NEG-MODE | baseline | pass | none |
| NEG-MODE | candidate | unknown | quoted phrase handling needs review |
| NEG-EVIDENCE | baseline | pass | none |
| NEG-EVIDENCE | candidate | pass | none |
| POS-COMPRESSION | baseline | unknown | requires reviewed semantic judgment |
| POS-COMPRESSION | candidate | unknown | requires reviewed semantic judgment |
| POS-CLARITY | baseline | unknown | requires reviewed semantic judgment |
| POS-CLARITY | candidate | unknown | requires reviewed semantic judgment |
