package compressors

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strconv"

	"github.com/JuliusBrussee/caveman/engine/contextwindow"
	"github.com/JuliusBrussee/caveman/engine/safety"
)

// ElidedKey marks a collapsed run of array elements. The value is the number of
// elements dropped (recoverable in full via CCR). It keeps the output valid JSON.
const ElidedKey = "__caveman_elided__"

// ElidedInvariantsKey carries the class invariants of the run this marker
// replaces — facts computed from those exact elements (see invariants.go), never
// guessed. It is absent when the elements had no extractable top-level scalars.
const ElidedInvariantsKey = "__caveman_invariants__"

// ElidedNoteKey carries the one-per-payload contract line telling the model how
// to read an elided view. It rides on the FIRST elision marker of the payload
// (document order) because a JSON payload has no trailing line to append to, and
// adding a synthetic root key would change the shape the model parses.
const ElidedNoteKey = "__caveman_note__"

// errorKeyRe matches object keys whose subtrees carry signal the model needs —
// errors, messages, stack traces. The JSON compressor keeps these verbatim.
var errorKeyRe = regexp.MustCompile(`(?i)^(error|errors|message|msg|stack|stacktrace|stack_trace|trace|traceback|exception|reason|detail|details|warning|warnings)$`)

// errorValueRe matches critical keywords anywhere inside an array element's
// canonical JSON, so an element carrying an error/failure state is force-kept
// even when its key did not trigger errorKeyRe (e.g. {"status":"error"} or
// {"msg":"connection timeout"}).
var errorValueRe = regexp.MustCompile(`(?i)\b(error|errors|exception|failed|failure|critical|fatal|crash|panic|abort|timeout|denied|rejected)\b`)

// jsonCompressor collapses repetitive arrays in tool-output JSON while keeping
// every object key, the overall structure, any error/message subtree, and — for
// arrays it does collapse — the elements that carry signal: the first/last few,
// items in an error state, statistical anomalies, change-point boundaries, and
// (when a query is supplied) the items most relevant to it. Selection is fully
// deterministic: median/MAD anomaly detection, CUSUM change-points, and the
// shared deterministic BM25 — no randomness, no embeddings. It is S4 (lossy): the
// original is recoverable through CCR. On any parse problem it reports !ok and the
// caller forwards the bytes unchanged.
type jsonCompressor struct {
	arrayThreshold int     // arrays longer than this are eligible to collapse
	keepFirst      int     // elements always kept from the front
	keepLast       int     // elements always kept from the back
	maxItems       int     // cap on relevance-filled keeps (force-keeps may exceed it)
	anomalyZ       float64 // robust |z| (MAD-based) at/above which an item is an anomaly
	relevanceMin   float64 // normalized BM25 score at/above which an item is relevant
}

// NewJSON returns the default JSON/tool-output compressor.
func NewJSON() Compressor {
	return &jsonCompressor{arrayThreshold: 8, keepFirst: 3, keepLast: 2, maxItems: 50, anomalyZ: 2.0, relevanceMin: 0.30}
}

func (c *jsonCompressor) ContentType() string       { return "json" }
func (c *jsonCompressor) SafetyClass() safety.Class { return safety.S4 }

// Compress is the query-agnostic transform (identical to CompressQuery with an
// empty query).
func (c *jsonCompressor) Compress(input []byte) ([]byte, bool) { return c.compress(input, "") }

// CompressQuery biases array selection toward elements relevant to query via the
// shared deterministic BM25. An empty query behaves exactly like Compress.
func (c *jsonCompressor) CompressQuery(input []byte, query string) ([]byte, bool) {
	return c.compress(input, query)
}

func (c *jsonCompressor) CompressWithMetadata(input []byte, query string) ([]byte, Metadata, bool) {
	out, ok := c.compress(input, query)
	return out, Metadata{Method: "elision", LosslessToModel: metadataBool(false)}, ok
}

