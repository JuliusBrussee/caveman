package compressors

import (
	"sort"

	"github.com/JuliusBrussee/caveman/engine/contextwindow"
)

// keepQueryRelevant adds the highest-scoring BM25 documents to keep. Existing
// force-keeps are never removed. Selection is deterministic: score descending,
// source order for ties. threshold is relative to the best document score.
func keepQueryRelevant(keep []bool, docs []string, query string, limit int, threshold float64) {
	if query == "" || len(docs) == 0 || len(keep) != len(docs) || limit <= 0 {
		return
	}
	scores := contextwindow.BM25(query, docs)
	maxScore := 0.0
	for _, score := range scores {
		if score > maxScore {
			maxScore = score
		}
	}
	if maxScore == 0 {
		return
	}
	type candidate struct {
		index int
		score float64
	}
	candidates := make([]candidate, 0, len(docs))
	for i, score := range scores {
		if !keep[i] && score/maxScore >= threshold {
			candidates = append(candidates, candidate{index: i, score: score})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score == candidates[j].score {
			return candidates[i].index < candidates[j].index
		}
		return candidates[i].score > candidates[j].score
	})
	added := 0
	for _, candidate := range candidates {
		if added >= limit {
			break
		}
		keep[candidate.index] = true
		added++
	}
}
