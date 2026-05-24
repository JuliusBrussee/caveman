# Elastic Agents — Two-Tier Investigator Pattern

## Problem

One investigator model fits all queries. Simple lookups waste capable model. Complex queries starve on cheap model.

## Solution

Two tiers. Parent (main thread) picks tier by task complexity. Investigator can flag ambiguous results for escalation.

```
Main thread
  ├── simple lookup → cavecrew-investigator (fast/cheap)
  ├── complex trace → cavecrew-investigator-deep (capable)
  └── ambiguous result → investigator flags ESCALATE → main spawns deep
```

## Tiers

| Tier | Agent | Model | Use for |
|---|---|---|---|
| Fast | `cavecrew-investigator` | `opencode-go/qwen3.6-plus` | Symbol lookup, file search, grep, "where is X", "what calls Y" |
| Deep | `cavecrew-investigator-deep` | `opencode/claude-sonnet-4-5` | Cross-module tracing, architecture questions, dependency chains, ambiguous references |

## Cost Comparison

| Metric | Fast (qwen3.6-plus) | Deep (sonnet-4-5) |
|---|---|---|
| Input cost | ~$0.10/M tokens | ~$3.00/M tokens |
| Output cost | ~$0.30/M tokens | ~$15.00/M tokens |
| Latency | ~1-3s | ~5-15s |
| Best for | 80% of lookups | 20% complex queries |
| Output format | `path:line — symbol — note` | Same + architecture context |

**Rule:** Always try fast tier first. Escalate only when needed. Saves ~70% on investigation costs.

## Escalation Workflow

### Path 1: Parent decides (preferred)

Main thread assesses task complexity before spawning:
- "Where is function X?" → fast
- "Trace the full request lifecycle from router to database" → deep

### Path 2: Investigator flags

Fast investigator encounters ambiguity:
1. Returns partial results
2. Appends `ESCALATE: spawn cavecrew-investigator-deep for thorough analysis`
3. Main thread reads flag, spawns deep agent with same query

### Escalation triggers

- Zero or single hit when multiple expected
- Results span 5+ files (investigator stops at surface)
- Query mentions "architecture", "flow", "lifecycle", "dependency"
- Symbol appears in multiple contexts (overloaded, polymorphic)

## Adding More Tiers

Pattern extends to N tiers:

1. Create agent file: `~/.config/opencode/agents/cavecrew-investigator-<tier>.md`
2. Set `model:` to target provider/model
3. Write description mentioning when to use this tier
4. Add escalation instruction pointing to next tier up
5. Update this doc with new tier in table

Example third tier (ultra-deep for full codebase analysis):
```yaml
model: opencode/claude-opus-4-1-20250805
description: Full codebase architecture analysis. Use when both investigator tiers insufficient.
```

## Agent-to-Agent Spawning

**Can a subagent spawn another subagent?** Yes, with caveats.

OpenCode's `task` tool is available to any agent with `permission.task` set. Subagents can invoke other subagents through the Task tool if:
- The subagent has `task` permission not set to `deny`
- The target subagent is not blocked by `permission.task` glob patterns on the calling agent

**Evidence:**
- OpenCode docs (`/docs/agents/`): "Control which subagents an agent can invoke via the Task tool with `permission.task`"
- Permission table includes `task` key gating the `task` tool
- Hidden agents note: "Hidden agents can still be invoked by the model via the Task tool if permissions allow"

**Current cavecrew design:** The SKILL.md chaining patterns show main thread as orchestrator (investigator → builder → review all spawned by main). Agent-to-agent chaining is technically supported but not the documented pattern. Use main thread as dispatcher for now.

## Files

| File | Purpose |
|---|---|
| `agents/cavecrew-investigator.md` | Fast tier — simple lookups |
| `agents/cavecrew-investigator-deep.md` | Deep tier — complex analysis |
| `agents/cavecrew-builder.md` | Edit tier — 1-2 file changes |
| `agents/cavecrew-reviewer.md` | Review tier — diff audit |
