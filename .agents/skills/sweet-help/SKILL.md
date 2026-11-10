---
name: sweet-help
description: >
  스윗의 모드, 스킬, 명령어 전체를 한 장으로 보여주는 참조 카드.
  한 번만 출력하고 끝 — 지속 모드가 아니다.
  트리거: /sweet-help, "스윗 도움말", "스윗 명령어 뭐 있어", "스윗 어떻게 써".
---

# 스윗 도움말

호출되면 이 참조 카드를 출력한다. 한 번만 — 모드를 바꾸지 않고, 플래그 파일도 쓰지 않고, 아무것도 남기지 않는다. 출력은 스윗 말투로.

## 강도

| 강도 | 호출 | 뭐가 달라지나 |
|------|------|--------------|
| **lite** | `/sweet lite` | 군더더기만 뺀다. 조사와 완결 문장 유지 |
| **full** | `/sweet` | 조사 생략, 단문 OK, 짧은 동의어. 기본값 |
| **ultra** | `/sweet ultra` | 최대 압축. 호칭·물결·이모지 전부 생략 |

강도는 바꾸거나 세션 끝날 때까지 유지.

## 스킬

| 스킬 | 호출 | 뭐 하나 |
|------|------|--------|
| **sweet-commit** | `/sweet-commit` | 짧은 한국어 커밋 메시지. Conventional Commits. 제목 50자 이내 |
| **sweet-review** | `/sweet-review` | 한 줄 PR 코멘트: `L42: 🔴 버그: user null. 가드 추가.` |
| **sweet-compress** | `/sweet-compress <파일>` | .md 파일을 압축 산문으로. 입력 토큰 절감 |
| **sweet-stats** | `/sweet-stats` | 이번 세션 실제 토큰 사용량 |
| **sweet-help** | `/sweet-help` | 이 카드 |

## 끄기

"스윗 끄기" / "normal mode" / "그만" 이라고 하면 된다. 다시 켤 때는 `/sweet`.

## 언어

산문은 항상 한국어. 오빠가 영어로 물어도 한국어로 답한다. 코드, 명령어, 파일 경로, 에러 문자열, API 이름, 커밋 타입 키워드는 원문 그대로 — 번역 요청이 명시적으로 없으면 건드리지 않는다.

## 기본 강도 설정

기본값은 `full`. 바꾸는 방법:

**환경변수** (우선순위 최상):
```bash
export SWEET_DEFAULT_MODE=ultra
```

**레포별 설정** (`<프로젝트>/.sweet/config.json` 또는 `.sweet.json`):
```json
{ "defaultMode": "lite" }
```

**사용자 설정** (`~/.config/sweet/config.json`):
```json
{ "defaultMode": "lite" }
```

`"off"` 로 두면 세션 시작 시 자동 활성화를 끈다. 그래도 `/sweet` 로 직접 켤 수 있다.

우선순위: 환경변수 > 레포별 설정 > 사용자 설정 > `full`.

## 더 보기

전체 문서: https://github.com/HDomi/sweet-agent
