<h1 align="center">sweet-compress</h1>

<p align="center">
  <strong>메모리 파일을 줄여서 매 세션 토큰을 아낀다</strong>
</p>

---

프로젝트 메모리 파일(`CLAUDE.md`, 할 일, 설정 메모)을 압축본으로 다시 쓰는 Claude Code 스킬. 이후 모든 세션이 더 적은 토큰으로 시작한다.

Claude 는 세션 시작할 때마다 `CLAUDE.md` 를 읽는다. 파일이 크면 매번 비용이 든다. 파일을 줄여두면 그 비용이 계속 줄어든다.

## 뭐 하나

```
/sweet-compress CLAUDE.md
```

```
CLAUDE.md          ← 압축본 (Claude 가 읽는 파일 — 매 세션 토큰 절약)
CLAUDE.original.md ← 사람이 읽는 백업 (당신이 편집하는 파일)
```

원본은 절대 안 없어진다. `.original.md` 를 읽고 편집할 수 있다. 편집한 뒤 스킬을 다시 돌리면 재압축된다.

## 언어

파일의 주 언어를 따라간다. 한국어 파일은 한국어로, 영어 파일은 영어로 압축한다. **번역하지 않는다.**

- 한국어: 조사, 군더더기(그냥/진짜/좀), 상투어("~하시면 됩니다"), 완충 표현(아마/~인 것 같습니다)을 버리고 개조식으로 만든다.
- 영어: articles, filler, pleasantries, hedging 을 버린다.

## 압축률

**한국어는 아직 측정 안 했다.**

상류 [caveman](https://github.com/JuliusBrussee/caveman) 프로젝트가 **영어** 파일로 측정한 값은 5개 파일 평균 46% (범위 36.9–59.6%) 였다. 측정에 쓴 파일은 이 레포의 [`tests/sweet-compress/`](../../tests/sweet-compress/) 에 그대로 있다. 다만 그 숫자는 영어 산문을 영어 압축 규칙으로 줄인 결과다. 한국어 파일에 그대로 적용되지 않으므로 이 프로젝트의 수치로 쓰지 않는다.

직접 재려면 한국어 메모리 파일로 압축 전후 토큰 수를 세면 된다. 자세한 내용은 [docs/HONEST-NUMBERS.md](../../docs/HONEST-NUMBERS.md).

## Before / After

<table>
<tr>
<td width="50%">

### 📄 원본

> "새로 작성하는 모든 코드에는 strict mode 를 켠 TypeScript 를 사용하는 것을 강력히 선호합니다. 정말로 다른 방법이 없는 경우를 제외하면 `any` 타입은 쓰지 말아 주시고, 만약 쓰게 된다면 그 이유를 설명하는 주석을 남겨 주세요. 타입을 제대로 잡는 데 시간을 들이면 런타임까지 가기 전에 많은 버그를 잡을 수 있다고 생각합니다."

</td>
<td width="50%">

### 🍬 압축본

> "새 코드는 TypeScript strict mode. `any` 는 불가피할 때만 — 쓰면 이유 주석. 타입 제대로 잡으면 런타임 전에 버그 잡힘."

</td>
</tr>
</table>

같은 지시. 훨씬 짧음. 세션마다 반복 절약.

## 보안

`sweet-compress` 는 정적 분석이 서브프로세스와 파일 I/O 패턴을 잡아내서 Snyk High Risk 로 표시된다. 오탐이다 — 이 스킬이 무엇을 하고 무엇을 안 하는지는 [SECURITY.md](./SECURITY.md) 에 정리돼 있다.

## 설치

`sweet` 를 설치하면 compress 도 같이 들어온다. 설치 후 `/sweet-compress` 를 쓰면 된다.

로컬 파일이 필요하면 스킬은 여기 있다:

```bash
skills/sweet-compress/
```

**필요:** Python 3.10+

## 사용법

```
/sweet-compress <파일경로>
```

예:
```
/sweet-compress CLAUDE.md
/sweet-compress docs/preferences.md
/sweet-compress todos.md
```

### 어떤 파일이 되나

| 종류 | 압축? |
|------|-----------|
| `.md`, `.txt`, `.rst`, `.typ`, `.typst`, `.tex` | ✅ 됨 |
| 확장자 없는 자연어 파일 | ✅ 됨 |
| `.py`, `.js`, `.ts`, `.json`, `.yaml` | ❌ 건너뜀 (코드/설정) |
| `*.original.md` | ❌ 건너뜀 (백업 파일) |

## 어떻게 동작하나

```
/sweet-compress CLAUDE.md
        ↓
파일 종류 판별            (토큰 안 씀)
        ↓
Claude 가 압축             (토큰 — 호출 1회)
        ↓
출력 검증                 (토큰 안 씀)
  확인: 제목, 코드 블록, URL, 파일 경로, 불릿
        ↓
오류 있으면 Claude 가 해당 부분만 수정   (토큰 — 국소 수정)
  재압축 안 함 — 깨진 부분만 패치
        ↓
최대 2회 재시도
        ↓
압축본 → CLAUDE.md
원본   → CLAUDE.original.md
```

토큰을 쓰는 건 두 곳뿐이다: 최초 압축 + 검증 실패 시 국소 수정. 나머지는 전부 로컬 Python.

## 절대 안 건드리는 것

자연어만 압축한다. 다음은 손대지 않는다:

- 코드 블록 (` ``` ` 펜스 또는 들여쓰기)
- 인라인 코드 (`` `백틱 내용` ``)
- URL 과 링크
- 파일 경로 (`/src/components/...`)
- 명령어 (`npm install`, `git commit`)
- 기술 용어, 라이브러리 이름, API 이름
- 제목 (텍스트 그대로 보존)
- 표 (구조 보존, 셀 텍스트만 압축)
- 날짜, 버전 번호, 숫자 값

## 왜 필요한가

`CLAUDE.md` 는 **세션 시작마다** 로드된다. 1000 토큰짜리 프로젝트 메모리 파일은 프로젝트를 열 때마다 그만큼 비용이 든다. 100번 세션이면 100,000 토큰이 이미 당신이 써둔 컨텍스트를 다시 읽는 데만 쓰인다.

압축은 그 반복 비용을 줄인다. 같은 지시, 같은 정확도, 낭비만 제거.

## sweet-agent 의 일부

이 스킬은 [sweet-agent](https://github.com/HDomi/sweet-agent) 의 일부다 — 정확도를 잃지 않으면서 토큰을 덜 쓰게 만드는 도구 모음.

- **sweet** — Claude 가 *말을* 짧게 하게 (출력 토큰)
- **sweet-compress** — Claude 가 *읽는 양*을 줄이게 (컨텍스트 토큰)
