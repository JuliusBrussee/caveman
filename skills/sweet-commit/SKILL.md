---
name: sweet-commit
description: >
  Write concise Korean Conventional Commits messages. Use for /sweet-commit,
  /commit, "커밋 메시지 써줘", "커밋해줘", or "커밋 메시지"; also when staging
  changes. Explain intent and non-obvious reasons, not a diff inventory.
---

Write plain professional Korean, without casual or affectionate language. Follow an explicit repository-language requirement when the user supplies one.

## Format

- Subject: `<type>(<scope>): <summary>`. Scope optional. Aim for 50 characters; hard cap 72. No final period. Follow repository capitalization conventions.
- Types: `feat`, `fix`, `refactor`, `perf`, `docs`, `test`, `chore`, `build`, `ci`, `style`, `revert`. Keep types and identifiers verbatim.
- Use concise Korean noun endings, not conversational sentences.
- Body only for a non-obvious reason, breaking change, migration, or linked issue. Wrap at 72 characters; use `-` bullets. End with references such as `Closes #42` or `Refs #17`.
- Always include context for breaking changes, security fixes, data migrations and reverts. Use `!` and a `BREAKING CHANGE:` footer when appropriate.

Omit first-person narration, request history, timestamps, obvious diff descriptions and repeated filenames already in scope. No emoji unless the repository requires them. Add AI attribution only when the user's rules require it, as a trailer.

Example Korean output:
```text
feat(api): GET /users/:id/profile 추가

모바일 콜드 런치에서 전체 user 대신 프로필만 조회해 대역폭 절감.

Closes #128
```

This skill formats messages; it does not itself authorize staging, commit, amend, or push. Execute those only when separately authorized by the task. Otherwise return a copyable code block. Disable on "스윗 커밋 끄기" or "normal mode".
