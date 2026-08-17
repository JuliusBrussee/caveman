# skills/caveman-learn — the Caveman Learn editing skill (MIT, public)

The consent-gated half of `caveman learn`. The analyzer (the Go proxy) **measures**
where an agent's tokens go and writes a ranked plan; this skill is what an agent
loads to **act** on that plan — proposing each fix and applying it only with the
user's per-edit yes. It is the loop-closer the learn spec §10
describes, plus the new `cavemem_offload` move.

## Layout
- `SKILL.md` — the canonical skill body (frontmatter `name: caveman-learn` + a
  trigger-phrase `description`; body = the read-plan → per-class consent loop). This
  file is the source of truth.
- `tests/skill-file.test.mjs` — asserts the canonical file is well-formed and honest
  (frontmatter present; the net-token-negative gate, the never-make-the-agent-dumber
  guard, consent-per-edit, and reversibility are all stated; no imperative for
  behavioral findings; no placeholders).

## Install path
`caveman tools skills install caveman-learn` (in `../../cli/src/index.ts`) writes this file
into a repo's `.claude/skills/caveman-learn/SKILL.md` (Claude Code) or
`~/.codex/skills/caveman-learn/SKILL.md` (Codex). The CLI **embeds a byte-identical copy**
(`CAVEMAN_LEARN_SKILL_MD`) because the published CLI ships no sibling assets;
`../../cli/tests/skills.runtime.mjs` asserts the embedded copy equals this canonical
file (the drift guard). **Change this file and that constant together.**

## Boundary (binding)
The skill — using the agent's own file tools — is the ONLY thing that edits a user's
config. `caveman learn apply` stays read-only (it materializes candidates), and
`caveman mem *` are mechanical store ops. The offload move enforces a net-token-negative
gate and the never-make-the-agent-dumber guard before any trim.

See ../../mem/CLAUDE.md (cavemem) · ../caveman-explore/SKILL.md (the packaging precedent)
