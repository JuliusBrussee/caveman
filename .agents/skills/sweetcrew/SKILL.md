---
name: sweetcrew
description: >
  스윗 스타일 서브에이전트에 위임할지 판단하는 가이드. 메인 스레드가
  `sweetcrew-investigator`(코드 위치 찾기), `sweetcrew-builder`(1~2개 파일 수정),
  `sweetcrew-reviewer`(diff 리뷰) 를 언제 띄워야 하는지 알려준다. 직접 하거나
  기본 `Explore` 를 쓰는 것과 비교한다. 서브에이전트 출력이 압축돼 있어서
  메인 컨텍스트로 되돌아오는 tool-result 가 작다 — 긴 세션에서 컨텍스트가 더 오래 버틴다.
  트리거: "서브에이전트한테 넘겨", "sweetcrew 써", "investigator/builder/reviewer 띄워",
  "컨텍스트 아껴", "압축된 에이전트 출력".
---

sweetcrew = 압축 출력을 내는 서브에이전트 프리셋 3개. 하는 일은 Anthropic 기본 에이전트(`Explore`, 편집 계열, 리뷰어)와 같다. 다른 점은 돌려주는 tool-result 가 압축돼 있어서 위임 한 번당 메인 컨텍스트를 덜 먹는다는 것.

## sweetcrew vs 대안

| 할 일 | 쓸 것 |
|---|---|
| "X 어디 정의됐어 / Y 누가 호출해 / Z 쓰는 곳 전부" | `sweetcrew-investigator` |
| 같은 일 + 제안이나 아키텍처 의견도 받고 싶다 | `Explore` (기본) |
| 정밀 수정, 2개 파일 이하, 범위 명확 | `sweetcrew-builder` |
| 신규 기능 / 3개 파일 이상 / 전방위 리팩터링 | 메인 스레드 또는 `feature-dev:code-architect` |
| diff·브랜치·파일에서 버그 찾기 | `sweetcrew-reviewer` |
| 근거와 대안까지 포함한 깊은 리뷰 | `Code Reviewer` (기본) |
| 이미 아는 한 줄 답 | 메인 스레드, 서브에이전트 안 씀 |

기준: **서브에이전트 출력을 1/3 토큰으로 받고 싶으면 sweetcrew. 산문으로 받고 싶으면 기본 에이전트.**

## 왜 있나 (실제 이득)

서브에이전트 tool-result 는 메인 컨텍스트에 그대로 주입된다. 기본 `Explore` 가 산문 2k 토큰을 돌려주면 매번 메인 컨텍스트 예산 2k를 쓴다. 같은 발견을 `sweetcrew-investigator` 가 돌려주면 훨씬 작다. 한 세션에서 20번 위임하면 컨텍스트가 터지느냐 작업을 끝내느냐가 갈린다.

## 출력 계약

메인 스레드가 에이전트별로 믿을 수 있는 것:

**`sweetcrew-investigator`**
```
<헤더>:
- path:line — `symbol` — 짧은 메모
합계: <개수>.
```
또는 `없음.` 항상 파일 경로 먼저, 줄 번호 붙임, 심볼은 백틱. `path:\d+` 로 grep 해도 안전. 목록이 완전하다고 확신 못 하면 마지막 줄에 `불완전: <이유>.`

**`sweetcrew-builder`**
```
<path:line-range> — <변경 10단어 이내>.
검증: <재확인 OK | 불일치 @ path:line>.
테스트 미실행 — 메인 스레드가 <명령> 돌려야 함.
```
또는 다음 중 하나 (종결 첫 토큰): `너무 큼.` / `확인 필요.` / `모호.` / `회귀.`

**`sweetcrew-reviewer`**
```
path:line: <이모지> <심각도>: <문제>. <수정>.
합계: N🔴 N🟡 N🔵 N❓
```
또는 `이상 없음.` 발견은 파일 → 줄 번호 오름차순 정렬. 다 못 봤으면 `불완전: <안 본 범위>.`

## 품질 경계 (메인 스레드 책임)

압축되는 건 **출력 형식**이고, 조사 범위나 검증 수준은 아니다. 위임하면 메인 스레드가 이걸 떠안는다:

- **builder 수정 뒤 테스트는 메인 스레드가 돌린다.** builder 에 `Bash` 가 없어서 테스트·타입체크·린트를 못 돌린다. 영수증 마지막 줄이 `테스트 미실행` 인 이유. 이 줄 보고 넘어가면 검증 안 된 diff 가 그대로 남는다.
- **`불완전:` 이 붙어 오면 범위를 좁히지 말고 다시 조사한다.** investigator 목록이 완전하다는 전제로 수정 범위를 정하면, 놓친 호출부가 깨진다.
- **깊은 판단이 필요한 리뷰는 기본 `Code Reviewer`.** sweetcrew-reviewer 는 한 줄 발견만 낸다. 아키텍처 판단, 근거 있는 반대 의견, 보안 심층 분석은 범위 밖.
- **모델 바꾸기:** `SWEETCREW_INVESTIGATOR_MODEL` / `SWEETCREW_BUILDER_MODEL` / `SWEETCREW_REVIEWER_MODEL` 로 에이전트별 모델을 지정할 수 있다. investigator 는 기계적 탐색이라 기본이 haiku 고, builder·reviewer 는 세션 모델을 그대로 물려받는다.

## 연결 패턴

**찾기 → 고치기 → 검증** (가장 흔함):
1. `sweetcrew-investigator` 가 위치 목록을 돌려준다.
2. 메인 스레드가 1~2곳을 골라 경로를 `sweetcrew-builder` 에 넘긴다.
3. `sweetcrew-reviewer` 가 diff를 감사한다.

**병렬 정찰** (조사 범위가 넓을 때):
`sweetcrew-investigator` 를 한 메시지에서 2~3개 띄운다 (각각 다른 각도: 정의 / 호출자 / 테스트). 메인 스레드에서 합친다.

**단발 수정** (위치를 이미 알 때):
investigator 생략. 정확한 path:line 을 `sweetcrew-builder` 에 바로 넘긴다.

## 하지 말 것

- 어느 파일인지 모르는 상태로 `sweetcrew-builder` 쓰지 마라. investigator 먼저 띄우지 않으면 메인 스레드가 맥락 전달하느라 토큰을 먹는다.
- 5개 파일 리팩터링에 `sweetcrew-investigator → sweetcrew-builder` 를 엮지 마라. builder 가 `너무 큼.` 을 돌려주고 한 턴 낭비된다.
- `sweetcrew-reviewer` 에게 "전반적인 피드백" 을 요구하지 마라 — 발견만 돌려주고 아키텍처 의견은 없다. 그건 `Code Reviewer` 를 쓴다.
- 산문을 기대하지 마라. sweetcrew 출력은 구조화돼 있고, 때로는 암호처럼 짧다. 사람이 직접 읽을 거라면 메인 스레드에서 풀어 쓴다.

## 말투 예외 (중요)

sweetcrew 서브에이전트 출력에는 애교와 호칭("오빠")이 없다. 이건 사람이 아니라 메인 스레드가 읽는 데이터라서 그렇다. 압축과 한국어는 유지하고, 다정한 말투만 뺀다. 사람에게 최종 답을 낼 때는 메인 스레드가 다시 스윗 말투로 쓴다.

## 자동 명확화 (상속)

서브에이전트도 보안 경고, 되돌릴 수 없는 작업 확인, 압축하면 잘못 읽힐 수 있는 출력에서는 압축을 끄고 완전한 한국어 문장으로 쓴다. 끝나면 복귀.
