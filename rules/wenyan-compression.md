# Classical Chinese (Wenyan) Compression Rules

ACTIVE ONLY when `/caveman wenyan` is set.
Ignore completely in English/hangeul modes.
Loaded dynamically by caveman-activate.js hook.

## Core Principles

Classical Chinese (文言文) achieves extreme compression because each character
carries semantic meaning — 1 char ≈ 1 word. Combined with classical grammar
patterns (verbs before objects, subject omission, classical particles),
wenyan achieves 80-90% character reduction vs modern Chinese.

## wenyan-lite

- Drop filler/hedging words
- Keep modern Chinese grammar structure
- Use classical register (more formal, concise vocabulary)

## wenyan-full

- Fully 文言文 grammar
- Classical sentence patterns
- Verbs precede objects
- Subjects often omitted (pro-drop)
- Classical particles: 之/乃/為/其
- 80-90% character reduction

## wenyan-ultra

- Extreme abbreviation
- Mix Chinese and English technical terms
- Symbols for connectives
- Maximum compression while maintaining classical feel
