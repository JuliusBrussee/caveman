# caveman-commit

Terse Conventional Commits. Why over what.

## What it does

Generates commit messages in Conventional Commits format. Subject ≤50 chars, hard cap 72. Imperative mood. Body only when the *why* is non-obvious or there are breaking changes. No AI attribution, no "this commit does X", no emoji unless the project uses them. Body always required for breaking changes, security fixes, data migrations, and reverts — future debuggers need the context.

Outputs only the message. Does not stage, commit, or amend.

## How to invoke

```
/caveman-commit
```

Also triggers on phrases like "write a commit", "commit message", "generate commit".

## Example output

Diff: new endpoint for user profile.

```
feat(api): add GET /users/:id/profile

Mobile client needs profile data without the full user payload
to reduce LTE bandwidth on cold-launch screens.

Closes #128
```

Diff: breaking API rename.

```
feat(api)!: rename /v1/orders to /v1/checkout

BREAKING CHANGE: clients on /v1/orders must migrate to /v1/checkout
before 2026-06-01. Old route returns 410 after that date.
```

## See also

- [`SKILL.md`](./SKILL.md) — full LLM-facing instructions
- [Caveman README](../../README.md) — repo overview
