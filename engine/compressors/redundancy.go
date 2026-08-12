package compressors

import (
	"bytes"
	"unicode"
)

// keepNonRedundant guarantees that every distinct kind of content in a payload
// survives an elision at least once. A unit about to be dropped is kept when no
// unit already surviving resembles it; its later copies still elide against it.
// It runs in every compressor that drops units, and it is the guard that
// separates *eliding repetition* from *truncating a document*.
//
// Why it exists. An elider keeps the first/last few units plus whatever carries a
// signal, and drops the rest. On a payload dominated by repeated boilerplate, the
// dropped units are represented by the ones kept, so the model can still answer
// and the reduction is real. On a payload where every unit is distinct — a
// bibliography, a source file, a design doc — the same transform silently deletes
// most of the answer.
//
// A distinct document can lose the answer when this rule is absent: the model
// must recover it repeatedly, erasing any token reduction. Repeated boilerplate
// is different: every dropped unit is represented by a surviving unit, so the
// reduction is useful without truncating distinct records. That distinction is
// what this guard encodes.
//
// Keeping one representative rather than refusing outright is what makes the rule
// useful rather than merely safe: a payload that is half boilerplate and half
// distinct records still gets its boilerplate elided, and an incident report whose
// eighteen background paragraphs sit in the middle keeps one of them instead of
// losing the lot. When so much is distinct that nothing is left to drop, the
// compressor's own "output is not smaller" check passes the original through and
// claims nothing — the honest zero.
//
// It is deterministic — a pure function of the unit bytes, in document order —
// because a compressed block must re-serialize identically on every later turn or
// the provider prefix cache is busted.
func keepNonRedundant(units [][]byte, keep []bool) {
	if len(units) != len(keep) {
		return
	}
	kept := make([]int, 0, len(units))
	for i, k := range keep {
		if k {
			kept = append(kept, i)
		}
	}
	// Seed the represented classes from a bounded, evenly spread sample of what
	// the compressor already decided to keep, so the gate costs the same on a
	// 200-line payload and a 20 000-line one and still sees the whole document
	// rather than only its head. Sampling can only ever promote an extra unit
	// that a kept twin would have covered, which errs toward keeping data.
	classes := make([]profile, 0, 2*redundancyMaxClasses)
	step := 1
	if len(kept) > redundancyMaxClasses {
		step = len(kept) / redundancyMaxClasses
	}
	for n := 0; n < len(kept) && len(classes) < redundancyMaxClasses; n += step {
		classes = append(classes, unitVocabulary(units[kept[n]]))
	}

	promotions := 0
	for i := range units {
		if keep[i] {
			continue
		}
		if promotions >= redundancyMaxPromotions {
			// This payload has turned up more distinct kinds of content than any
			// repetitive stream plausibly has, so it is a document. Keep the rest;
			// the compressor's own "output is not smaller" check then passes the
			// original through and claims nothing. Prefer the honest zero.
			keep[i] = true
			continue
		}
		vocab := unitVocabulary(units[i])
		if representedBy(vocab, classes) {
			continue
		}
		// First of its kind: keep this one so the class is visible at all, and
		// let its later copies elide against it.
		keep[i] = true
		promotions++
		classes = append(classes, vocab)
	}
}

// docsAsUnits adapts the canonical per-element strings the array-shaped eliders
// already build into the unit slice keepNonRedundant takes.
func docsAsUnits(docs []string) [][]byte {
	units := make([][]byte, len(docs))
	for i, doc := range docs {
		units[i] = []byte(doc)
	}
	return units
}

const (
	// redundantUnitContainment is the share of a dropped unit's vocabulary a
	// single surviving unit must already carry for that unit to count as
	// represented. The threshold is deliberately conservative: a missed elision
	// costs reduction, while an incorrect one changes the answer. Being strict is
	// cheap here because the compressor keeps one representative per class rather
	// than refusing the whole payload.
	//
	// Being strict is cheap here precisely because this keeps one representative
	// per class rather than refusing outright — a stream with several varying
	// fields simply retains a handful more representatives, not its whole body.
	redundantUnitContainment = 0.9
	// redundancyMaxClasses bounds how many of the already-kept units seed the
	// class list, which bounds the work done per dropped unit.
	redundancyMaxClasses = 64
	// redundancyMaxPromotions bounds how many first-of-their-kind units are kept
	// before the payload is declared a document and the rest kept wholesale. It
	// counts only promotions, never the seed: counting both let a single
	// promotion trip the limit on any payload with more than 64 kept units, which
	// silently turned every large array into a pass-through.
	redundancyMaxPromotions = 128
	// redundancyMaxUnitTokens bounds the vocabulary built per unit, so one
	// pathologically long line cannot make the gate quadratic.
	redundancyMaxUnitTokens = 128
	// redundancyMinWordTokens is how many letter-bearing tokens a unit needs
	// before digit masking leaves anything behind to compare. A unit with none at
	// all masks down to punctuation and "#", which is not evidence of anything —
	// there, the numbers ARE the content, so it is compared with its digits
	// intact. One word is enough: `replicas: 2` and `replicas: 3` are the same
	// config line, while `['1973.', 251]` and `['1974.', 260.5]` are two
	// observations and must not collapse into each other.
	redundancyMinWordTokens = 1
)

// profile is a unit reduced to the vocabulary it is judged by, plus whether that
// vocabulary had its digits masked. Two units are only ever compared like for
// like: a masked profile says "same shape, different numbers", an unmasked one
// says "same text, same numbers", and reading one as the other is what let a table
// of measurements pass for a repeated line.
type profile struct {
	vocab  map[string]struct{}
	masked bool
}

func representedBy(dropped profile, classes []profile) bool {
	if len(dropped.vocab) == 0 {
		return true // an empty or punctuation-only unit carries nothing to lose
	}
	need := int(redundantUnitContainment*float64(len(dropped.vocab)) + 0.999999)
	for _, class := range classes {
		if class.masked != dropped.masked {
			continue
		}
		hit := 0
		for token := range dropped.vocab {
			if _, ok := class.vocab[token]; ok {
				hit++
				if hit >= need {
					return true
				}
			}
		}
	}
	return false
}

// unitVocabulary reduces a unit to the set of words it uses, with every run of
// digits collapsed to a single "#". Masking digits is what lets two log lines
// that differ only in a timestamp, or two CSV rows that differ only in a row id,
// recognise each other as the same content — while leaving the words themselves
// intact, so two bibliography entries that share only their field names do not.
//
// Masking is only applied to units that carry enough words to still be
// distinguishable without their numbers. A unit like `6 ['1973.', 251]` masks
// down to nothing but "#", which would make every row of a data table look like
// every other and let the whole table elide. Those units keep their digits, so
// they only match a unit carrying the same values.
func unitVocabulary(unit []byte) profile {
	vocab, words := tokenize(unit, true)
	if words >= redundancyMinWordTokens {
		return profile{vocab: vocab, masked: true}
	}
	unmasked, _ := tokenize(unit, false)
	return profile{vocab: unmasked, masked: false}
}

// tokenize splits a unit into its word/number tokens, optionally collapsing each
// run of digits to "#", and reports how many tokens carry a letter.
func tokenize(unit []byte, maskDigits bool) (map[string]struct{}, int) {
	vocab := make(map[string]struct{})
	words := 0
	var token []rune
	hasLetter := false
	inDigits := false
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
		hasLetter = false
		inDigits = false
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
			hasLetter = true
			inDigits = false
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