func (c *jsonCompressor) compress(input []byte, query string) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false // malformed JSON → pass-through
	}
	if dec.More() {
		return nil, false // more than one value → not a single JSON payload
	}
	collapsed := false
	shaped := c.transform(v, false, query, &collapsed)
	if !collapsed {
		// No array was actually collapsed. Re-serialization alone (whitespace,
		// object-key reordering) is not a reduction this S4 array compressor should
		// claim — pass through unchanged.
		return nil, false
	}
	out, err := json.Marshal(shaped)
	if err != nil {
		return nil, false
	}
	return out, true
}

// transform recursively compresses v. When preserve is true (inside an
// error/message subtree) the value is kept verbatim. collapsed is set to true
// when any array is actually collapsed (≥1 element elided), so the caller can
// pass through when nothing real happened.
func (c *jsonCompressor) transform(v any, preserve bool, query string, collapsed *bool) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, val := range t {
			m[k] = c.transform(val, preserve || errorKeyRe.MatchString(k), query, collapsed)
		}
		return m
	case []any:
		if containsElidedMarker(t) {
			return t
		}
		if preserve {
			return t // keep error/message arrays in full
		}
		if len(t) <= c.arrayThreshold {
			out := make([]any, len(t))
			for i, e := range t {
				out[i] = c.transform(e, preserve, query, collapsed)
			}
			return out
		}
		return c.selectArray(t, query, collapsed)
	default:
		return v
	}
}

// selectArray collapses a long array to the elements that carry signal, emitting
// one elided-count marker per dropped run so the output stays valid JSON. It sets
// *collapsed when it actually drops at least one element.
func (c *jsonCompressor) selectArray(arr []any, query string, collapsed *bool) []any {
	n := len(arr)

	// Canonical per-element strings (json.Marshal sorts map keys → deterministic).
	docs := make([]string, n)
	for i, e := range arr {
		if b, err := json.Marshal(e); err == nil {
			docs[i] = string(b)
		}
	}

	keep := make([]bool, n)
	force := func(i int) {
		if i >= 0 && i < n {
			keep[i] = true
		}
	}

	// First/last anchors.
	for i := 0; i < c.keepFirst && i < n; i++ {
		force(i)
	}
	for i := n - c.keepLast; i < n; i++ {
		force(i)
	}

	// Error-state elements (force-kept, never dropped).
	for i, d := range docs {
		if errorValueRe.MatchString(d) {
			force(i)
		}
	}

	// Numeric anomalies (robust MAD-z per numeric leaf path + element length).
	for i := range anomalyIndices(arr, docs, c.anomalyZ) {
		force(i)
	}

	// Change-point boundaries on the primary full-coverage numeric series
	// (falls back to element length when no full-coverage numeric path exists).
	for k := range changePointSeams(primarySeries(arr, docs), c.anomalyZ, changePointBudget(c.maxItems)) {
		force(k)
	}

	// Relevance fill (only when a query is present), capped at maxItems. Force-keeps
	// above are never trimmed even if they already exceed the cap (honesty over ratio).
	if query != "" {
		scores := contextwindow.BM25(query, docs)
		maxScore := 0.0
		for _, s := range scores {
			if s > maxScore {
				maxScore = s
			}
		}
		if maxScore > 0 {
			type cand struct {
				idx   int
				score float64
			}
			var cands []cand
			for i := 0; i < n; i++ {
				if keep[i] {
					continue
				}
				if scores[i]/maxScore >= c.relevanceMin {
					cands = append(cands, cand{i, scores[i]})
				}
			}
			sort.SliceStable(cands, func(a, b int) bool {
				if cands[a].score == cands[b].score {
					return cands[a].idx < cands[b].idx
				}
				return cands[a].score > cands[b].score
			})
			kept := 0
			for i := range keep {
				if keep[i] {
					kept++
				}
			}
			for _, cd := range cands {
				if kept >= c.maxItems {
					break
				}
				keep[cd.idx] = true
				kept++
			}
		}
	}

	keepNonRedundant(docsAsUnits(docs), keep)

	// Emit kept elements in original order; one marker per dropped run.
	out := make([]any, 0, n)
	var run []any
	var runDocs []string
	flush := func() {
		if len(run) == 0 {
			return
		}
		elidedBytes := 0
		for _, doc := range runDocs {
			elidedBytes += len(doc) + 1
		}
		summary := summarizeElementRun(run, runDocs)
		marker := map[string]any{ElidedKey: json.Number(strconv.Itoa(len(run)))}
		if summary != "" {
			marker[ElidedInvariantsKey] = summary
		}
		if !worthEliding(len(run), len(summary)+len(ElidedKey)+len(ElidedInvariantsKey)+16, elidedBytes, summary) {
			// Too small a run to describe and too small to be worth a recovery
			// handle: keep the elements themselves and claim nothing for them.
			for _, element := range run {
				out = append(out, c.transform(element, false, query, collapsed))
			}
			run, runDocs = run[:0], runDocs[:0]
			return
		}
		if !*collapsed && wantsElisionNote(elidedBytes) {
			marker[ElidedNoteKey] = elisionNote("elements")
		}
		out = append(out, marker)
		run, runDocs = run[:0], runDocs[:0]
		*collapsed = true
	}
	for i := 0; i < n; i++ {
		if keep[i] {
			flush()
			out = append(out, c.transform(arr[i], false, query, collapsed))
		} else {
			run = append(run, arr[i])
			runDocs = append(runDocs, docs[i])
		}
	}
	flush()
	return out
}

