## Note on Korean (Hangeul) Compression

Hangul is a phonetic alphabet — it cannot match the inherent compression
of logographic scripts like Classical Chinese (wenyan).

Hangeul-ultra achieves ~55-65% savings vs normal Korean, NOT vs English ultra.
For maximum token efficiency, English ultra mode remains best.

Hangeul modes exist for Korean-speaking developers who:
- Ask questions in Korean and prefer Korean responses
- Need terse, technical answers without fluff
- Want caveman's speed/cost benefits in their native language

The 6% gap between hangeul-ultra and English ultra is the cost of
native-language UX. Korean has its own removable fluff:
honorific endings (~합니다/~습니다), particle chains (은/는/이/가/을/를),
filler hedging (~것 같습니다), and connective padding (때문에, 그리고).
