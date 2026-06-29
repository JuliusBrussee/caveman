# caveman-th

Thai caveman mode. Same technical substance, fewer Thai filler words.

## What it does

Compresses Thai replies into short, developer-friendly fragments while preserving English technical terms, code identifiers, paths, commands, API names, and exact error text. Use it when you want Thai output that is shorter than normal Thai prose without losing technical precision.

The base `caveman` skill already preserves the user's language. `caveman-th` adds Thai-specific wording rules and examples for people who explicitly want compressed Thai.

Three intensity levels:

| Level | What change |
|-------|-------------|
| `lite` | Remove filler. Thai sentences still read smoothly. |
| `full` | Default. Shorter fragments, English technical terms preserved. |
| `ultra` | Tightest form. Arrows and developer shorthand where clear. |

## How to invoke

```text
caveman-th
caveman-th lite
caveman-th ultra
stop caveman-th
```

Also triggers on "Thai caveman", "ตอบไทยแบบสั้น", "พูดไทยแบบ caveman", "น้อยคำ", or "ประหยัด token".

## Example output

Question: "ทำไม React component re-render ตลอด?"

Normal Thai:
> Component re-render เพราะมีการสร้าง object reference ใหม่ทุกครั้งที่ render ควรใช้ `useMemo` เพื่อคง reference เดิมไว้

caveman-th full:
> Object ref ใหม่ทุก render -> re-render. Inline prop ทำ shallow compare fail. ใช้ `useMemo`.

caveman-th ultra:
> Inline object prop -> new ref -> re-render. `useMemo`.

## See also

- [`SKILL.md`](./SKILL.md) - full LLM-facing instructions
- [Caveman README](../../README.md) - repo overview, install, benchmarks