// summarizeElementRun describes a run of dropped array elements by their
// top-level scalar members. docs carries the canonical JSON of the same elements,
// used only to size the run against the summary budget.
func summarizeElementRun(run []any, docs []string) string {
	units := make([][]field, len(run))
	elidedBytes := 0
	for i, element := range run {
		units[i] = objectFields(element)
		elidedBytes += len(docs[i]) + 1
	}
	return summarizeElided(units, elidedBytes)
}

// changePointBudget bounds the number of accepted change-point splits.
func changePointBudget(maxItems int) int {
	b := maxItems / 4
	if b > 8 {
		b = 8
	}
	if b < 1 {
		b = 1
	}
	return b
}

// numericLeaves records every numeric leaf value in v keyed by its dotted path
// ("" for a scalar-number element root). Arrays nested inside an element are not
// descended into — only object fields and the root scalar contribute, which keeps
// paths stable and comparable across heterogeneous elements.
func numericLeaves(v any, prefix string, out map[string]float64) {
	switch t := v.(type) {
	case json.Number:
		if f, err := t.Float64(); err == nil {
			out[prefix] = f
		}
	case float64:
		out[prefix] = t
	case map[string]any:
		for k, val := range t {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			numericLeaves(val, key, out)
		}
	}
}

// anomalyIndices returns the set of element indices that are statistical
// anomalies: for each numeric leaf path (and the element-length pseudo-path), it
// computes a robust MAD-based z over the elements that have that path and flags
// those at/beyond zThresh. Absent values are excluded from a path's statistics,
// never imputed (imputing would fabricate anomalies).
func anomalyIndices(arr []any, docs []string, zThresh float64) map[int]bool {
	n := len(arr)
	flags := make(map[int]bool)

	// Gather values per path.
	pathVals := map[string]map[int]float64{}
	for i, e := range arr {
		leaves := map[string]float64{}
		numericLeaves(e, "", leaves)
		for p, val := range leaves {
			if pathVals[p] == nil {
				pathVals[p] = map[int]float64{}
			}
			pathVals[p][i] = val
		}
	}
	// Element length as a synthetic path (always full coverage).
	lenVals := map[int]float64{}
	for i, d := range docs {
		lenVals[i] = float64(len(d))
	}
	pathVals["__len__"] = lenVals

	// Deterministic path order.
	paths := make([]string, 0, len(pathVals))
	for p := range pathVals {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, p := range paths {
		vals := pathVals[p]
		series := make([]float64, 0, len(vals))
		idxs := make([]int, 0, len(vals))
		for i := 0; i < n; i++ {
			if v, ok := vals[i]; ok {
				series = append(series, v)
				idxs = append(idxs, i)
			}
		}
		if len(series) < 4 {
			continue
		}
		med, scale := medianAndScale(series)
		if scale <= 0 {
			continue
		}
		for j, v := range series {
			if math.Abs(0.6745*(v-med)/scale) >= zThresh {
				flags[idxs[j]] = true
			}
		}
	}
	return flags
}

// medianAndScale returns the median and a robust scale (MAD) of xs, falling back
// to the standard deviation when the MAD is zero (≥ half the values identical).
// It does not mutate xs.
func medianAndScale(xs []float64) (med, scale float64) {
	cp := append([]float64(nil), xs...)
	sort.Float64s(cp)
	med = medianSorted(cp)
	dev := make([]float64, len(cp))
	for i, v := range cp {
		dev[i] = math.Abs(v - med)
	}
	sort.Float64s(dev)
	mad := medianSorted(dev)
	if mad > 0 {
		return med, mad
	}
	// Fall back to stddev so a few outliers against a near-constant series still flag.
	var mean float64
	for _, v := range cp {
		mean += v
	}
	mean /= float64(len(cp))
	var ss float64
	for _, v := range cp {
		ss += (v - mean) * (v - mean)
	}
	if len(cp) < 2 {
		return med, 0
	}
	std := math.Sqrt(ss / float64(len(cp)-1))
	// MAD-equivalent scaling expects a MAD; std/0.6745 makes the |z| test consistent.
	return mean, std / 0.6745
}

func medianSorted(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// primarySeries picks the change-point series: the highest-variance numeric leaf
// path that is present in every element (so indices align with element indices),
// or the element-length series when no numeric path has full coverage.
func primarySeries(arr []any, docs []string) []float64 {
	n := len(arr)
	leavesByIdx := make([]map[string]float64, n)
	for i, e := range arr {
		m := map[string]float64{}
		numericLeaves(e, "", m)
		leavesByIdx[i] = m
	}
	// Candidate full-coverage paths.
	counts := map[string]int{}
	for _, m := range leavesByIdx {
		for p := range m {
			counts[p]++
		}
	}
	bestPath, bestVar := "", -1.0
	paths := make([]string, 0, len(counts))
	for p := range counts {
		paths = append(paths, p)
	}
	sort.Strings(paths) // deterministic tie-break
	for _, p := range paths {
		if counts[p] != n {
			continue
		}
		series := make([]float64, n)
		for i := range leavesByIdx {
			series[i] = leavesByIdx[i][p]
		}
		if v := variance(series); v > bestVar {
			bestVar = v
			bestPath = p
		}
	}
	if bestPath != "" {
		series := make([]float64, n)
		for i := range leavesByIdx {
			series[i] = leavesByIdx[i][bestPath]
		}
		return series
	}
	// Fall back to element lengths.
	series := make([]float64, n)
	for i, d := range docs {
		series[i] = float64(len(d))
	}
	return series
}

func variance(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	var mean float64
	for _, v := range xs {
		mean += v
	}
	mean /= float64(len(xs))
	var ss float64
	for _, v := range xs {
		ss += (v - mean) * (v - mean)
	}
	return ss / float64(len(xs)-1)
}

// changePointSeams finds up to maxSplits regime shifts in y via deterministic
// binary segmentation (a CUSUM split per segment, largest segment first), and
// returns the element indices on both sides of each accepted shift so both
// regimes are represented at the seam.
func changePointSeams(y []float64, zThresh float64, maxSplits int) map[int]bool {
	seams := make(map[int]bool)
	if len(y) < 8 {
		return seams
	}
	if nearConstantSlope(y) {
		return seams
	}
	type seg struct{ a, b int }
	segs := []seg{{0, len(y)}}
	splits := 0
	for splits < maxSplits && len(segs) > 0 {
		// Pick the largest remaining segment (ties by lower start index).
		bi, bl := -1, -1
		for i, s := range segs {
			if l := s.b - s.a; l > bl {
				bl, bi = l, i
			}
		}
		s := segs[bi]
		segs = append(segs[:bi], segs[bi+1:]...)
		if s.b-s.a < 4 {
			continue
		}
		k, ok := bestSplit(y, s.a, s.b, zThresh)
		if !ok {
			continue
		}
		seams[k] = true
		if k+1 < len(y) {
			seams[k+1] = true
		}
		segs = append(segs, seg{s.a, k + 1}, seg{k + 1, s.b})
		splits++
	}
	return seams
}

func containsElidedMarker(arr []any) bool {
	for _, e := range arr {
		// A marker object carries ElidedKey plus, since invariant summaries, up to
		// two more reserved keys. The reserved name is the whole test — keying off
		// the object's exact size made an enriched marker invisible here, and the
		// second pass then re-compressed an already-compressed array.
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if _, ok := m[ElidedKey]; ok {
			return true
		}
	}
	return false
}

func nearConstantSlope(y []float64) bool {
	if len(y) < 3 {
		return true
	}
	d := y[1] - y[0]
	const eps = 1e-9
	for i := 2; i < len(y); i++ {
		if math.Abs((y[i]-y[i-1])-d) > eps {
			return false
		}
	}
	return true
}

// bestSplit finds the CUSUM-maximizing split k in [a, b) (left=[a,k], right=[k+1,b))
// and accepts it when the mean shift across k exceeds zThresh robust scales.
func bestSplit(y []float64, a, b int, zThresh float64) (int, bool) {
	if b-a < 4 {
		return 0, false
	}
	var mean float64
	for j := a; j < b; j++ {
		mean += y[j]
	}
	mean /= float64(b - a)
	cum, best, bestK := 0.0, -1.0, -1
	for k := a; k < b-1; k++ {
		cum += y[k] - mean
		if math.Abs(cum) > best {
			best, bestK = math.Abs(cum), k
		}
	}
	if bestK < 0 {
		return 0, false
	}
	leftMean := segMean(y, a, bestK+1)
	rightMean := segMean(y, bestK+1, b)
	shift := math.Abs(leftMean - rightMean)
	// A clean step (both regimes internally constant) inflates the whole-segment
	// scale by the step size, so testing against the segment scale alone would
	// reject the very boundary the step marks. Detect that case via the pooled
	// WITHIN-regime scale: when it is ~0, both sides are flat → any level
	// difference is a genuine change point.
	_, ls := medianAndScale(y[a : bestK+1])
	_, rs := medianAndScale(y[bestK+1 : b])
	if math.Max(ls, rs) <= 0 {
		return bestK, shift > 0
	}
	// Otherwise require the shift to exceed the whole segment's own variability.
	// This is deliberately conservative: it rejects a steady ramp (whose halves
	// still trend, so a CUSUM split looks like a shift) while still catching an
	// abrupt regime change that dwarfs the segment spread. Missing a subtle seam
	// only costs a representative sample (CCR holds the original); a spurious seam
	// would over-keep, depress the ratio, and risk breaking idempotence.
	_, segScale := medianAndScale(y[a:b])
	if segScale > 0 && shift > zThresh*segScale {
		return bestK, true
	}
	return 0, false
}

func segMean(y []float64, a, b int) float64 {
	if b <= a {
		return 0
	}
	var s float64
	for j := a; j < b; j++ {
		s += y[j]
	}
	return s / float64(b-a)
}
