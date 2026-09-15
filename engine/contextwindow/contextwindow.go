// Package contextwindow selects the highest-value context items for a bounded
// model window. It is deterministic and local-only: BM25 relevance plus small
// recency/error/priority signals, no embeddings and no network.
package contextwindow

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/JuliusBrussee/caveman/engine/tokens"
)

const (
	defaultMaxTokens       = 8000
	defaultRecencyHalfLife = 6 * time.Hour
	defaultRecencyWeight   = 0.15
	defaultErrorBoost      = 0.8
	bm25K1                 = 1.5
	bm25B                  = 0.75
)

var errorSignalRe = regexp.MustCompile(`(?i)\b(ERROR|FATAL|PANIC|EXCEPTION|TRACEBACK|FAIL|FAILED|FAILURE|SECURITY|REGRESSION)\b`)

// Item is one candidate context fragment. Tokens may be supplied by a caller
// that already counted the item; when zero, Pack uses the engine token counter.
type Item struct {
	ID        string
	Text      string
	Tokens    int
	Timestamp time.Time
	Priority  float64
	Pin       bool
}

// Options controls context packing. MaxTokens is the total window budget;
// ReserveTokens is left unused for the next model response/tool call.
type Options struct {
	MaxTokens       int
	ReserveTokens   int
	Now             time.Time
	RecencyHalfLife time.Duration
	RecencyWeight   float64
	ErrorBoost      float64
	Counter         tokens.Counter
}

// Selected is an item chosen for the packed context.
type Selected struct {
	Item
	Score float64
}

// Result is the packed context and accounting for omitted items.
type Result struct {
	Items         []Selected
	TokensUsed    int
	TokensBefore  int
	TokensSaved   int
	DeferredCount int
}

// Pack selects the best items that fit within the token budget. It scores in
// relevance order but returns selected items in their original order so message
// chronology stays intact.
func Pack(query string, items []Item, opts Options) Result {
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = defaultMaxTokens
	}
	if opts.RecencyHalfLife <= 0 {
		opts.RecencyHalfLife = defaultRecencyHalfLife
	}
	if opts.RecencyWeight == 0 {
		opts.RecencyWeight = defaultRecencyWeight
	}
	if opts.ErrorBoost == 0 {
		opts.ErrorBoost = defaultErrorBoost
	}
	if opts.Counter == nil {
		opts.Counter = tokens.Default()
	}
	budget := opts.MaxTokens - opts.ReserveTokens
	if budget < 0 {
		budget = 0
	}
	now := opts.Now
	if now.IsZero() {
		now = latestTimestamp(items)
	}

	normalized := make([]Item, len(items))
	tokensBefore := 0
	for i, item := range items {
		normalized[i] = item
		if normalized[i].Tokens <= 0 {
			normalized[i].Tokens = opts.Counter.Count([]byte(item.Text))
		}
		tokensBefore += normalized[i].Tokens
	}

	bm25 := bm25Scores(query, normalized)
	candidates := make([]candidate, len(normalized))
	for i, item := range normalized {
		score := bm25[i] + item.Priority
		if !item.Timestamp.IsZero() && !now.IsZero() {
			age := now.Sub(item.Timestamp)
			if age < 0 {
				age = 0
			}
			score += opts.RecencyWeight * math.Exp(-float64(age)/float64(opts.RecencyHalfLife))
		}
		if errorSignalRe.MatchString(item.Text) {
			score += opts.ErrorBoost
		}
		if item.Pin {
			score += 1_000_000
		}
		candidates[i] = candidate{index: i, item: item, score: score}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score == candidates[j].score {
			return candidates[i].index < candidates[j].index
		}
		return candidates[i].score > candidates[j].score
	})

	var selected []candidate
	used := 0
	for _, c := range candidates {
		if c.item.Tokens <= 0 {
			continue
		}
		if used+c.item.Tokens > budget {
			continue
		}
		selected = append(selected, c)
		used += c.item.Tokens
	}
	sort.SliceStable(selected, func(i, j int) bool { return selected[i].index < selected[j].index })

	out := make([]Selected, len(selected))
	for i, c := range selected {
		out[i] = Selected{Item: c.item, Score: c.score}
	}
	return Result{
		Items:         out,
		TokensUsed:    used,
		TokensBefore:  tokensBefore,
		TokensSaved:   max(0, tokensBefore-used),
		DeferredCount: len(items) - len(out),
	}
}

type candidate struct {
	index int
	item  Item
	score float64
}

func latestTimestamp(items []Item) time.Time {
	var latest time.Time
	for _, item := range items {
		if item.Timestamp.After(latest) {
			latest = item.Timestamp
		}
	}
	return latest
}

// BM25 scores each document against the query using the same deterministic Okapi
// BM25 the context packer uses (k1=1.5, b=0.75) — no embeddings, no network. The
// returned scores are unnormalized and comparable only within the slice. It is
// exported so other deterministic compressors reuse the single BM25 implementation
// rather than growing a second one.
func BM25(query string, docs []string) []float64 {
	items := make([]Item, len(docs))
	for i, d := range docs {
		items[i] = Item{Text: d}
	}
	return bm25Scores(query, items)
}

func bm25Scores(query string, items []Item) []float64 {
	queryTerms := termFreq(query)
	out := make([]float64, len(items))
	if len(queryTerms) == 0 || len(items) == 0 {
		return out
	}
	docs := make([]map[string]int, len(items))
	docLen := make([]int, len(items))
	df := map[string]int{}
	totalLen := 0
	for i, item := range items {
		tf := termFreq(item.Text)
		docs[i] = tf
		for _, count := range tf {
			docLen[i] += count
		}
		totalLen += docLen[i]
		for term := range tf {
			df[term]++
		}
	}
	avgdl := 1.0
	if totalLen > 0 {
		avgdl = float64(totalLen) / float64(len(items))
	}
	for i := range items {
		for term := range queryTerms {
			tf := float64(docs[i][term])
			if tf == 0 {
				continue
			}
			idf := math.Log(1 + (float64(len(items))-float64(df[term])+0.5)/(float64(df[term])+0.5))
			denom := tf + bm25K1*(1-bm25B+bm25B*float64(docLen[i])/avgdl)
			out[i] += idf * (tf * (bm25K1 + 1)) / denom
		}
	}
	return out
}

func termFreq(text string) map[string]int {
	freq := map[string]int{}
	var current strings.Builder
	flush := func() {
		tok := current.String()
		if len(tok) >= 2 && !stopwords[tok] {
			freq[tok]++
		}
		current.Reset()
	}
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			current.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return freq
}

var stopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "by": true, "for": true, "from": true, "how": true, "in": true,
	"is": true, "it": true, "of": true, "on": true, "or": true, "that": true,
	"the": true, "this": true, "to": true, "was": true, "what": true, "when": true,
	"where": true, "which": true, "who": true, "why": true, "with": true,
}
