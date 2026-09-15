// Package cacheguard owns normalized cache-epoch drift and volatile-prefix
// checks. Provider adapters remain responsible for locating raw frozen/live
// spans; cacheguard never parses or reserializes provider envelopes.
package cacheguard

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"sort"
	"sync"
)

type Warning string

const (
	WarningMissingBoundary    Warning = "missing_boundary"
	WarningPrefixDrift        Warning = "prefix_drift"
	WarningVolatileStableSlot Warning = "volatile_stable_slot"
	WarningAdapterAmbiguity   Warning = "adapter_ambiguity"
	DecisionTransformLiveOnly         = "transform_live_only"
	DecisionPassThrough               = "pass_through"
	DecisionNewEpoch                  = "new_epoch"
)

type VolatileMatch struct {
	Kind   string
	Offset int
}

type Input struct {
	EpochID string
	Prefix  []byte
	// PrefixSHA256 lets a content-blind gateway enforce a prefix frozen by an
	// upstream framework adapter without receiving prompt bytes. When present it
	// must be lowercase hex and Prefix must be empty.
	PrefixSHA256  string
	BoundaryKnown bool
	AdapterKnown  bool
}

type Result struct {
	EpochID      string
	PrefixSHA256 string
	Warnings     []Warning
	Volatile     []VolatileMatch
	Decision     string
}

// defaultEpochCap bounds the number of distinct epochs the guard retains. Without
// it the epochs map grows unbounded — one entry per (session, provider, endpoint)
// tuple — for the life of a long-running proxy. When the cap is exceeded the
// oldest-inserted epoch is evicted; a later request for an evicted epoch is simply
// treated as a fresh first observation (transform-live-only), which is the safe
// direction (it never fabricates a drift warning).
const defaultEpochCap = 8192

type Guard struct {
	mu     sync.Mutex
	epochs map[string][sha256.Size]byte
	// order is the insertion order of epoch keys, used for oldest-first eviction.
	order []string
	cap   int
}

func New() *Guard {
	return &Guard{epochs: make(map[string][sha256.Size]byte), cap: defaultEpochCap}
}

// putEpoch records sum for id, tracking insertion order and evicting the oldest
// epoch once the cap is exceeded. Callers must hold g.mu.
func (g *Guard) putEpoch(id string, sum [sha256.Size]byte) {
	if _, exists := g.epochs[id]; !exists {
		g.order = append(g.order, id)
		for len(g.order) > g.cap {
			oldest := g.order[0]
			g.order = g.order[1:]
			delete(g.epochs, oldest)
		}
	}
	g.epochs[id] = sum
}

func (g *Guard) Inspect(input Input) (Result, error) {
	if g == nil {
		return Result{}, errors.New("cacheguard: nil guard")
	}
	if input.EpochID == "" {
		return Result{}, errors.New("cacheguard: empty epoch id")
	}

	var sum [sha256.Size]byte
	if input.PrefixSHA256 != "" {
		if len(input.Prefix) != 0 {
			return Result{}, errors.New("cacheguard: prefix and digest are mutually exclusive")
		}
		decoded, err := hex.DecodeString(input.PrefixSHA256)
		if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != input.PrefixSHA256 {
			return Result{}, errors.New("cacheguard: invalid prefix sha256")
		}
		copy(sum[:], decoded)
	} else {
		sum = sha256.Sum256(input.Prefix)
	}
	result := Result{
		EpochID:      input.EpochID,
		PrefixSHA256: hex.EncodeToString(sum[:]),
		Decision:     DecisionTransformLiveOnly,
	}
	if !input.BoundaryKnown {
		result.Warnings = append(result.Warnings, WarningMissingBoundary)
	}
	if !input.AdapterKnown {
		result.Warnings = append(result.Warnings, WarningAdapterAmbiguity)
	}

	if input.PrefixSHA256 == "" {
		result.Volatile = DetectVolatile(input.Prefix)
	}
	if len(result.Volatile) > 0 {
		result.Warnings = append(result.Warnings, WarningVolatileStableSlot)
	}

	g.mu.Lock()
	previous, exists := g.epochs[input.EpochID]
	if !exists {
		g.putEpoch(input.EpochID, sum)
	} else if previous != sum {
		result.Warnings = append(result.Warnings, WarningPrefixDrift)
	}
	g.mu.Unlock()

	if len(result.Warnings) > 0 {
		result.Decision = DecisionPassThrough
	}
	return result, nil
}

// StartNewEpoch explicitly replaces cache state. Callers must separately prove
// cold creation plus warm reads beat baseline total catalog cost.
func (g *Guard) StartNewEpoch(epochID string, prefix []byte) (Result, error) {
	if g == nil {
		return Result{}, errors.New("cacheguard: nil guard")
	}
	if epochID == "" {
		return Result{}, errors.New("cacheguard: empty epoch id")
	}
	sum := sha256.Sum256(prefix)
	g.mu.Lock()
	g.putEpoch(epochID, sum)
	g.mu.Unlock()
	return Result{
		EpochID:      epochID,
		PrefixSHA256: hex.EncodeToString(sum[:]),
		Decision:     DecisionNewEpoch,
		Volatile:     DetectVolatile(prefix),
	}, nil
}

var volatilePatterns = []struct {
	kind string
	re   *regexp.Regexp
}{
	{kind: "iso_timestamp", re: regexp.MustCompile(`\b20[0-9]{2}-[01][0-9]-[0-3][0-9][T ][0-2][0-9]:[0-5][0-9](?::[0-6][0-9](?:\.[0-9]+)?)?(?:Z|[+-][0-2][0-9]:?[0-5][0-9])?\b`)},
	{kind: "uuid", re: regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\b`)},
	{kind: "dynamic_id_key", re: regexp.MustCompile(`(?i)["'](?:run|request|build|trace|span)[_-]?id["']\s*:`)},
	{kind: "nonce_key", re: regexp.MustCompile(`(?i)["']nonce["']\s*:`)},
}

// DetectVolatile returns content-blind pattern kind and byte offset only. It
// never copies matching values into traces or errors.
func DetectVolatile(prefix []byte) []VolatileMatch {
	var matches []VolatileMatch
	for _, pattern := range volatilePatterns {
		for _, location := range pattern.re.FindAllIndex(prefix, -1) {
			matches = append(matches, VolatileMatch{Kind: pattern.kind, Offset: location[0]})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Offset == matches[j].Offset {
			return matches[i].Kind < matches[j].Kind
		}
		return matches[i].Offset < matches[j].Offset
	})
	return matches
}
