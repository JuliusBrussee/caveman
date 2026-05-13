# Security

## Snyk High Risk Rating

`caveman-compress` receives a Snyk High Risk rating due to static analysis heuristics. This document explains what the skill does and does not do.

### What triggers the rating

1. **subprocess usage**: The skill calls the `claude` CLI via `subprocess.run()` to send a single non-interactive `claude -p <prompt>` request. The call uses a fixed argument list with `shell=False` — no shell interpolation occurs. The prompt is passed as a positional argument, not interpolated into a command string.

2. **File read/write**: The skill reads the file the user explicitly points it at, compresses it, and writes the result back to the same path. A `.original.md` backup is saved alongside it. No files outside the user-specified path are read or written.

### What the skill does NOT do

- Does not execute user file content as code
- Does not make network requests except those issued by the `claude` CLI itself
- Does not access files outside the path the user provides
- Does not use shell=True or string interpolation in subprocess calls
- Does not collect or transmit any data beyond the file being compressed
- Does not interpret slash commands embedded in the file (`--disable-slash-commands` is always passed)

### Auth behavior

All requests route through the local `claude -p` CLI using whatever Claude Code login the user already has. The skill never reads `ANTHROPIC_API_KEY` directly. By default (`CAVEMAN_AUTH_MODE=cli-login`), it strips `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, and the Bedrock/Vertex base URLs from the subprocess environment so that a stray credential in the parent shell cannot redirect the request. Set `CAVEMAN_AUTH_MODE=inherit` to keep those env vars in place if you want the CLI itself to honor them.

Nested `CLAUDE_CODE_*` env vars are also stripped (except the OAuth and provider-toggle ones) so a recursive invocation cannot inherit the parent's session-scoped state.

### File size limit

Files larger than 500KB are rejected before any API call is made.

### Reporting a vulnerability

If you believe you've found a genuine security issue, please open a GitHub issue with the label `security`.
