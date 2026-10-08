# Security

## Snyk High Risk Rating

`caveman-compress` receives a Snyk High Risk rating due to static analysis heuristics. This document explains what the skill does and does not do.

### What triggers the rating

1. **subprocess usage**: The skill calls the `claude` CLI via `subprocess.run()` as a fallback when `ANTHROPIC_API_KEY` is not set. It can also call `opencode run` when `CAVEMAN_COMPRESS_PROVIDER=opencode`. Subprocess calls use fixed argument lists — no shell interpolation occurs. Claude receives user file content via stdin; opencode receives the generated prompt through a temporary `--file` attachment that is deleted after the call.

   The file being compressed is untrusted input, and opencode 2.x lets its agents use every tool that is not explicitly denied: shell, file edits, web search and fetch, MCP tools from your opencode config, subagents. Compression needs none of them. Compress therefore runs `opencode run --standalone --agent caveman-compress` with `OPENCODE_CONFIG_CONTENT` denying every permission (`"*": "deny"`) both globally and for that agent, and never passes `--auto`. The dedicated agent matters because opencode applies a per-agent rule after the global one, so an `agent.build.permission` allow in your own config would otherwise re-enable a tool. `--standalone` matters because the shared opencode background service does not read the caller's environment, so the deny rule only binds a private server. Checked on opencode 2.0.22: the model is offered no tools and the prompt attachment is still read. Unlike the Claude path (`--strict-mcp-config`), compress does not switch off the MCP servers in your opencode config: their tools are denied, but opencode may still start them.

2. **File read/write**: The skill reads the file the user explicitly points it at, compresses it, and writes the result back to the same path. A `.original.md` backup is saved to an out-of-tree data dir (`$XDG_DATA_HOME/caveman-compress/backups/<parent-dir-name>/`, or `%LOCALAPPDATA%\caveman-compress\backups\<parent-dir-name>\` on Windows). The opencode provider writes a temporary prompt attachment in the OS temp directory and deletes it after the subprocess exits. Beyond the target file, that backup location, and the temporary attachment, no files are read or written.

### What the skill does NOT do

- Does not execute user file content as code
- Does not make network requests except through the configured LLM provider CLI, SDK, or (for `openai-compat`) the one configured HTTP endpoint
- Does not read unrelated files outside the path the user provides
- Does not use shell=True or string interpolation in subprocess calls
- Does not collect or transmit any data beyond the file being compressed

### Auth behavior

Default path uses Claude. If `ANTHROPIC_API_KEY` is set, the skill uses the Anthropic Python SDK directly (no subprocess). If not set, it falls back to the `claude` CLI, which uses the user's existing Claude desktop authentication.

Set `CAVEMAN_COMPRESS_PROVIDER=opencode` to use `opencode run` instead. Set `CAVEMAN_COMPRESS_MODEL=provider/model` for compress-specific model selection. `CAVEMAN_MODEL` is the fallback when `CAVEMAN_COMPRESS_MODEL` is unset.

Set `CAVEMAN_COMPRESS_PROVIDER=openai-compat` to POST the prompt with Python's standard-library `urllib` (no subprocess, no extra dependency) to `$CAVEMAN_COMPRESS_ENDPOINT/chat/completions`, default `http://localhost:11434/v1` (Ollama). Other local servers: LM Studio `http://localhost:1234/v1`, llama.cpp `http://localhost:8080/v1`, vLLM `http://localhost:8000/v1`. `CAVEMAN_COMPRESS_API_KEY`, if set, is sent as a Bearer token. A non-local `http://` endpoint sends the file and that key in plaintext; use `https://` for anything off the machine.

### File size limit

Files larger than 500KB are rejected before any API call is made.

### Snyk W007 ("insecure credential handling")

W007 fires on the rule that code blocks, commands, and exact error strings stay verbatim. See [Scanner warnings](../../SECURITY.md#scanner-warnings) in the repository SECURITY.md.

### Reporting a vulnerability

If you believe you've found a genuine security issue, please open a GitHub issue with the label `security`.
