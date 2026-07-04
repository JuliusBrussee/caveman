# Caveman Benchmark Report — All Intensity Levels

**Date:** 2026-06-12
**Models:** deepseek-v4-flash, deepseek-v4-pro
**Method:** 10 prompts × 2 modes (normal vs caveman) × 3 trials = 60 API calls per level
**Runner:** `benchmarks/run.py`

## Summary

| Mode | deepseek-v4-flash | deepseek-v4-pro | Delta |
|------|------------------|-----------------|-------|
| `lite` | 76% | 77% | +1pp |
| `full` | — | 76% | (first DeepSeek run) |
| `ultra` | 73% | 79% | +6pp |
| `wenyan-lite` | 72% | 77% | +5pp |
| `wenyan-full` | 67% | 76% | +9pp |
| `wenyan-ultra` | 72% | 76% | +4pp |

- **deepseek-v4-pro** consistently outperforms v4-flash across all modes (+1–9pp).
- **Wenyan modes see the biggest gains**: v4-pro is significantly better at classical Chinese compression.
- **6-mode average**: v4-pro 76.8%, v4-flash 72.0%.
- **`ultra` mode wins at 79%** on v4-pro — maximum token compression.

## Per-task breakdown — deepseek-v4-pro

### lite (77%)

| Task | Normal | Caveman | Saved |
|------|--------|---------|-------|
| Explain React re-render bug | 488 | 31 | 94% |
| Fix auth middleware token expiry | 442 | 36 | 92% |
| Set up PostgreSQL connection pool | 2060 | 289 | 86% |
| Explain git rebase vs merge | 524 | 287 | 45% |
| Refactor callback to async/await | 248 | 77 | 69% |
| Architecture: microservices vs monolith | 475 | 239 | 50% |
| Review PR for security issues | 542 | 73 | 87% |
| Docker multi-stage build | 777 | 166 | 79% |
| Debug PostgreSQL race condition | 754 | 173 | 77% |
| Implement React error boundary | 2644 | 251 | 91% |

### full (76%)

| Task | Normal | Caveman | Saved |
|------|--------|---------|-------|
| Explain React re-render bug | 517 | 45 | 91% |
| Fix auth middleware token expiry | 470 | 89 | 81% |
| Set up PostgreSQL connection pool | 1715 | 333 | 81% |
| Explain git rebase vs merge | 551 | 178 | 68% |
| Refactor callback to async/await | 266 | 77 | 71% |
| Architecture: microservices vs monolith | 501 | 258 | 49% |
| Review PR for security issues | 554 | 95 | 83% |
| Docker multi-stage build | 837 | 248 | 70% |
| Debug PostgreSQL race condition | 794 | 139 | 82% |
| Implement React error boundary | 2708 | 315 | 88% |

### ultra (79%)

| Task | Normal | Caveman | Saved |
|------|--------|---------|-------|
| Explain React re-render bug | 500 | 31 | 94% |
| Fix auth middleware token expiry | 476 | 46 | 90% |
| Set up PostgreSQL connection pool | 1895 | 253 | 87% |
| Explain git rebase vs merge | 557 | 195 | 65% |
| Refactor callback to async/await | 269 | 67 | 75% |
| Architecture: microservices vs monolith | 648 | 258 | 60% |
| Review PR for security issues | 532 | 73 | 86% |
| Docker multi-stage build | 855 | 223 | 74% |
| Debug PostgreSQL race condition | 720 | 227 | 68% |
| Implement React error boundary | 2586 | 243 | 91% |

### wenyan-lite (77%)

| Task | Normal | Caveman | Saved |
|------|--------|---------|-------|
| Explain React re-render bug | 511 | 32 | 94% |
| Fix auth middleware token expiry | 444 | 91 | 80% |
| Set up PostgreSQL connection pool | 1898 | 253 | 87% |
| Explain git rebase vs merge | 530 | 181 | 66% |
| Refactor callback to async/await | 235 | 100 | 57% |
| Architecture: microservices vs monolith | 593 | 242 | 59% |
| Review PR for security issues | 521 | 88 | 83% |
| Docker multi-stage build | 780 | 217 | 72% |
| Debug PostgreSQL race condition | 799 | 117 | 85% |
| Implement React error boundary | 2648 | 319 | 88% |

### wenyan-full (76%)

| Task | Normal | Caveman | Saved |
|------|--------|---------|-------|
| Explain React re-render bug | 488 | 42 | 91% |
| Fix auth middleware token expiry | 414 | 81 | 80% |
| Set up PostgreSQL connection pool | 1803 | 354 | 80% |
| Explain git rebase vs merge | 680 | 215 | 68% |
| Refactor callback to async/await | 238 | 85 | 64% |
| Architecture: microservices vs monolith | 442 | 247 | 44% |
| Review PR for security issues | 544 | 83 | 85% |
| Docker multi-stage build | 782 | 215 | 73% |
| Debug PostgreSQL race condition | 710 | 123 | 83% |
| Implement React error boundary | 2573 | 314 | 88% |

### wenyan-ultra (76%)

| Task | Normal | Caveman | Saved |
|------|--------|---------|-------|
| Explain React re-render bug | 506 | 22 | 96% |
| Fix auth middleware token expiry | 438 | 76 | 83% |
| Set up PostgreSQL connection pool | 1818 | 267 | 85% |
| Explain git rebase vs merge | 509 | 213 | 58% |
| Refactor callback to async/await | 272 | 71 | 74% |
| Architecture: microservices vs monolith | 487 | 302 | 38% |
| Review PR for security issues | 508 | 75 | 85% |
| Docker multi-stage build | 762 | 220 | 71% |
| Debug PostgreSQL race condition | 727 | 149 | 80% |
| Implement React error boundary | 2451 | 257 | 90% |

