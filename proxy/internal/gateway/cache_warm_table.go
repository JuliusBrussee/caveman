package gateway

import (
	_ "embed"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// cacheWarmTableJSON is the default return-time table: per stream class, the
// probability that the next request of the stream arrives in each 30-second
// bin after the last one started (the last bin: later or never). Measured on
// real Claude Code sessions with .blocks/cache-warm-sim.py; its provenance
// block says when, on how much data and how. Aggregate probabilities only.
//
//go:embed cache_warm_table.json
var cacheWarmTableJSON []byte

// cacheWarmLearnWeight is how many observations the defaults count for once
// a user's own gaps start arriving.
const cacheWarmLearnWeight = 100

type warmClass struct {
	N        int       `json:"n"`
	Realized float64   `json:"realized"`
	P        []float64 `json:"p"`
}

// warmTable answers "is the next warm of this stream worth it" from the
// defaults plus the gaps this proxy has seen, persisted as plain counts.
type warmTable struct {
	bin     time.Duration
	bins    int
	prior   map[string]warmClass
	mu      sync.Mutex
	counts  map[string][]float64
	path    string
	pending int
}

func newWarmTable(path string) *warmTable {
	var doc struct {
		BinSeconds int                  `json:"bin_seconds"`
		Bins       int                  `json:"bins"`
		Classes    map[string]warmClass `json:"classes"`
	}
	if json.Unmarshal(cacheWarmTableJSON, &doc) != nil || doc.BinSeconds <= 0 || doc.Bins <= 0 {
		panic("gateway: cache_warm_table.json is invalid")
	}
	t := &warmTable{bin: time.Duration(doc.BinSeconds) * time.Second, bins: doc.Bins, prior: doc.Classes,
		counts: map[string][]float64{}, path: path}
	if path != "" {
		if raw, err := os.ReadFile(path); err == nil {
			var saved struct {
				Classes map[string][]float64 `json:"classes"`
			}
			if json.Unmarshal(raw, &saved) == nil {
				for c, v := range saved.Classes {
					if len(v) == t.bins+1 && t.lookup(c) != nil {
						t.counts[c] = v
					}
				}
			}
		}
	}
	return t
}

// warmClassKey names a stream class: main or subagent, whether the last
// response asked for a tool, and the cache lifetime.
func warmClassKey(subagent, tool bool, ttl time.Duration) string {
	kind, stop, life := "main", "end", "5m"
	if subagent {
		kind = "subagent"
	}
	if tool {
		stop = "tool"
	}
	if ttl == time.Hour {
		life = "1h"
	}
	return kind + "/" + stop + "/" + life
}

// lookup is the most specific default the class has: itself, kind/stop, all.
func (t *warmTable) lookup(class string) *warmClass {
	for c := class; ; {
		if p, ok := t.prior[c]; ok {
			return &p
		}
		i := strings.LastIndexByte(c, '/')
		if i < 0 {
			if c == "all" {
				return nil
			}
			c = "all"
			continue
		}
		c = c[:i]
	}
}

// observe records one gap of class; inf means the stream never came back.
func (t *warmTable) observe(class string, gap time.Duration) {
	if t.lookup(class) == nil {
		return
	}
	b := t.bins
	if gap >= 0 && gap < time.Duration(t.bins)*t.bin {
		b = int(gap / t.bin)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.counts[class] == nil {
		t.counts[class] = make([]float64, t.bins+1)
	}
	t.counts[class][b]++
	if t.pending++; t.pending >= 20 {
		t.pending = 0
		t.saveLocked()
	}
}

// saveLocked writes the counts atomically, 0600. Counts per class and bin only.
func (t *warmTable) saveLocked() {
	if t.path == "" {
		return
	}
	raw, err := json.Marshal(map[string]any{"version": 1, "bin_seconds": int(t.bin / time.Second), "classes": t.counts})
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(t.path), ".cache-warm-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(raw)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Chmod(tmp.Name(), 0o600) != nil || os.Rename(tmp.Name(), t.path) != nil {
		_ = os.Remove(tmp.Name())
	}
}

// probs is the class's return-time distribution: the defaults, pulled toward
// what this proxy has seen as its own observations accumulate.
func (t *warmTable) probs(class string) ([]float64, float64, bool) {
	prior := t.lookup(class)
	if prior == nil || len(prior.P) != t.bins+1 {
		return nil, 0, false
	}
	t.mu.Lock()
	seen := t.counts[class]
	t.mu.Unlock()
	if seen == nil {
		return prior.P, prior.Realized, true
	}
	n := 0.0
	for _, c := range seen {
		n += c
	}
	out := make([]float64, len(prior.P))
	for i := range out {
		out[i] = (seen[i] + cacheWarmLearnWeight*prior.P[i]) / (n + cacheWarmLearnWeight)
	}
	return out, prior.Realized, true
}

// survival is P(gap > t), uniform within a bin.
func (t *warmTable) survival(p []float64, at time.Duration) float64 {
	if at <= 0 {
		return 1
	}
	b := int(at / t.bin)
	if b >= t.bins {
		return p[t.bins]
	}
	frac := float64(at-time.Duration(b)*t.bin) / float64(t.bin)
	s := p[b] * (1 - frac)
	for _, x := range p[b+1:] {
		s += x
	}
	return s
}

// plan decides every warm of one idle stream up front by backward induction
// (optimal stopping). Warm k at k*delay costs warm and extends the cache from
// k*delay+ttl-delay to k*delay+ttl; a return in that window saves
// full*realized. Warm k is sent only when its value, counting every later
// warm it commits to, is positive. plan[0] is unused.
func (t *warmTable) plan(class string, ttl, horizon time.Duration, full, warm float64) []bool {
	d := cacheWarmDelay(ttl)
	p, realized, ok := t.probs(class)
	if d <= 0 || !ok || full <= 0 || warm <= 0 || math.IsNaN(full+warm) {
		return nil
	}
	K := int(horizon / d)
	plan := make([]bool, K+1)
	v := 0.0
	for k := K; k >= 1; k-- {
		at := time.Duration(k) * d
		s := t.survival(p, at)
		if s <= 1e-9 {
			v = 0
			continue
		}
		window := (t.survival(p, at+ttl-d) - t.survival(p, at+ttl)) / s
		stay := t.survival(p, at+d) / s
		value := -warm + full*realized*window + stay*v
		plan[k] = value > 0
		v = math.Max(0, value)
	}
	return plan
}
