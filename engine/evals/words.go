package evals

import "unicode"

// CountWords is the tokenizer-independent denominator for eval reporting.
// It treats contiguous letters/digits as one word and ignores punctuation.
func CountWords(b []byte) int {
	var n int
	inWord := false
	for _, r := range string(b) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if !inWord {
				n++
				inWord = true
			}
			continue
		}
		inWord = false
	}
	return n
}

func ratio(before, after int) float64 {
	if before <= 0 || after >= before {
		return 0
	}
	return float64(before-after) / float64(before)
}
