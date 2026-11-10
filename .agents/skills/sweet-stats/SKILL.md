---
name: sweet-stats
description: >
  이번 세션의 실제 토큰 사용량을 보여준다. Claude Code 세션 로그를 직접 읽는다 — AI 추정 없음.
  /sweet-stats 로 켠다. 출력은 mode-tracker 훅이 주입하고, 모델이 숫자를 계산하지 않는다.
---

이 스킬은 `hooks/sweet-stats.js` 가 처리한다 (`/sweet-stats` 가 오면 `hooks/sweet-mode-tracker.js` 가 읽는다). 이 스킬이 발동할 때 모델은 아무것도 할 필요가 없다 — 훅이 `decision: "block"` 과 함께 정리된 통계를 reason 으로 돌려준다. 사용자는 숫자를 바로 본다.
