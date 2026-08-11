// Package inventory tracks items and computes rollups for a region.
package inventory

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Item is a single tracked unit.
type Item struct {
	ID    int
	Name  string
	Score float64
	Tags  []string
}

// ErrEmpty is returned when a rollup is requested over no items.
var ErrEmpty = errors.New("inventory: no items")

// Rollup summarizes a set of items by tag.
type Rollup struct {
	ByTag map[string]int
	Total int
	Top   string
}

// Summarize groups items by tag, totals them, and finds the highest scorer.
// It returns ErrEmpty when there is nothing to summarize.
func Summarize(items []Item) (Rollup, error) {
	if len(items) == 0 {
		return Rollup{}, ErrEmpty
	}
	r := Rollup{ByTag: map[string]int{}}
	best := -1.0
	for _, it := range items {
		r.Total++
		for _, t := range it.Tags {
			r.ByTag[strings.ToLower(t)]++
		}
		if it.Score > best {
			best = it.Score
			r.Top = it.Name
		}
	}
	return r, nil
}

// FilterByTag returns the items carrying the given tag, sorted by score
// descending so the most relevant items come first.
func FilterByTag(items []Item, tag string) []Item {
	tag = strings.ToLower(tag)
	var out []Item
	for _, it := range items {
		for _, t := range it.Tags {
			if strings.ToLower(t) == tag {
				out = append(out, it)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	return out
}

// Format renders a rollup as a stable, human-readable string.
func (r Rollup) Format() string {
	keys := make([]string, 0, len(r.ByTag))
	for k := range r.ByTag {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "total=%d top=%s\n", r.Total, r.Top)
	for _, k := range keys {
		fmt.Fprintf(&b, "  %s: %d\n", k, r.ByTag[k])
	}
	return b.String()
}
