package mem

import (
	"math"
	"strings"
	"unicode"
)

// BM25 parameters (Robertson/Spärck Jones defaults).
const (
	bm25K1 = 1.5
	bm25B  = 0.75
)

// tokenize lowercases and splits on any non-alphanumeric rune. It is
// deterministic — the same text always yields the same tokens — which keeps
// recall reproducible.
func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// stopwords are common function words that carry no selection signal. Dropping
// them keeps recall conservative: a query like "where is the deploy key" matches
// on "deploy"/"key", not on incidental "is"/"the" overlap with unrelated notes.
var stopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "by": true, "for": true, "from": true, "how": true, "in": true,
	"is": true, "it": true, "of": true, "on": true, "or": true, "that": true,
	"the": true, "this": true, "to": true, "was": true, "what": true,
	"when": true, "where": true, "which": true, "who": true, "why": true, "with": true,
}

// contentTerms tokenizes and drops stopwords. Applied to both the query and the
// documents so scoring stays consistent.
func contentTerms(s string) []string {
	toks := tokenize(s)
	out := toks[:0:0]
	for _, t := range toks {
		if !stopwords[t] {
			out = append(out, t)
		}
	}
	return out
}

// bm25Scores scores every memory against the query and returns id → score. A
// memory with no query-term overlap scores 0. IDF uses the +1 smoothed form, so
// scores are non-negative and a fixed threshold is meaningful.
func bm25Scores(query string, docs []Memory) map[string]float64 {
	out := make(map[string]float64, len(docs))
	queryTerms := contentTerms(query)
	if len(queryTerms) == 0 || len(docs) == 0 {
		return out
	}

	// Per-doc term frequencies and lengths.
	tf := make([]map[string]int, len(docs))
	totalLen := 0
	for i, d := range docs {
		toks := contentTerms(d.Text)
		totalLen += len(toks)
		m := make(map[string]int, len(toks))
		for _, t := range toks {
			m[t]++
		}
		tf[i] = m
	}
	n := float64(len(docs))
	avgdl := float64(totalLen) / n
	if avgdl == 0 {
		return out
	}

	// Document frequency per distinct query term.
	df := make(map[string]int)
	seen := map[string]bool{}
	for _, t := range queryTerms {
		if seen[t] {
			continue
		}
		seen[t] = true
		for i := range docs {
			if tf[i][t] > 0 {
				df[t]++
			}
		}
	}

	for i, d := range docs {
		dl := 0
		for _, c := range tf[i] {
			dl += c
		}
		var score float64
		for t := range seen {
			f := float64(tf[i][t])
			if f == 0 {
				continue
			}
			idf := math.Log(1 + (n-float64(df[t])+0.5)/(float64(df[t])+0.5))
			denom := f + bm25K1*(1-bm25B+bm25B*float64(dl)/avgdl)
			score += idf * (f * (bm25K1 + 1)) / denom
		}
		if score > 0 {
			out[d.ID] = score
		}
	}
	return out
}
