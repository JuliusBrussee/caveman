# Sweet Agent

한국어로 짧고 다정하게 답하는 Caveman 포크야. 반말과 선택적인 오빠 호칭을 쓰고, 코드·명령어·오류·기술 내용은 그대로 보존해.

> React가 매번 새 객체 참조를 받아서 리렌더돼. 인라인 객체를 `useMemo`로 감싸면 돼, 오빠.

## 설치

이 브랜치를 포함한 로컬 체크아웃에서 설치해.

```sh
node installer/sweet.js --list
node installer/sweet.js --only codex
node installer/sweet.js --only claude
```

`--dry-run`으로 먼저 설치 계획을 확인할 수 있어. 실제 설치는 요청한 명령 하나만 실행하면 돼.

이 브랜치의 저장소 루트에서 Claude Code 마켓플레이스를 등록할 수도 있어.

```sh
claude plugin marketplace add .
claude plugin install sweet-agent@sweet-agent
```

플러그인 이름은 `sweet-agent`. 내부 설정 경로·환경 변수·엔진 패키지는 모체 `caveman` 이름을 유지해. `/sweet` 스킬과 한국어 페르소나가 포크의 차이야. 이전 버전의 `~/.claude/.sweet-active` 등은 자동으로 옮기지 않아. 이전 sweet 설치가 있다면 이중 훅이 등록되지 않도록 기존 설치를 제거한 뒤 새 버전을 설치해.

## 명령

- `/sweet`, `/sweet full`: 기본 한국어 반말. 플러그인 네임스페이스는 `/sweet-agent:sweet`.
- `/sweet lite`: 문장을 유지하고 군더더기만 줄이기.
- `/sweet ultra`: 최대 압축. 물결·이모지 없이 기술 내용 보존.
- `/sweet off`, `/sweet status`: 해제·상태 확인.
- `/sweet-commit`, `/sweet-review`: 평문 한국어 커밋 메시지·리뷰.
- `/sweet-compress <파일>`: 코드·주석·경로를 보존하는 파일 압축.
- `/sweet-stats`, `/sweet-help`: 실제 사용량·도움말.

lite/full은 내부 `caveman` 슬롯을 공유하고, 선택한 강도는 대화 지시로 유지해. ultra는 `ultracave` 슬롯을 사용해. `/megacave`도 한국어 ultra 호환 모드야. 중국어로 전환하지 않아.

## 모체 업데이트

Caveman v4.1.0 이후 main의 `3dc497a7`을 기반으로 해. 파일 압축 안전성·설치기·훅·엔진 개선을 포함해. 상세 기능과 지원 에이전트는 [모체 문서](https://github.com/JuliusBrussee/caveman/tree/3dc497a7f858c774d6e15b25e8c6120ef3a0eda8)를 참고해.

**한국어 토큰 절감률은 아직 측정하지 않았어.** 모체의 영어 측정치를 한국어 성능으로 제시하지 않아. 기존 측정 작업은 백업과 Git stash에 보존했어.

유지보수·복구 절차는 [SWEET-FORK.md](docs/SWEET-FORK.md). [Apache-2.0](./LICENSE)과 기존 MIT 고지는 [LICENSING.md](LICENSING.md), [NOTICE](NOTICE), [LICENSE-MIT](LICENSE-MIT)를 따른다.

추가 스킬: `/sweetcrew`는 기존 `cavecrew-*` 에이전트에 위임하는 한국어 가이드야.

내부 지시문은 입력 토큰을 아끼도록 영어로 작성했어. 답변은 한국어로 유지하고, 파일 압축은 원문의 언어를 보존해. 스킬별 측정치는 [지시문 토큰 비교](docs/sweet-instruction-tokens.json)에 기록해.
