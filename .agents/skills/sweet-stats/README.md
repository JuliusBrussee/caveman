# sweet-stats

세션 토큰 실측. AI 추정 없음.

## 뭐 하나

현재 Claude Code 세션 로그를 직접 읽어서 실제 입력·출력 토큰 사용량을 보고한다. 숫자는 디스크에 있는 JSONL 세션 로그에서 나온다 — 모델이 계산하거나 추정하지 않는다. 출력은 `sweet-mode-tracker` 훅이 `/sweet-stats` 를 가로채서 차단 결정(reason)으로 되돌려준다.

**절감 추정치는 기본적으로 안 나온다.** 한국어 압축률을 측정한 값이 아직 없기 때문이다. 추측한 숫자를 보여주지 않는다. 직접 측정한 값이 있으면 알려주면 된다:

```bash
export SWEET_COMPRESSION_RATIOS='{"full":0.65}'   # 직접 측정한 값으로
```

측정 방법은 [docs/HONEST-NUMBERS.md](../../docs/HONEST-NUMBERS.md) 에 있다.

압축률이 설정된 상태로 실행하면 상태 표시줄 배지가 쓰는 누적 절감 파일(`⛏ 12.4k`)도 갱신한다.

## 어떻게 켜나

```
/sweet-stats
```

플래그: `--share` (공유용 한 줄), `--all` (누적), `--since 7d`.

## 출력 예시

압축률 미설정 (기본):

```
Turns:                 47
Output tokens:         3,891
Cache-read tokens:     12,304
No savings estimate — no mode has a committed benchmark yet.
```

압축률 설정 후:

```
Turns:                 47
Output tokens:         3,891
Est. without sweet:    11,117
Est. tokens saved:     7,226 (~65% of output)
```

## 같이 보기

- [`SKILL.md`](./SKILL.md) — 훅 계약과 동작 방식
- [docs/HONEST-NUMBERS.md](../../docs/HONEST-NUMBERS.md) — 숫자에 대해
- [레포 README](../../README.md) — 개요
