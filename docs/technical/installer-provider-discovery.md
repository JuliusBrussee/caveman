# Installer provider discovery

Checked 2026-09-08. This records where each skill host discovers instructions,
how the installer reaches that location, and what was actually verified.
Discovery is distinct from automatic invocation, model compliance, cloud sync,
and token savings. No authenticated model conversation was used for this review.

## Effective upstream behavior

The reviewed upstream is the integrity-verified `skills@1.5.24` package and
[source revision 1682051](https://github.com/vercel-labs/skills/tree/1682051d48c34f5eb135e6475c1a965dce05e820).
The live delegated dependency is unpinned, so this is a versioned observation.

Its [installation algorithm](https://github.com/vercel-labs/skills/blob/1682051d48c34f5eb135e6475c1a965dce05e820/src/installer.ts)
first copies into `~/.agents/skills`. Profiles with project `skillsDir` equal to
`.agents/skills` are universal: global installation stops there, without creating
the profile's declared `globalSkillsDir`. Other profiles get a per-skill symlink
at that declared path, with a copy fallback. `--copy` instead materializes the
profile destination. Reading `globalSkillsDir` alone therefore gives the wrong
answer for Codex, Cursor, Cline, Copilot, Amp, Warp, Replit and Antigravity.

Caveman uses owned physical copies where the upstream behavior misses a vendor
loader or configured directory. Journals and forced-install backups remain
beside the skills directory, outside recursive skill discovery. Uninstall removes
unchanged owned files and preserves unrelated or subsequently edited content.
Existing unowned skill directories and symlinks are not silently adopted.

## IDE and coding profiles

`~` means the host user's home, including `%USERPROFILE%` on Windows. A matching
directory below is source or documentation evidence; it is not a full host run.

| Installer ID | Vendor discovery and current installation | Consumption and evidence |
| --- | --- | --- |
| `codex` | Shared `~/.agents/skills`; `$CODEX_HOME/skills` remains compatible. Universal install reaches shared root. | Recursive metadata scan follows user/repository symlinks; body loads on use. [Roots](https://github.com/openai/codex/blob/8e694e955ae02ca737230a5468c55d5847074072/codex-rs/ext/skills/src/host_roots.rs), [loader](https://github.com/openai/codex/blob/8e694e955ae02ca737230a5468c55d5847074072/codex-rs/ext/skills/src/loader/host.rs). An isolated app-server listing timed out; no runtime success claimed. |
| `cursor` | Docs include `~/.agents/skills` and `~/.cursor/skills`; universal install reaches shared root. | Metadata first, body on selection/invocation. [Official docs](https://prod.cursor.com/docs/skills). Closed source. Global cloud sync has separate `.cursor` requirements. |
| `windsurf` | Cascade reads `~/.codeium/windsurf/skills`; upstream links there. | Metadata then body on selection/mention. [Official Cascade docs](https://docs.devin.ai/desktop/cascade/skills). Desktop scope, distinct from Devin CLI/cloud. |
| `cline` | Source reads `~/.cline/skills` and `~/.agents/skills`; universal install reaches shared root. | `fs.stat` follows skill symlinks; name/description and directory-name match required; `getSkillContent` reads body. [Directories](https://github.com/cline/cline/blob/c21b17255b228e88a1518c18a73a473ee5876362/apps/vscode/src/core/storage/skill-directories.ts), [loader](https://github.com/cline/cline/blob/c21b17255b228e88a1518c18a73a473ee5876362/apps/vscode/src/core/context/instructions/user-instructions/skills.ts). |
| `continue` | Owned copies into `$CONTINUE_GLOBAL_DIR/skills`, default `~/.continue/skills`; relative override is cwd-relative. | Actual IDE walker and CLI loader skip per-skill symlink directories. Extracted vendor functions found zero symlinked skills and one physical-copy control. [Paths](https://github.com/continuedev/continue/blob/5522c6f44ca0ac3528b37244818fbfa39b5af470/core/util/paths.ts), [walker](https://github.com/continuedev/continue/blob/5522c6f44ca0ac3528b37244818fbfa39b5af470/core/indexing/walkDir.ts), [CLI loader](https://github.com/continuedev/continue/blob/5522c6f44ca0ac3528b37244818fbfa39b5af470/extensions/cli/src/util/loadMarkdownSkills.ts). |
| `kilo` | Current source accepts `.kilo`, legacy `.kilocode`, shared `.agents`, and config roots; upstream legacy link remains supported. | Recursive symlink-aware metadata/body loader; compatibility discovery can be disabled. [Paths](https://github.com/Kilo-Org/kilocode/blob/7bcd136950db11e572164c8e3baffe9b7c260c43/packages/opencode/src/config/paths.ts), [loader](https://github.com/Kilo-Org/kilocode/blob/7bcd136950db11e572164c8e3baffe9b7c260c43/packages/opencode/src/skill/index.ts). No reproduced legacy-path defect. |
| `roo` | Global `.roo/skills` and `.agents/skills`; upstream `.roo` link matches. | `fs.stat`/realpath follow symlinks; metadata first, file contents on use; project and mode-specific overrides. [SkillsManager](https://github.com/RooCodeInc/Roo-Code/blob/b867ec9145750d0ae1ff7f02d35406e9bf2a0b16/src/services/skills/SkillsManager.ts). |
| `augment` | Global `.augment/skills`, compatible `.claude`/`.agents`, and project roots; upstream link matches docs. | Auto/Manual/Disabled modes. Skills beta requires VS Code 0.789.0+ or JetBrains 0.428.8+ and enabled rollout. [VS Code](https://docs.augmentcode.com/using-augment/skills), [JetBrains](https://docs.augmentcode.com/jetbrains/using-augment/skills), [CLI](https://docs.augmentcode.com/cli/skills). Closed source; folder existence cannot prove feature availability. |
| `copilot` | VS Code reads global `.agents`, `.copilot`, `.claude`; universal install reaches shared root. | Metadata/body/resources are progressive; slash or model selection. [Locations](https://github.com/microsoft/vscode/blob/a360ecfa4225f587afed0d17534cfccd94479ece/src/vs/workbench/contrib/chat/common/promptSyntax/config/promptFileLocations.ts), [prompt service](https://github.com/microsoft/vscode/blob/a360ecfa4225f587afed0d17534cfccd94479ece/src/vs/workbench/contrib/chat/common/promptSyntax/service/promptsServiceImpl.ts). VS Code source covers the native host, not every Copilot product. |
| `aider-desk` | Owned copies into `$AIDER_DESK_HOME_DIR/skills`, otherwise `~/($AIDER_DESK_DIR or .aider-desk)/skills`. Relative home override is cwd-relative. | Immediate directories/symlinks; metadata then body. Active agent profile must enable Skills Tools. Plain `aider` is not AiderDesk detection. [Constants](https://github.com/hotovo/aider-desk/blob/e76c2f04b0837dadaebf92f6d701d35942bd0be1/src/main/constants.ts), [SkillManager](https://github.com/hotovo/aider-desk/blob/e76c2f04b0837dadaebf92f6d701d35942bd0be1/src/main/skills/skill-manager.ts). |

## Terminal profiles

| Installer ID | Vendor discovery and current installation | Consumption and evidence |
| --- | --- | --- |
| `amp` | Universal install reaches `~/.agents/skills`; docs also accept XDG agents and legacy roots. | Metadata then instructions on selection. [Official docs](https://ampcode.com/docs/customize/skills). Inspected native package `0.0.1788811227-gce258b`; loader not readable, so discovery remains docs-backed. |
| `bob` | Upstream `.bob/skills` link; Bob Shell 2.0.2 also scans shared `.agents` and `.claude`. | Integrity-verified shipped loader accepts directories/symlinks; `onActivate` reads file body. [Vendor docs](https://bob.ibm.com/docs/shell/features/skills). Shell 1.x is outside this skill contract. |
| `crush` | Default source includes config `crush/skills` and shared `.agents`, covering upstream destinations. Nonempty `CRUSH_SKILLS_DIR` replaces all roots; Caveman copies into that exact directory. | Metadata for selection, instructions on activation. [Pinned loader](https://github.com/charmbracelet/crush/blob/7f9a8e4ca8c0365ac981d082470999900a5d10ce/internal/config/load.go). Windows/config-root differences retain shared fallback only when explicit override is absent. |
| `devin` | XDG/default `.config/devin/skills` and shared `.agents`; upstream destinations match. | Metadata, then instructions/permissions/model settings on invocation. [CLI docs](https://docs.devin.ai/cli/extensibility/skills/overview). Closed-source CLI `3000.6.7` identified; no account run. |
| `droid` | `.factory/skills`, shared `.agents`, `.agent`, and project roots; upstream link matches. | Enabled effective skill loads body on invocation. [Factory docs](https://docs.factory.ai/harness/skills). Installed native loader strings corroborate paths; no authenticated run. |
| `forgecode` | Configured base `skills`, shared `.agents`, project `.forge/skills`; global base supports `.forge` and legacy `forge`. | Later duplicate names override; loader supplies body. [Repository loader](https://github.com/antinomyhq/forge/blob/6ed5d37b6b45a2b6220877fd9aec5ba4c4b7f3c0/crates/forge_repo/src/skill.rs), [environment](https://github.com/antinomyhq/forge/blob/6ed5d37b6b45a2b6220877fd9aec5ba4c4b7f3c0/crates/forge_infra/src/env.rs). Shared copy covers legacy home; outdated docs alone are not an install defect. |
| `goose` | Platform/config Goose root plus shared `.agents`, even with `GOOSE_PATH_ROOT`; upstream shared copy matches. | Project precedes global; first duplicate wins; metadata then body. [Pinned loader](https://github.com/block/goose/blob/5e90925962f05acf8e255032de44d16c4a7768a2/crates/goose/src/skills/mod.rs). |
| `iflow` | Default upstream `.iflow/skills` link matches. Nonempty `IFLOW_HOME` makes Caveman copy into that home's `skills`. | Exact shipped 0.5.19 scanner/home/parser executed: default found, custom home missed before fix, correctly placed control found. Symlinks accepted; metadata and body parsed. [Published vendor package](https://registry.npmjs.org/@iflow-ai/iflow-cli/0.5.19), [docs](https://platform.iflow.cn/cli/examples/skill). Shipped duplicate precedence differs from docs; discovery cache can last five minutes. |
| `kiro` | Global `.kiro/skills`; upstream link matches. Detection now includes actual `kiro-cli`. | Metadata then body on match/invocation; custom agents need `skill://.../SKILL.md` resources. [Skills](https://kiro.dev/docs/skills/), [installer](https://cli.kiro.dev/install). Manifest reported 2.21.1; rolling docs and native version are separate evidence. |
| `mistral` | `$VIBE_HOME/skills`, shared agents-home skills and configured/project roots; upstream honors VIBE_HOME. Detection now includes actual `vibe`. | Metadata and duplicate resolution, then skill tool reads body. [Skill manager](https://github.com/mistralai/mistral-vibe/blob/6c79ef0e1ee484d7069bc38590d5917d3914cd48/vibe/core/skills/manager.py), [entry points](https://github.com/mistralai/mistral-vibe/blob/6c79ef0e1ee484d7069bc38590d5917d3914cd48/pyproject.toml). Version 2.25.0. User config-source selection can exclude user skills. |

## Other profiles and product scopes

| Installer ID | Vendor discovery and current installation | Consumption and evidence |
| --- | --- | --- |
| `openhands` | SDK user roots start with shared `.agents/skills`, then persistence `skills`/`microagents`; upstream shared copy matches. | Child directories resolve symlinks and find `SKILL.md`; merged metadata/body loading. [Skill source](https://github.com/OpenHands/software-agent-sdk/blob/df2ea8fa5542d5d2a543e108bc8b2d4fbbab34b1/openhands-sdk/openhands/sdk/skills/skill.py), [discovery](https://github.com/OpenHands/software-agent-sdk/blob/df2ea8fa5542d5d2a543e108bc8b2d4fbbab34b1/openhands-sdk/openhands/sdk/skills/utils.py). Local SDK/CLI home evidence; container/cloud home is a separate scope. |
| `qwen` | Global `$QWEN_HOME/skills` plus shared `.agents/skills`; upstream shared copy covers home override. | Directory/symlink scanning validates targets and parses metadata/body. [Storage](https://github.com/QwenLM/qwen-code/blob/7dc5cc745aea259d47b4500cd8f3cb599c87e321/packages/core/src/config/storage.ts), [loader](https://github.com/QwenLM/qwen-code/blob/7dc5cc745aea259d47b4500cd8f3cb599c87e321/packages/core/src/skills/skill-load.ts). |
| `rovodev` | User `.rovodev/skills` or shared `.agents/skills`; both upstream destinations match docs. | Name/description and directory-name match; refer to the skill in the prompt. [Official docs](https://support.atlassian.com/rovo/docs/extend-rovo-dev-cli-with-agent-skills/). Closed source. |
| `tabnine` | User `.tabnine/agent/skills` and shared `.agents/skills`; upstream link matches. | Vendor explicitly supports linked skills; metadata/body loading, trusted workspace roots only. [Official docs](https://docs.tabnine.com/main/getting-started/tabnine-cli/features/agent-skills). Closed source. |
| `trae` | Trae Code global `.trae/skills`; upstream link targets documented root. | Metadata then instructions. Project `.agents` discovery is optional and needs UI enablement. [Official IDE docs](https://docs.trae.ai/ide/skills), rendered page inspected. Closed source; no full host loading test. |
| `warp` | Global shared `.agents/skills`; universal install matches. | Skills supply instructions on use. [Official skills docs](https://docs.warp.dev/agents/capabilities/skills/). Closed source; Warp cloud/Drive objects have separate scope. |
| `replit` | Project `.agents/skills`; installer now omits `-g` and reports cwd. | Run from the Replit project's Shell. Workspace-wide library is managed in Workspace Settings. [Official docs](https://docs.replit.com/features/agent/skills). Closed source; home-global installation was not documented discovery. |
| `junie` | Global `.junie/skills` and shared `.agents/skills`; upstream destinations match. | Skills discovered and used subject to enabled locations. [Official docs](https://junie.jetbrains.com/docs/agent-skills.html). Closed source; intentionally disabled defaults remain disabled. |
| `qoder` | Qoder CLI/IDE user `.qoder/skills`; upstream link matches. | Metadata first, automatic selection or `/skill-name`. [Official CLI docs](https://docs.qoder.com/cli/Skills). QoderWork's `.qoderwork/skills` is a separate product scope. |
| `antigravity` | Owned copies into Antigravity IDE `.gemini/antigravity/skills`; current universal upstream algorithm misses this declared destination. | [Official IDE skills docs](https://www.antigravity.google/docs/ide/skills/) (IDE 2.5.5). Explicit selection retains the existing installer ID. Closed source; no native loading run. |
| `antigravity-2` | Owned copies into Antigravity 2.0 `.gemini/config/skills`, distinct from IDE. | [Official 2.0 skills docs](https://www.antigravity.google/docs/skills/) (2.12.2). Separate explicit target; no guessed filesystem detection or multi-home install. Closed source; no native loading run. |

## Native installer integrations

Claude uses its plugin manager, Gemini its extension manager, opencode owned
plugin/rules files, OpenClaw workspace skills plus a marker-fenced SOUL.md block,
and Hermes its native skill directory. Their implementations remain in
`bin/install.js` and the corresponding `bin/lib` helpers; temporary-home install,
ownership, restart and hook tests cover these paths. See [INSTALL.md](../../INSTALL.md)
for activation and override instructions. These skill installation paths do not
establish proxy compression compatibility; [provider audit](provider-compatibility-audit.md)
records that separate requirement and its remaining evidence.

## Reproduction and limits

`tests/installer/provider-skills.test.mjs` checks vendor path semantics on POSIX
and Windows path implementations and filesystem ownership behavior.
`provider-skills-integration.test.mjs` launches the real installer in temporary
homes, checks every copied skill body, forbids incompatible delegation, exercises
detached staging, and verifies preservation during uninstall.
`skills-global-install.test.mjs` checks actual delegated argv, Replit scope and
command detection. These files are included in the Windows compatibility gate.

Full native loading remains unverified for many hosts, especially closed-source
products. Feature toggles, trust, custom-agent resources, conflicting names and
remote homes can deliberately exclude otherwise valid skills. Native Windows
execution of the final tree is required before claiming Windows conformance;
POSIX tests of Windows path functions and cross-compilation do not replace it.
