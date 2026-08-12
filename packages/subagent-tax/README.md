# subagent-tax

**How big is the prefix your coding harness sends on every call?**

Every subagent your agent spawns re-sends its harness's full prefix — system
prompt plus every tool schema — before doing any work. This tool captures that
prefix, per harness, on *your* machine: a local sink impersonates the provider
endpoint, each installed harness sends it one real request, and the tool reports
what was in it. **No provider API calls, no account, nothing leaves your
machine** (the one exception, `--count-tokens`, is opt-in and says so).

```
node run.mjs
```

Example output from one real machine (2026-08-07 — yours will differ; that's
the point). Note the `variant` column: it says what each row measures, and rows
with different variants are different constructs.

```
harness       status        wire                    tools  mcp  system  schemas  body  input tokens  variant
------------  ------------  ----------------------  -----  ---  ------  -------  ----  ------------  -------------------------------
claude        ok            anthropic-messages      91     63   42k     219k     267k  ~43k (est)    real config
opencode      ok            openai-responses        10     -    68k     20k      87k   ~14k (est)    isolated config
codex         ok            openai-responses        11     -    40k     10k      52k   ~8.3k (est)   minimal home (floor)
gemini        ok            gemini-generatecontent  8      -    30k     8.3k     39k   ~6.3k (est)   isolated home (api-key mode)
pi            ok            anthropic-messages      4      -    23k     2.8k     26k   ~4.1k (est)   isolated home (4 default tools)
cursor-agent  unmeasurable  -                       -      -    -       -        -     -             -
```

What that machine's claude row actually says: **219k of its 267k-char request
body is tool schemas, and 63 of its 91 tools come from MCP servers/plugins** —
about 69% of the schema weight, roughly 24k estimated tokens per call, before
the agent does anything. A single `Workflow` schema is 20.8k chars; one Notion
MCP tool is 17.1k.

What it does **not** say: that claude is "10x pi". That row is one person's
real installed setup with 63 MCP tools; the pi row is a floor with 4 built-in
tools. Comparing them measures a plugin loadout, not a harness. Run
`--isolate` if you want floor-vs-floor (note: claude cannot be measured that
way — its login lives in the config dir, so an isolated run exits "Not logged
in").

## Honest by construction

- **Basis: inferred, always.** Prefix sizes, not bills, not spend, not savings.
  With a warm provider cache this prefix re-reads at a discount (Anthropic
  ~0.1x, OpenAI/Gemini higher) — the honesty block on every run says so, and no
  savings figure appears anywhere in this tool.
- Token counts are labeled `est` (calibrated chars/token, rounded to 2
  significant figures because the calibration band is ±8%) or `exact`
  (Anthropic `count_tokens`).
- `-` in the mcp column means **unknown**, not zero: only Claude Code's MCP
  naming convention has been confirmed.
- Every run writes a repro pack: raw captures, logs, configs, `report.json`,
  `manifest.sha256`. Auth headers and known account/session identifiers are
  redacted at write time, and bodies are scrubbed of emails and
  credential-shaped strings — but bodies are still your harness's real system
  prompt. Review before sharing.
- No recipe modifies your config files. The **claude** row deliberately runs
  against your real config (your plugins are the tax being measured), which
  boots your MCP servers, runs your hooks, and leaves a session transcript in
  `~/.claude/projects`. The tool says so before it starts; `--isolate` opts out.

## Flags

```
--harness a,b,c   pick harnesses (default: all installed)
--out DIR         repro pack location (default ./subagent-tax-report)
--repeat N        run each harness N times; median row + observed min–max spread
--isolate         measure harness floors instead of your real config
--count-tokens    provider-exact tokens for anthropic rows. Sends the captured
                  body — your real system prompt — to api.anthropic.com.
                  Needs ANTHROPIC_API_KEY; opt-in, never automatic
--ratio N         chars-per-token for estimates (default 6.4, calibrated)
--json            machine-readable report to stdout
--list            registry + detection status
--timeout/--grace per-harness capture timing
```

Method, labeling rules, per-harness variants, and limitations: [METHOD.md](METHOD.md).

Part of [Caveman](https://caveman.so) — measurement first, claims never ahead
of evidence.
