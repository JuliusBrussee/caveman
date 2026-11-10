# Sweet 포크 유지보수

## 기준과 범위

- 모체: `JuliusBrussee/caveman`.
- 기존 공통 조상: `0d95a81d35a9f2d123a5e9430d1cfc43d55f1bb0` (2026-07-03).
- 새 기준: `3dc497a7f858c774d6e15b25e8c6120ef3a0eda8` (v4.1.0 이후 main).
- 포함된 모체 커밋: 1,777개. 기존 포크 커밋 2개는 백업 브랜치에 보존.
- 작업 브랜치: `migration/upstream-4.1.0`. 기존 `main`은 변경하지 않음.

이 포크는 모체 엔진 위에 한국어 반말·오빠 호칭·정확도 보호 규칙을 적용한다. 내부 `caveman` 파일명·설정 경로·엔진 패키지는 유지한다. 공개 플러그인 식별자는 `sweet-agent`다. 이전 포크처럼 저장소 전체 이름을 치환하지 않는다. 공개 `/sweet*` 스킬과 설치 진입점만 추가한다.

`/sweet lite`와 `/sweet full`은 모체의 `caveman` 슬롯을 공유한다. 강도는 사용자의 대화 지시로 유지하며 상태 보고에는 내부 모드명이 나온다. `/sweet ultra`는 `ultracave`. 기존 `megacave`도 한국어 ultra 호환 모드로 바꿨다. 이전 중국어 지시는 적용하지 않는다.

## 포크 차이 소유권

- `skills/{caveman,ultracave,megacave}/SKILL.md`: 최신 모체 규칙 + 한국어 페르소나. CLI 생성 프롬프트도 여기서 파생된다.
- `skills/sweet*/SKILL.md`: 공개 명령 별칭. `sweet-compress/scripts/`는 복사본이며 직접 수정하지 않는다.
- `skills/caveman-compress/scripts/validate.py`: 기존 포크의 부정문·조건 표현 보존 검사 추가. 모체의 최신 파일 안전성 로직은 유지한다.
- `src/hooks/caveman-parse.js`: `/sweet` 별칭과 짧은 한국어 모드 지시. 코드·UI 수정 요청과 인용문은 모드 전환으로 취급하지 않는다.
- `src/hooks/caveman-config.js`, 활성화·추적 훅, `.codex/codex-sessionstart.js`: 스킬 누락 시에도 한국어와 정확도 경계 유지.
- `skills/activation-rule.mjs`, OpenClaw 부트스트랩: 항상 켜지는 지시에도 동일한 페르소나 적용.
- `agents/cavecrew-*.md`: 평문 한국어와 불완전·테스트 미실행 보고. reviewer는 세션 모델 상속.
- `installer/sweet.js`, 설치기 저장소 주소, 셸/PowerShell 진입점: 포크에서 스킬을 받는다. 런타임 패키지 자체는 모체 패키지를 유지한다.
- `scripts/sync-sweet.mjs`: 별칭 압축 스크립트, 플러그인 배포본, 훅 체크섬, 결정적 ZIP 생성.

모체가 배포한 npm CLI/바이너리는 이 체크아웃의 수정으로 바뀌지 않는다. 로컬 소스의 CLI 프롬프트 생성본은 갱신했지만, 공식 npm CLI가 독자적으로 주입하는 프롬프트까지 한국어로 바꾸려면 수정된 CLI를 따로 빌드·배포해야 한다. 이 작업은 로컬 플러그인·스킬·훅을 검증하며 별도 패키지 게시를 수행하지 않는다.

## 갱신

1. 사용자 작업을 보존하고 현재 포크를 백업한다.
2. `git fetch upstream` 후 새 작업 브랜치에서 `upstream/main`을 병합하거나 포크 차이 커밋을 재적용한다.
3. 위 파일의 포크 차이만 충돌 해결한다. 압축기·설치기 전체를 이전 버전으로 덮어쓰지 않는다.
4. `npm run sync:sweet`로 생성본을 갱신한다.
5. `npm test`, 파일 압축/검증 Python 테스트, `python3 tests/verify_repo.py`, `git diff --check`를 실행한다.
6. 검증 후 게시할 때 포크 태그 또는 커밋을 고정한다. 현재 원격 기본 ref는 `main`이므로 `SWEET_REF`로 불변 SHA/태그를 명시할 수 있다.

Apache-2.0, NOTICE, 기존 MIT 고지를 유지한다. 현재 모체 코드를 예전 MIT-only 상태로 되돌리지 않는다.

## 기존 작업 보존과 복구

백업 디렉터리: `/Users/im_1767/Desktop/Dev/sweet-agent-backup-20261110-093715`.

- `repository.bundle`: 브랜치·태그를 포함한 Git 백업.
- `committed.patch`: 기존 포크 커밋 차이.
- `unstaged.patch`, `staged.patch`, `untracked/`: 기존 작업 사본.
- 백업 브랜치: `backup/pre-upstream-20261110-093715`.
- stash: `pre-upstream migration 2026-11-10`. 자동 삭제하지 않음.

