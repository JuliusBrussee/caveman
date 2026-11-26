# Caveman

Public repository (`JuliusBrussee/caveman`, Apache-2.0) for Caveman: the
response skills and their installer, the `caveman` CLI (`@caveman-ai/cli`), the
local runtime that compresses what an agent reads (Engine, proxy, MCP, memory,
shrink, browse), the SDKs and framework middleware, and the browser extension.
Why it is built this way: [`docs/WHY.md`](docs/WHY.md). How it works:
[`docs/README.md`](docs/README.md).

## Map

| Path | What it is |
|---|---|
| `skills/` | Every skill. Each `skills/*/SKILL.md` ships to every plugin user (see below) |
| `agents/` | The three `cavecrew-*` subagents, plus the agent-profile registry (`profiles/`, `compile.mjs`) behind `caveman wrap` |
| `commands/` | Codex/Gemini `.toml` command stubs and `caveman-init.md` |
| `src/hooks/`, `src/rules/`, `src/tools/`, `src/plugins/opencode/` | Claude Code hooks, the always-on rule body, the per-repo rule writer, the opencode plugin |
| `installer/` | `install.js`, the only installer (`install.sh` / `install.ps1` are shims) |
| `.claude-plugin/`, `.codex-plugin/`, `.cursor-plugin/`, `hooks/hooks-cursor.json`, `GEMINI.md`, `gemini-extension.json` | Plugin and extension manifests (must stay at the root) |
| `plugins/caveman/`, `dist/caveman.skill` | CI-generated mirrors. Never edit by hand |
| `packages/cli/` | The `caveman` CLI |
| `engine/`, `proxy/`, `mcp/`, `mem/`, `shrink/` | Go runtime: compression Engine, provider proxy, MCP server, cavemem, tool-catalog shrink |
| `browse/` | Consumer copy of `JuliusBrussee/caveman-browse`; edit only for pinned integration or an explicit sync |
| `packages/sdk/`, `packages/middleware/`, `packages/shared/contracts/`, `shared/provider-catalog/` | SDKs, framework middleware, JSON schemas and OpenAPI, provider prices |
| `packages/pi-extension/`, `packages/subagent-tax/`, `packages/device-auth/`, `integrations/` | Pi extension, harness-prefix benchmark, device login, framework recipes |
| `extension/` | MV3 browser extension |
| `evals/`, `benchmarks/`, `tests/` | Three-arm skill evals, API benchmarks, repository tests |
| `deploy/`, `Dockerfile` | Shared proxy deployment (Kubernetes, ECS) and its image |
| `docs/` | Technical manual, published benchmark write-ups, release notes |

Folders with their own `AGENTS.md`: `engine/`, `proxy/`, `mcp/`, `mem/`,
`shrink/`, `browse/`, `extension/`, `integrations/`, `packages/cli/`,
`packages/sdk/`, `packages/shared/contracts/`, `shared/provider-catalog/`.
Read the one for the folder you touch.

## Non-negotiables

- **Public repo.** No private business details, customer names, pricing
  strategy, internal infrastructure, local machine paths, or the internals of
  private repositories in any file, commit message, or PR.
- **Numbers are real.** Every published number comes from a committed run in
  `evals/`, `benchmarks/`, or a write-up in `docs/` with its method. Never
  invent, round up, or estimate one. Losing rows stay visible. No reduction or
  quality-equivalence percentage without a committed, reviewed result.
- **Label the basis.** Local counts are `inferred`; only a named, enforced
  method may say `verified`
  ([accounting](docs/technical/accounting-and-evidence.md)).
- **Fail toward the original.** A transform that cannot parse, is not smaller,
  or cannot store its recovery passes the input through unchanged. A lossy
  result is emitted only after its original is stored.
- **Code, commands, paths, and error text stay verbatim**, in the voice and in
  every compressor.
- **The user's prompt is never rewritten.**
- **Fixes happen only on the user's yes.** `caveman learn` measures and
  proposes; the skill applies one edit at a time after consent.
- **Telemetry never carries prompts, code, file paths, or scripts.**
  `caveman telemetry off` and `DO_NOT_TRACK=1` turn it off. Keep `SECURITY.md`
  in step with what is sent.
- **Hooks never block a session.** They silent-fail on filesystem errors and
  read stdin to the first complete JSON object, never to EOF.

## Rules that bite

- `skills/*/SKILL.md` is auto-discovered (the plugin root is the repo root). A
  new skill directory installs into every user's agent; name it in a code span
  in `README.md` or here, or `tests/verify_repo.py` fails. Shipped skills:
  `caveman`, `ultracave`, `megacave`, `caveman-commit`, `caveman-review`,
  `caveman-help`, `caveman-stats`, `caveman-compress`, `cavecrew`,
  `caveman-setup`, `caveman-discover`, `caveman-learn`, `caveman-manage`,
  `caveman-optimize`, `caveman-explore`, `caveman-evidence-review`,
  `investigate-first`, `lean-build`, `surgical-patch`, `safe-refactor`,
  `migration`, `verify-and-stop`.