## Per-task breakdown — deepseek-v4-flash

### lite (76%)

| Task | Normal | Caveman | Saved |
|------|--------|---------|-------|
| Explain React re-render bug | 449 | 53 | 88% |
| Fix auth middleware token expiry | 461 | 92 | 80% |
| Set up PostgreSQL connection pool | 2046 | 288 | 86% |
| Explain git rebase vs merge | 752 | 189 | 75% |
| Refactor callback to async/await | 249 | 104 | 58% |
| Architecture: microservices vs monolith | 1034 | 251 | 76% |
| Review PR for security issues | 399 | 99 | 75% |
| Docker multi-stage build | 838 | 325 | 61% |
| Debug PostgreSQL race condition | 796 | 117 | 85% |
| Implement React error boundary | 1581 | 420 | 73% |

### ultra (73%)

| Task | Normal | Caveman | Saved |
|------|--------|---------|-------|
| Explain React re-render bug | 489 | 109 | 78% |
| Fix auth middleware token expiry | 511 | 80 | 84% |
| Set up PostgreSQL connection pool | 2076 | 238 | 89% |
| Explain git rebase vs merge | 757 | 282 | 63% |
| Refactor callback to async/await | 248 | 113 | 54% |
| Architecture: microservices vs monolith | 950 | 255 | 73% |
| Review PR for security issues | 383 | 106 | 72% |
| Docker multi-stage build | 1001 | 414 | 59% |
| Debug PostgreSQL race condition | 810 | 159 | 80% |
| Implement React error boundary | 1660 | 380 | 77% |

### wenyan-lite (72%)

| Task | Normal | Caveman | Saved |
|------|--------|---------|-------|
| Explain React re-render bug | 505 | 84 | 83% |
| Fix auth middleware token expiry | 416 | 83 | 80% |
| Set up PostgreSQL connection pool | 2000 | 324 | 84% |
| Explain git rebase vs merge | 749 | 257 | 66% |
| Refactor callback to async/await | 242 | 101 | 58% |
| Architecture: microservices vs monolith | 578 | 232 | 60% |
| Review PR for security issues | 377 | 104 | 72% |
| Docker multi-stage build | 844 | 364 | 57% |
| Debug PostgreSQL race condition | 771 | 173 | 78% |
| Implement React error boundary | 1652 | 343 | 79% |

### wenyan-full (67%)

| Task | Normal | Caveman | Saved |
|------|--------|---------|-------|
| Explain React re-render bug | 469 | 86 | 82% |
| Fix auth middleware token expiry | 440 | 86 | 80% |
| Set up PostgreSQL connection pool | 2155 | 327 | 85% |
| Explain git rebase vs merge | 663 | 381 | 43% |
| Refactor callback to async/await | 241 | 110 | 54% |
| Architecture: microservices vs monolith | 571 | 311 | 46% |
| Review PR for security issues | 390 | 101 | 74% |
| Docker multi-stage build | 838 | 513 | 39% |
| Debug PostgreSQL race condition | 876 | 113 | 87% |
| Implement React error boundary | 1598 | 340 | 79% |

### wenyan-ultra (72%)

| Task | Normal | Caveman | Saved |
|------|--------|---------|-------|
| Explain React re-render bug | 480 | 79 | 84% |
| Fix auth middleware token expiry | 448 | 86 | 81% |
| Set up PostgreSQL connection pool | 2016 | 467 | 77% |
| Explain git rebase vs merge | 668 | 288 | 57% |
| Refactor callback to async/await | 241 | 73 | 70% |
| Architecture: microservices vs monolith | 1126 | 270 | 76% |
| Review PR for security issues | 428 | 102 | 76% |
| Docker multi-stage build | 811 | 346 | 57% |
| Debug PostgreSQL race condition | 815 | 263 | 68% |
| Implement React error boundary | 1489 | 392 | 74% |

## Raw data files

### deepseek-v4-pro
- `benchmark_20260612_063333.json` — lite
- `benchmark_20260612_063410.json` — full
- `benchmark_20260612_063443.json` — ultra
- `benchmark_20260612_063631.json` — wenyan-lite
- `benchmark_20260612_063734.json` — wenyan-full
- `benchmark_20260612_063811.json` — wenyan-ultra

### deepseek-v4-flash
- `benchmark_20260611_192846.json` — lite
- `benchmark_20260611_191748.json` — ultra
- `benchmark_20260611_193607.json` — wenyan-lite
- `benchmark_20260611_193957.json` — wenyan-full
- `benchmark_20260611_194114.json` — wenyan-ultra

## Code changes

### `benchmarks/run.py`
- `--mode` argument — injects target intensity level into system prompt
- `--base-url` / `--api-key` — support non-Anthropic endpoints
- `thinking: {"type": "disabled"}` — exclude reasoning tokens from output count
- Fix text block extraction (join all `type: "text"` blocks)

### `src/hooks/caveman-stats.js`
- `COMPRESSION` table populated for all 6 intensity modes
- `Mode` line added to stats output, showing current active mode
- Updated fallback message listing benchmarked modes

### `tests/test_caveman_stats.js`
- Tests adapted for ultra/lite mode data and new Mode line output

## Caveats

1. Normal baseline varies between runs (different API calls, slight model non-determinism). Within-run comparisons are fair; cross-run comparisons have ~2–3% variance.
2. `full` mode for deepseek-v4-flash was not benchmarked. The original 65% figure came from `claude-sonnet-4-20250514` (Anthropic API).
3. `thinking` was disabled (`{"type": "disabled"}`) but models may still emit internal tokens counted in `output_tokens`.
4. v4-pro consistently achieves higher compression than v4-flash across all modes, especially in wenyan modes (+4–9pp).