기존 `.agents/`, `skills-lock.json`, 한국어 벤치마크 프롬프트는 작업 트리에 복원했다. `benchmarks/run.py`는 모체의 새 간결 지시 비교군과 통계 로직을 유지하면서 사용자의 한국어 레이블·thinking 비활성화·선택 모델을 재적용했다. 원래 파일 그대로의 사본은 stash와 백업에 남아 있다. 사용자 작업은 포크 이식 커밋에 포함하지 않는다.

이전 상태는 `git switch main`으로 확인할 수 있다. 작업 트리 변경이 있으면 먼저 별도 보존한다. 백업 stash를 현재 브랜치에 무조건 다시 적용하면 이미 복원된 작업과 충돌할 수 있다.

## 이식 검증 결과

- `npm test`: 728개 중 699개 통과, 29개 환경별 검사 생략, 실패 0.
- 압축 Python 테스트 128개, 파일 판별 12개, 인라인 검증 34개, 포크 부정문 보존 6개 통과.
- 추가 파서·언어·규칙 전달 Node 검사 통과.
- `python3 tests/verify_repo.py`: 배포본·라이선스·체크섬·패키지·훅 설치/제거 등 전체 로컬 검사 통과.
- `git diff --check` 통과.
- 실제 모델 호출, 사용자 전역 설치, 수정 CLI의 빌드·게시, Windows 실기 실행은 수행하지 않음.
- `.agents/`는 사용자 로컬 설치 사본이라 인코딩 소스 검사에서 제외. 배포하는 정본과 플러그인 복사본은 별도로 검증.

## 영어 지시문과 공개 브랜드

지시문·스킬 설명·훅 폴백·에이전트 지시·명령 템플릿은 영어로 유지한다. 사용자에게 하는 답변은 한국어 반말이며, `오빠` 호칭은 응답당 최대 한 번이다. 한국어 실행 문구, 말투 예시, 에이전트 결과 표식과 코드의 한국어 감지 정규식은 그대로 둔다. 문서 파일 압축은 채팅 언어 규칙과 별개로 원문 언어를 보존한다.

공개 플러그인 이름은 Claude/Gemini/Cursor/Codex/OMP/Antigravity 모두 `sweet-agent`다. Claude 마켓플레이스 설치·갱신 대상은 `sweet-agent@sweet-agent`이며 `/sweet-agent:sweet` 네임스페이스도 처리한다. OpenCode 설치 플러그인 디렉터리는 `plugins/sweet-agent`다. 상태 배지는 `SWEET` 또는 `SWEET:ULTRA`를 쓴다.

npm 진입점 `sweet-agent`와 `sweet`는 모두 `installer/sweet.js`를 실행한다. 래퍼는 부작용 없이 내보낸 설치기의 `main()`을 호출하고 종료 코드를 전달한다. 원래 `installer/install.js` 직접 실행도 지원한다.

제작자·저장소·지원 안내와 Codex 아이콘은 포크를 가리킨다. 원본 저작권·라이선스, 실제 모체 npm 패키지와 서비스 주소, 내부 파일·환경 변수·상태 경로는 호환성을 위해 유지한다. 사용자 전역 설치나 로컬 `.agents` 사본을 자동으로 바꾸지는 않는다.

로컬 체크아웃이나 npm 패키지의 설치기는 포함된 스킬·플러그인·Gemini 확장을 우선 설치한다. 사용자 로컬 `.agents` 사본이 섞이지 않게 범용 스킬 CLI에는 정본 `skills/` 디렉터리만 전달한다. 소스 없는 분리형 설치만 `REPO#SWEET_REF`를 내려받으므로, 브랜치 게시 후에도 원격 main의 옛 버전으로 돌아가지 않는다. 원격 분리형 설치를 직접 사용할 때는 `SWEET_REF=migration/upstream-4.1.0`을 명시한다.

공통 스킬 검증기는 12개를 통과했다. `ultracave`·`megacave`의 기존 Claude 전용 `disable-model-invocation` 필드는 공통 검증기 지원 범위 밖이라 그대로 보존했으며, 실제 저장소 스킬 컴파일과 호스트 테스트로 검증했다. 실제 모델 호출에 의한 말투 동등성, Windows 실기, 사용자 전역 설치는 수행하지 않았다.

## 영어 전환·브랜드 통합 검증 결과

- 설치기·훅 Node 테스트: 736개 중 707개 통과, 환경별 29개 생략, 실패 0.
- 압축·파일 판별·인라인/부정문 검증 Python 180개, 모드 추적 Python 44개 통과.
- 실제 로컬 소스·Gemini·공개 명령 진입점 추가 검사 80개 통과.
- 독립 파서·언어·규칙 전달 검사와 저장소 전체 `verify_repo.py`, `git diff --check` 통과.
- 스킬 토큰 측정은 `docs/sweet-instruction-tokens.json`에 기준 커밋·파일 해시와 함께 기록. `/sweet` 스킬은 o200k_base 기준 1,976→813토큰(58.9% 감소). 영어화와 중복 정리의 합계이며 실제 세션 사용량은 아니다.
- 사용자 벤치마크·잠금 파일·로컬 `.agents` 사본은 변경/커밋하지 않음.