- Every `.md` under `agents/` becomes a subagent; only the three cavecrew files
  belong there. `plugin.json` must not list agents. `commands/` keeps one `.md`.
- Edit sources, never mirrors: `skills/<name>/SKILL.md`, not
  `plugins/caveman/`; `skills/caveman/SKILL.md` and `skills/activation-rule.mjs`,
  not the generated `src/rules/caveman-activate.md`. After editing skills,
  agent profiles, or recipes run `node packages/cli/scripts/compile-registries.mjs`
  and commit the generated files with their source.
- Any edit under `src/hooks/` means regenerating `src/hooks/checksums.sha256`.
- Mode state goes through the `caveman-config.js` helpers, flag writes through
  `safeWriteFlag()`, session ids through `validateSessionId()`, and
  `settings.json` through `readSettings()` / `validateHookFields()`. Honor
  `CLAUDE_CONFIG_DIR`.
- New agent install targets go in the `PROVIDERS` array in
  `installer/install.js`; the profile slug must exist in vercel-labs/skills.
- Details and reasons: [skills, hooks, and plugins](docs/technical/skills-hooks-and-plugins.md).

## README

`README.md` is the product's front page; non-programmers read it to decide
whether to install. Keep the before/after example first, every install command
working, and the commands table in sync with the code. The caveman voice there
is deliberate brand ("Brain still big.", "One rock. That it."); don't normalize
it. Keep the section anchors (`#install`, `#commands`, the rock sections) and
the badges. `INSTALL.md` holds the full per-agent matrix; tests read both files.

## Commands

```bash
npm test                                          # installer and hook tests
node --test --test-force-exit tests/*.js          # standalone hook and tool tests
python -m unittest discover -s tests              # Python tests
python tests/verify_repo.py                       # repository invariants
go test ./engine/... ./proxy/... ./shared/...     # Go runtime (engine-ci runs go test ./...)
pnpm install --frozen-lockfile && pnpm -r build   # TypeScript workspace
pnpm --filter @caveman-ai/cli run test:fast       # CLI
node .github/scripts/check-doc-links.mjs <files>  # documentation links
```

Run the narrowest suite for what you changed first, then `verify_repo.py`.
`npm run test:windows` covers Windows path handling.

## Commits and pull requests

- Work in a clean branch or worktree off `origin/main`; never commit or push
  directly to `main`.
- Scoped conventional commits (`fix(proxy): ...`, `docs(readme): ...`), signed
  off for the DCO (`git commit -s`). Stage files explicitly (`git commit --only
  -- <paths>`), never `git add -A`.
- Run `python3 tests/verify_repo.py` and touched suites before pushing, then
  open a PR targeting `main` and squash-merge on green CI.
- Generated files land in the same commit as their source.
- `sync-skill.yml` pushes a `[skip ci]` mirror commit to `main` after a skill
  change merges; pull before checking branch state.
- Releases: [`docs/PACKAGE_RELEASES.md`](docs/PACKAGE_RELEASES.md). Product
  release notes: `docs/releases/<version>.md`.

## Pointers

- Contributing: [`CONTRIBUTING.md`](CONTRIBUTING.md), agent profiles
  [`docs/CONTRIBUTING_PROFILES.md`](docs/CONTRIBUTING_PROFILES.md)
- Security and telemetry: [`SECURITY.md`](SECURITY.md)
- Licensing: [`LICENSING.md`](LICENSING.md)
- Testing and benchmark rules: [`docs/technical/testing-and-benchmarks.md`](docs/technical/testing-and-benchmarks.md)
- Public Caveman ecosystem repositories:
  - [`caveman-ai/caveman-code`](https://github.com/caveman-ai/caveman-code) — Desktop, CLI (`@caveman-ai/code`), Web, and Mobile coding agent
  - [`caveman-ai/agent-sdk`](https://github.com/caveman-ai/agent-sdk) — Agent SDK, Caveman Harness, Session Protocol, and Evals
  - [`caveman-ai/cloud-sdk`](https://github.com/caveman-ai/cloud-sdk) — Caveman Cloud CLI (`cvm`), TypeScript/Python/Go SDKs, and Cloud skills
  - [`caveman-ai/caveman-docs`](https://github.com/caveman-ai/caveman-docs) — Public documentation site (`docs.caveman.so`)
  - [`caveman-ai/blocks`](https://github.com/caveman-ai/blocks), [`caveman-ai/trim-1`](https://github.com/caveman-ai/trim-1), [`JuliusBrussee/caveman-browse`](https://github.com/JuliusBrussee/caveman-browse) — Standalone reusable blocks, line-trimming model, and browser MCP driver
