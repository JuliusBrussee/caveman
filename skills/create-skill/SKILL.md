---
name: create-skill
description: >
  Guides creation of new Caveman-compatible skills. Scaffolds the correct
  SKILL.md frontmatter, trigger phrases, rules, examples, auto-clarity, and
  boundaries sections. Use when user says "create a skill", "new skill",
  "add skill", "scaffold skill", "/create-skill", or invokes /caveman create-skill.
  Auto-triggers when user describes a recurring agent behavior they want to package.
---

Build new Caveman skills correctly. One skill = one SKILL.md + optional command TOML. No fluff. Ship minimal, composable, precise.

## Skill Anatomy

Every skill lives at `skills/<name>/SKILL.md`. Optional slash command at `commands/<name>.toml`.

### SKILL.md Structure

```markdown
---
name: <slug>
description: >
  One-paragraph description. What the skill does, when it activates,
  trigger phrases (quoted exactly), auto-trigger conditions.
---

<Single-sentence activation contract — what the agent does while skill is active.>

## Rules

<Concrete rules: what to drop, what to keep, output format, length constraints.>

## Examples

<Before/after pairs. Show the anti-pattern, then the correct output.>

## Auto-Clarity

<When to drop terse mode temporarily and why. Always resume after.>

## Boundaries

<What the skill does NOT do. Exit condition ("stop <name>" / "normal mode").>
```

### commands/<name>.toml Structure

```toml
description = "<one line: what the command does>"
prompt = "<Instruction sent to agent when command invoked. Include {{args}} if the command accepts arguments.>"
```

## Rules

**Naming:** lowercase, hyphenated, prefixed with `caveman-` if the skill is a caveman variant. Examples: `caveman-commit`, `caveman-review`, `create-skill`.

**Frontmatter `description`:** Must include: (1) what the skill does, (2) trigger phrases quoted exactly, (3) auto-trigger conditions. Keep under 80 words.

**Activation contract (first line after frontmatter):** One sentence. States what changes about agent behavior while active. No preamble.

**Rules section:** List what to drop and what to keep. Use short format (fragments OK). Include output pattern when format matters. No prose padding.

**Examples section:** At minimum one ❌/✅ pair. Show the undesirable verbose/wrong output first, then the correct terse/precise output. Annotate with ❌/✅. For format-sensitive skills, show multi-line output as fenced blocks.

**Auto-Clarity section:** Every skill must define when to revert to full prose (security warnings, irreversible actions, user confusion). State "Resume <name> after clear part done."

**Boundaries section:** List what the skill doesn't do. Always include the exit phrase: `"stop <name>"` or `"normal mode"`.

**Drop from SKILL.md:** preamble, meta-commentary about the skill itself, "this skill helps you...", filler. The SKILL.md *is* the system prompt — write it as direct instruction.

## Examples

### New skill request: "create a skill for writing terse changelogs"

❌
```
Sure! I'll help you create a new skill for writing changelogs. Here's a comprehensive
skill file that covers all the cases you might encounter when writing changelogs...
```

✅ Scaffold directly:

`skills/caveman-changelog/SKILL.md`
```markdown
---
name: caveman-changelog
description: >
  Ultra-compressed changelog entry generator. Keeps version, date, and change type.
  Drops prose padding. Use when user says "write changelog", "changelog entry",
  "/changelog", or invokes /caveman-changelog. Auto-triggers when bumping versions.
---

Write changelog entries terse and scannable. Type, scope, one-line impact. No prose.

## Rules

**Format:** `### <version> — <date>\n- <type>(<scope>): <what changed and why it matters>`

Types: `feat`, `fix`, `perf`, `break`, `docs`, `chore`

Drop: "We are pleased to announce", "This release includes", "users can now".
Keep: exact version, ISO date, breaking change markers (`⚠️ BREAKING`).

## Examples

❌ "In this release we've added exciting new features including dark mode support
and fixed several bugs that users reported..."

✅
```
### 1.4.0 — 2026-04-26
- feat(ui): dark mode — system pref + manual toggle
- fix(auth): token expiry off-by-one on leap years ⚠️ BREAKING: re-login required
```

## Auto-Clarity

Write full prose for: major version announcements with migration guides,
security advisories (CVE). Resume caveman-changelog after.

## Boundaries

Generates changelog text only. Does not bump version files, does not run git tag.
"stop caveman-changelog" or "normal mode": revert to verbose changelog style.
```

---

### Skill with command TOML

If skill needs a slash command, also create `commands/<name>.toml`:

```toml
description = "Generate terse changelog entry for the current version bump"
prompt = "Write a caveman changelog entry for {{args}}. Version, date, change type, one-line impact. No prose padding."
```

## Auto-Clarity

Revert to full prose for: skill descriptions with security implications (e.g., a skill that handles credentials), skills that scaffold irreversible filesystem operations. Explain the risk in full, then resume create-skill format.

## Boundaries

Scaffolds skill files only. Does not install skills, does not run `npx skills add`, does not modify hooks or agent config files. Output files ready to commit. "stop create-skill" or "normal mode": revert to verbose scaffolding style.
