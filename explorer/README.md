# @caveman/explorer

Read-only repository exploration subagent inspired by FastContext
([arXiv 2606.14066](https://arxiv.org/abs/2606.14066)). It keeps file reads and
searches in isolated subagent context, then answers location questions with
compact `path:line` citations for solver.

## Install

```bash
cave explore install            # writes ./.claude/agents/fastcontext.md (this repo)
cave explore install --user     # writes ~/.claude/agents/fastcontext.md (all repos)
```

Claude Code can delegate broad exploration to `fastcontext`, a Haiku agent with
Read, Glob and Grep only. It cannot edit files or run commands; solver receives
final citations rather than explorer transcript.

Codex is not wired. Its integration needs a transcript-isolation test before release.

## Claim boundary

This package does not implement paper's trained model and does not inherit paper's
reported results. Local explorer uses your configured Claude Code model and
credential. Whether delegation reduces total tokens or improves task result depends
on repository, question, solver, and explorer model. Measure complete task, including
explorer calls, before claiming a benefit.

## How it relates to the rest of Caveman

Explorer is optional complement to Engine compression. Engine reduces selected
payloads in one context; explorer keeps broad repository search in another context
and returns citations. Neither mechanism proves savings without complete-task
comparison.
