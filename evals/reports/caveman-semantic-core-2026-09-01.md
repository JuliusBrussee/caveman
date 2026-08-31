# Caveman semantic run `semantic-core-2026-09-01-haiku45`

Superseded experiment: this report covers the 4,322-byte intermediate candidate. Use the `-v2` report for the shipped 4,494-byte candidate.

Generated: 2026-08-31T21:52:56.058628+00:00
Baseline model: `claude-haiku-4-5`
Candidate model: `claude-haiku-4-5`
Judge: `deterministic-caveman-contract-v1`
Provider model IDs: `claude-haiku-4-5, claude-haiku-4-5-20251001`
Repetitions: 1
Paired: true

Judge results: pass=6, fail=4, unknown=12.
Unknown means no supported deterministic conclusion; it is not a pass.

Provider-returned usage totals:

- `cache_creation_input_tokens`: 366
- `cache_read_input_tokens`: 994715
- `input_tokens`: 228
- `output_tokens`: 12586

## Cases

| Case | Arm | Result | Uncertainty |
|---|---|---|---|
| NEG-POLARITY | baseline | fail | missing exact limiter(s): not, only, except |
| NEG-POLARITY | candidate | fail | missing exact limiter(s): not, only, except |
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
| NEG-EXACT | baseline | pass | none |
| NEG-EXACT | candidate | fail | missing exact artifact(s): 250 ms, "ECONNRESET" |
| NEG-MODE | baseline | unknown | quoted phrase handling needs review |
| NEG-MODE | candidate | unknown | quoted phrase handling needs review |
| NEG-EVIDENCE | baseline | fail | malformed evidence treated as a pass |
| NEG-EVIDENCE | candidate | pass | none |
| POS-COMPRESSION | baseline | unknown | requires reviewed semantic judgment |
| POS-COMPRESSION | candidate | unknown | requires reviewed semantic judgment |
| POS-CLARITY | baseline | unknown | requires reviewed semantic judgment |
| POS-CLARITY | candidate | unknown | requires reviewed semantic judgment |
