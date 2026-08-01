# Security

## Snyk High Risk Rating

`caveman-compress` receives a Snyk High Risk rating due to static analysis heuristics. This document explains what the skill does and does not do.

### What triggers the rating

1. **subprocess usage**: The skill calls the `claude` CLI via `subprocess.run()` as a fallback when `ANTHROPIC_API_KEY` is not set. It can also call `opencode run` when `CAVEMAN_COMPRESS_PROVIDER=opencode`. Subprocess calls use fixed argument lists — no shell interpolation occurs. Claude receives user file content via stdin; opencode receives the generated prompt through a temporary `--file` attachment that is deleted after the call.

2. **File read/write**: The skill reads the file the user explicitly points it at, compresses it, and writes the result back to the same path. A `.original.md` backup is saved to an out-of-tree data dir (`$XDG_DATA_HOME/caveman-compress/backups/<parent-dir-name>/`, or `%LOCALAPPDATA%\caveman-compress\backups\<parent-dir-name>\` on Windows). The opencode provider writes a temporary prompt attachment in the OS temp directory and deletes it after the subprocess exits. Beyond the target file, that backup location, and the temporary attachment, no files are read or written.

### What the skill does NOT do

- Does not execute user file content as code
- Does not make network requests except through the configured LLM provider CLI or SDK
- Does not read unrelated files outside the path the user provides
- Does not use shell=True or string interpolation in subprocess calls
- Does not collect or transmit any data beyond the file being compressed

### Auth behavior

Default path uses Claude. If `ANTHROPIC_API_KEY` is set, the skill uses the Anthropic Python SDK directly (no subprocess). If not set, it falls back to the `claude` CLI, which uses the user's existing Claude desktop authentication.

Set `CAVEMAN_COMPRESS_PROVIDER=opencode` to use `opencode run` instead. Set `CAVEMAN_COMPRESS_MODEL=provider/model` for compress-specific model selection. `CAVEMAN_PROVIDER` and `CAVEMAN_MODEL` are fallback env vars when compress-specific values are unset.

### File size limit

Files larger than 500KB are rejected before any API call is made.

### Reporting a vulnerability

If you believe you've found a genuine security issue, please open a GitHub issue with the label `security`.
