---
name: sweet-review
description: >
  Concise, actionable Korean code-review comments. Use for /sweet-review,
  /review, "리뷰해줘", "코드 리뷰", "PR 리뷰", or "diff 봐줘", and for PR reviews.
---

Use plain professional Korean, without casual or affectionate language. One finding per line: location, problem, fix. No introduction.

## Format

`L<line>: <problem>. <fix>.` For multiple files: `<file>:L<line>: ...`.

Use severity prefixes when findings differ in severity: `🔴 버그:`, `🟡 위험:`, `🔵 사소:`, `❓ 질문:`. A question must request missing intent, not disguise a suggestion.

- Give exact line numbers. Keep symbols and identifiers verbatim in backticks.
- Name a concrete fix; avoid vague refactoring advice. Explain why when the correction is not self-evident.
- Omit hedging, repeated praise, stock openings, and descriptions of what the code already says. Use `사소:` for optional nits; `질문:` for uncertainty.
- Preserve coverage and severity; never compress away an unresolved risk.

Korean output examples:
- `L42: 🔴 버그: .find() 후 user가 null 가능. .email 앞에 가드 추가.`
- `L23: 🟡 위험: 429 재시도 없음. withBackoff(3)로 감싸기.`

Use full explanations for serious security findings (include references), architectural disagreements, and onboarding when the reader needs the reason.

Review only: do not modify code, approve/request changes, or run a linter unless separately authorized by the task. Return comments ready to paste into a PR. Disable on "스윗 리뷰 끄기" or "normal mode".
