package compressors

import (
	"bytes"
	"maps"
	"math/rand/v2"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// Keep the pre-byte-buffer implementation as an oracle: changing vocabulary
// changes the distinct-content guard, even if a repetitive benchmark is faster.
func runeTokenizeOracle(unit []byte, maskDigits bool) (map[string]struct{}, int) {
	vocab := make(map[string]struct{})
	words := 0
	var token []rune
	hasLetter, inDigits := false, false
	flush := func() {
		if len(token) > 0 {
			if _, seen := vocab[string(token)]; !seen {
				vocab[string(token)] = struct{}{}
				if hasLetter {
					words++
				}
			}
		}
		token = token[:0]
		hasLetter, inDigits = false, false
	}
	for _, r := range string(bytes.ToLower(unit)) {
		switch {
		case unicode.IsDigit(r):
			if maskDigits {
				if !inDigits {
					token = append(token, '#')
					inDigits = true
				}
			} else {
				token = append(token, r)
			}
		case unicode.IsLetter(r) || r == '_':
			token = append(token, r)
			hasLetter, inDigits = true, false
		default:
			flush()
		}
		if len(vocab) >= redundancyMaxUnitTokens {
			return vocab, words
		}
	}
	flush()
	return vocab, words
}

func TestTokenVocabularyMatchesRuneOracle(t *testing.T) {
	check := func(input []byte) {
		t.Helper()
		for _, masked := range []bool{false, true} {
			want, wantWords := runeTokenizeOracle(input, masked)
			got, gotWords := tokenize(input, masked)
			if !maps.Equal(got, want) || gotWords != wantWords {
				t.Fatalf("vocabulary changed for %q, masked=%v: got %v/%d, want %v/%d", input, masked, got, gotWords, want, wantWords)
			}
		}
	}
	for _, input := range []string{
		"", "a12b٣٤c １２３ café CAFÉ _ __ x_9", "6 ['1973.', 251]",
		"İıſK Σςσ Straße STRASSE 𝟘𝟙", strings.Repeat("界A9", 5000),
		"A\xffB\xc0\x80 \x00_42 \xed\xa0\x80", strings.Repeat("token ", 200),
	} {
		check([]byte(input))
	}
	// All scalar values, including case folds and non-ASCII digit categories.
	// Short groups avoid the intentional 128-token limit hiding later runes.
	for start := rune(0); start <= unicode.MaxRune; start += 64 {
		var input []byte
		for r := start; r < start+64 && r <= unicode.MaxRune; r++ {
			input = utf8.AppendRune(input, r)
			input = append(input, '9', '_', ' ')
		}
		check(input)
	}
	random := rand.New(rand.NewPCG(29, 91))
	for range 500 {
		input := make([]byte, random.IntN(4096))
		for i := range input {
			input[i] = byte(random.Uint32())
		}
		check(input)
	}
}
