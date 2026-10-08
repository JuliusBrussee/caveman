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

// A saved count above this is not a count this proxy made.
const cacheWarmMaxCount = 1e9

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
	// saveMu orders the file writes, which happen outside mu.
	saveMu sync.Mutex
}

// warmGap is one observed gap of a stream class: the stream came back after
// d, or (silent) was last seen still silent d after its request.
type warmGap struct {
	class  string
	d      time.Duration
	silent bool
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
	if counts := t.load(); counts != nil {
		t.counts = counts
	}
	return t
}

// load reads the saved counts, or nil: a file that is missing, a symlink, too
// large, of another shape, or holding one count that is not a plausible count
// is dropped whole, and the table starts from the defaults.
func (t *warmTable) load() map[string][]float64 {
	if t.path == "" {
		return nil
	}
	info, err := os.Lstat(t.path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil
	}
	raw, err := os.ReadFile(t.path)
	if err != nil {
		return nil
	}
	var saved struct {
		BinSeconds int                  `json:"bin_seconds"`
		Classes    map[string][]float64 `json:"classes"`
	}
	if json.Unmarshal(raw, &saved) != nil || time.Duration(saved.BinSeconds)*time.Second != t.bin {
		return nil
	}
	for c, v := range saved.Classes {
		if !validWarmClass(c) || len(v) != t.bins+1 {
			return nil
		}
		for _, x := range v {
			if math.IsNaN(x) || x < 0 || x > cacheWarmMaxCount {
				return nil
			}
		}
	}
	return saved.Classes
}

// validWarmClass reports whether c is a key warmClassKey can produce.
func validWarmClass(c string) bool {
	for _, subagent := range []bool{false, true} {
		for _, tool := range []bool{false, true} {
			if c == warmClassKey(subagent, tool, 5*time.Minute) || c == warmClassKey(subagent, tool, time.Hour) {
				return true
			}
		}
	}
	return false
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

// observe records one gap of class; negative means the stream never came back.
func (t *warmTable) observe(class string, gap time.Duration) {
	t.record([]warmGap{{class: class, d: gap}})
}

// record counts gaps and saves every 20th. The file is written here, outside
// both the table's and the caller's lock.
func (t *warmTable) record(gaps []warmGap) {
	due := false
	for _, g := range gaps {
		if !validWarmClass(g.class) {
			continue
		}
		b, share := t.bins, []float64(nil)
		if g.d >= 0 && g.d < time.Duration(t.bins)*t.bin {
			b = int(g.d / t.bin)
			if g.silent {
				share = t.later(g.class, g.d)
			}
		}
		t.mu.Lock()
		counts := t.counts[g.class]
		if counts == nil {
			counts = make([]float64, t.bins+1)
			t.counts[g.class] = counts
		}
		if share == nil {
			counts[b]++
		}
		for i, x := range share {
			counts[i] += x
		}
		if t.pending++; t.pending >= 20 {
			t.pending, due = 0, true
		}
		t.mu.Unlock()
	}
	if due {
		t.save()
	}
}

// later spreads one stream last seen silent at age (it was evicted, or the
// proxy stopped, before it came back or was given up on) over the bins after
// age, by the class's own distribution past that point. That is the unbiased
// count (Kaplan-Meier's redistribution to the right): dropping the stream
// keeps only the returns that came before it could be lost, so the table
// drifts toward "always returns" and over-warms; counting it as never
// returning drifts the other way.
func (t *warmTable) later(class string, age time.Duration) []float64 {
	p, _, ok := t.probs(class)
	total := 0.0
	if ok {
		total = t.survival(p, age)
	}
	out := make([]float64, t.bins+1)
	if total <= 1e-9 {
		out[t.bins] = 1
		return out
	}
	b := int(age / t.bin)
	out[b] = p[b] * (1 - float64(age-time.Duration(b)*t.bin)/float64(t.bin)) / total
	for i := b + 1; i <= t.bins; i++ {
		out[i] = p[i] / total
	}
	return out
}

// save writes the counts atomically, 0600: counts per class and bin only. A
// path that is a symlink (or anything but a plain file) is left alone. Two
// proxies sharing $CAVEMAN_HOME each write their own counts, so the last
// writer wins and the other's observations since its load are lost: the
// table learns slower, never wrong.
func (t *warmTable) save() {
	if t.path == "" {
		return
	}
	t.saveMu.Lock()
	defer t.saveMu.Unlock()
	if info, err := os.Lstat(t.path); err == nil && !info.Mode().IsRegular() {
		return
	}
	t.mu.Lock()
	raw, err := json.Marshal(map[string]any{"version": 1, "bin_seconds": int(t.bin / time.Second), "classes": t.counts})
	t.mu.Unlock()
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
	defer t.mu.Unlock()
	seen := t.counts[class]
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
// full*realized, where realized is the share of the miss a stream that DID
// return paid (the window probability already leaves out the streams that
// never do; a realized measured over them too would count them twice). Warm k
// is sent only when its value, counting every later warm it commits to, is
// positive. plan[0] is unused.
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
