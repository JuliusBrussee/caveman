package cacheengine

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/JuliusBrussee/caveman/shared/platform/cacheguard"
)

// Engine is concurrency-safe after construction. Custom callbacks must provide
// their own concurrency safety as documented by Config.
type Engine struct {
	guard                *cacheguard.Guard
	prefixSafety         *prefixSafetyCache
	maxKeyShards         int
	resolveProfile       func(NativeRequest) (Profile, bool)
	drivers              map[string]Driver
	configErr            error
	maxRequestBytes      int
	maxStablePrefixBytes int
}

const (
	maxConfiguredKeyShards = 1_000_000
	defaultInputByteLimit  = 64 << 20
	maxConfiguredByteLimit = 1 << 30
)

// New constructs an engine while preserving legacy one-result API. Invalid
// configuration makes every operation return its stored error; prefer
// NewChecked in new code.
func New(config Config) *Engine {
	engine, err := newEngine(config)
	if err != nil {
		return &Engine{
			guard: cacheguard.New(), prefixSafety: newPrefixSafetyCache(8192),
			maxKeyShards: 64, maxRequestBytes: defaultInputByteLimit, maxStablePrefixBytes: defaultInputByteLimit,
			resolveProfile: defaultProfile, drivers: map[string]Driver{}, configErr: err,
		}
	}
	return engine
}

// NewChecked validates configuration at construction time. New preserves its
// original one-result API but stores the same configuration error and makes all
// operations fail closed.
func NewChecked(config Config) (*Engine, error) {
	return newEngine(config)
}

func newEngine(config Config) (*Engine, error) {
	if config.MaxKeyShards < 0 || config.MaxKeyShards > maxConfiguredKeyShards {
		return nil, fmt.Errorf("cacheengine: max key shards must be within 0..%d", maxConfiguredKeyShards)
	}
	if config.MaxRequestBytes < 0 || config.MaxRequestBytes > maxConfiguredByteLimit {
		return nil, fmt.Errorf("cacheengine: max request bytes must be within 0..%d", maxConfiguredByteLimit)
	}
	if config.MaxStablePrefixBytes < 0 || config.MaxStablePrefixBytes > maxConfiguredByteLimit {
		return nil, fmt.Errorf("cacheengine: max stable prefix bytes must be within 0..%d", maxConfiguredByteLimit)
	}
	maxShards := config.MaxKeyShards
	if maxShards == 0 {
		maxShards = 64
	}
	maxRequestBytes := config.MaxRequestBytes
	if maxRequestBytes == 0 {
		maxRequestBytes = defaultInputByteLimit
	}
	maxStablePrefixBytes := config.MaxStablePrefixBytes
	if maxStablePrefixBytes == 0 {
		maxStablePrefixBytes = defaultInputByteLimit
	}
	resolver := config.ResolveProfile
	if resolver == nil {
		resolver = defaultProfile
	}
	drivers := make(map[string]Driver, len(config.Drivers))
	for rawProvider, driver := range config.Drivers {
		provider := strings.ToLower(strings.TrimSpace(rawProvider))
		if provider == "" || driver == nil {
			return nil, errors.New("cacheengine: driver needs non-empty provider and implementation")
		}
		if _, exists := drivers[provider]; exists {
			return nil, fmt.Errorf("cacheengine: duplicate normalized driver provider %q", provider)
		}
		drivers[provider] = driver
	}
	return &Engine{
		guard: cacheguard.New(), prefixSafety: newPrefixSafetyCache(8192),
		maxKeyShards: maxShards, maxRequestBytes: maxRequestBytes, maxStablePrefixBytes: maxStablePrefixBytes,
		resolveProfile: resolver, drivers: drivers,
	}, nil
}

// Plan selects profitable stable-prefix cache boundaries without editing wire bytes.
func (e *Engine) Plan(request PlanRequest) (Plan, error) {
	if e == nil || e.guard == nil || e.prefixSafety == nil {
		return Plan{}, errors.New("cacheengine: nil engine")
	}
	if e.configErr != nil {
		return Plan{}, e.configErr
	}
	if err := validatePlanRequest(request); err != nil {
		return Plan{}, err
	}
	profile := normalizedProfile(request.Profile)
	plan := Plan{
		Decision:       DecisionPassThrough,
		Reason:         ReasonUnsupported,
		ProfileID:      profile.ID,
		Mode:           profile.Mode,
		Attribution:    profile.Attribution,
		EconomicsBasis: "modeled_input_rate_units",
		KeyShardCount:  1,
	}
	if !profile.EconomicsKnown {
		plan.EconomicsBasis = "unavailable"
		plan.Warnings = append(plan.Warnings, "cache_economics_unavailable")
	}
	if profile.Mode == ModeUnsupported {
		return plan, nil
	}

	prefix, stableSegments, err := stablePrefix(request.Segments, e.maxStablePrefixBytes)
	if err != nil {
		return Plan{}, err
	}
	if len(stableSegments) == 0 {
		plan.Reason = ReasonNoStablePrefix
		return plan, nil
	}
	prefixSHA256, volatile := e.prefixSafety.inspect(prefix)
	if volatile {
		plan.Reason = ReasonVolatilePrefix
		plan.Warnings = []string{string(cacheguard.WarningVolatileStableSlot)}
		return plan, nil
	}

	guardResult, err := e.guard.Inspect(cacheguard.Input{
		EpochID:       epochKey(request.Scope, request.Epoch, profile.ID),
		PrefixSHA256:  prefixSHA256,
		BoundaryKnown: true,
		AdapterKnown:  true,
	})
	if err != nil {
		return Plan{}, err
	}
	plan.PrefixSHA256 = guardResult.PrefixSHA256
	for _, warning := range guardResult.Warnings {
		plan.Warnings = append(plan.Warnings, string(warning))
		if warning == cacheguard.WarningPrefixDrift {
			plan.Reason = ReasonPrefixDrift
			return plan, nil
		}
	}

	expectedCalls := request.ExpectedCalls
	if expectedCalls == 0 {
		expectedCalls = 2
	}
	if expectedCalls < 2 {
		plan.Reason = ReasonNoExpectedReuse
		return plan, nil
	}

	candidates, belowMinimum, negativeEconomics, err := breakpointCandidates(stableSegments, expectedCalls, profile)
	if err != nil {
		return Plan{}, err
	}
	if len(candidates) == 0 {
		switch {
		case belowMinimum:
			plan.Reason = ReasonBelowMinimum
		case negativeEconomics:
			plan.Reason = ReasonNegativeEconomics
		default:
			plan.Reason = ReasonNoStablePrefix
		}
		return plan, nil
	}
	plan.Breakpoints = limitBreakpoints(candidates, profile.MaxBreakpoints)
	if allTokenCountsUnavailable(plan.Breakpoints) {
		plan.EconomicsBasis = "unavailable"
		plan.Warnings = append(plan.Warnings, "token_count_unavailable")
	}
	for _, breakpoint := range plan.Breakpoints {
		if breakpoint.ExpectedNetInputRateUnits > plan.ExpectedNetInputRateUnits {
			plan.ExpectedNetInputRateUnits = breakpoint.ExpectedNetInputRateUnits
		}
	}
	if profile.Mode == ModeImplicit {
		for index := range plan.Breakpoints {
			plan.Breakpoints[index].ExpectedNetInputRateUnits = 0
		}
		plan.ExpectedNetInputRateUnits = 0
		plan.EconomicsBasis = "provider_managed_unattributed"
	}
	if profile.RoutingKey {
		var capped bool
		plan.KeyShardCount, plan.KeyShard, capped = keyShard(request, profile, e.maxKeyShards)
		plan.RoutingKey = routingKey(request.Scope, profile.ID, plan.PrefixSHA256, plan.KeyShard)
		if capped {
			plan.Warnings = append(plan.Warnings, "routing_key_shard_cap_reached")
		}
	}
	if profile.Mode == ModeImplicit {
		plan.Decision = DecisionObserveOnly
		plan.Reason = ReasonProviderManaged
		return plan, nil
	}
	plan.Decision = DecisionApply
	plan.Reason = ReasonApplied
	return plan, nil
}

// StartEpoch explicitly replaces frozen prefix state for one scope/profile epoch.
func (e *Engine) StartEpoch(request PlanRequest) (Plan, error) {
	if e == nil || e.guard == nil {
		return Plan{}, errors.New("cacheengine: nil engine")
	}
	if e.configErr != nil {
		return Plan{}, e.configErr
	}
	if err := validatePlanRequest(request); err != nil {
		return Plan{}, err
	}
	prefix, stableSegments, err := stablePrefix(request.Segments, e.maxStablePrefixBytes)
	if err != nil {
		return Plan{}, err
	}
	if len(stableSegments) == 0 {
		return Plan{}, errors.New("cacheengine: no stable prefix")
	}
	if len(cacheguard.DetectVolatile(prefix)) > 0 {
		return Plan{}, errors.New("cacheengine: volatile content cannot start stable epoch")
	}
	profile := normalizedProfile(request.Profile)
	economicsBasis := "modeled_input_rate_units"
	var warnings []string
	if !profile.EconomicsKnown {
		economicsBasis = "unavailable"
		warnings = []string{"cache_economics_unavailable"}
	}
	result, err := e.guard.StartNewEpoch(epochKey(request.Scope, request.Epoch, profile.ID), prefix)
	if err != nil {
		return Plan{}, err
	}
	return Plan{
		Decision:       DecisionNewEpoch,
		Reason:         string(cacheguard.DecisionNewEpoch),
		ProfileID:      profile.ID,
		Mode:           profile.Mode,
		Attribution:    profile.Attribution,
		PrefixSHA256:   result.PrefixSHA256,
		EconomicsBasis: economicsBasis,
		KeyShardCount:  1,
		Warnings:       warnings,
	}, nil
}

func validatePlanRequest(request PlanRequest) error {
	if !validIdentity(request.Scope, 4096, false) {
		return errors.New("cacheengine: invalid scope")
	}
	if !validIdentity(request.Epoch, 4096, false) {
		return errors.New("cacheengine: invalid epoch")
	}
	if !validIdentity(request.PartitionKey, 4096, true) {
		return errors.New("cacheengine: invalid partition key")
	}
	if request.ExpectedCalls < 0 || request.ExpectedRequestsPerMinute < 0 {
		return errors.New("cacheengine: negative traffic expectation")
	}
	profile := normalizedProfile(request.Profile)
	if !validIdentity(profile.ID, 256, false) || !validIdentity(profile.Provider, 64, true) || !validIdentity(profile.OptimizerID, 256, true) {
		return errors.New("cacheengine: invalid profile identity")
	}
	if profile.Mode != ModeUnsupported && profile.Mode != ModeImplicit && profile.Mode != ModeAffinity && profile.Mode != ModeExplicit {
		return fmt.Errorf("cacheengine: unknown mode %q", profile.Mode)
	}
	if profile.Mode != ModeUnsupported {
		if profile.MaxBreakpoints <= 0 || profile.MinPrefixTokens < 0 || profile.MaxRPMPerKey < 0 || profile.TTL < 0 {
			return errors.New("cacheengine: invalid cache thresholds")
		}
		switch profile.Attribution {
		case AttributionNone, AttributionOrganic, AttributionAffinity, AttributionCausal:
		default:
			return fmt.Errorf("cacheengine: unknown attribution %q", profile.Attribution)
		}
		if profile.EconomicsKnown && (!finiteNonNegative(profile.WriteMultiplier) || !finiteNonNegative(profile.ReadMultiplier)) {
			return errors.New("cacheengine: invalid cache economics")
		}
	}
	return nil
}

func normalizedProfile(profile Profile) Profile {
	if profile.Mode == ModeUnsupported {
		if profile.ID == "" {
			profile.ID = "unsupported"
		}
		return profile
	}
	if profile.MaxBreakpoints == 0 {
		profile.MaxBreakpoints = 1
	}
	if profile.MaxRPMPerKey == 0 {
		profile.MaxRPMPerKey = 15
	}
	if profile.Attribution == "" {
		profile.Attribution = AttributionNone
	}
	return profile
}

func stablePrefix(segments []Segment, maxBytes int) ([]byte, []Segment, error) {
	var prefix []byte
	var stable []Segment
	seenNames := map[string]bool{}
	for _, segment := range segments {
		if !segment.Stable || !segment.Cacheable {
			break
		}
		if !validIdentity(segment.Name, 1024, false) || len(segment.Content) == 0 || seenNames[segment.Name] {
			return nil, nil, errors.New("cacheengine: stable segment needs name and content")
		}
		seenNames[segment.Name] = true
		if segment.Tokens < 0 || segment.ExpectedCalls < 0 {
			return nil, nil, errors.New("cacheengine: negative segment measurement")
		}
		if maxBytes < 8 || len(segment.Name) > maxBytes-8 || len(segment.Content) > maxBytes-8-len(segment.Name) || len(prefix) > maxBytes-8-len(segment.Name)-len(segment.Content) {
			return nil, nil, errors.New("cacheengine: stable prefix exceeds configured byte limit")
		}
		if len(segment.Name) > math.MaxUint32 || len(segment.Content) > math.MaxUint32 {
			return nil, nil, errors.New("cacheengine: segment exceeds framing limit")
		}
		prefix = appendFrame(prefix, segment.Name, segment.Content)
		stable = append(stable, segment)
	}
	return prefix, stable, nil
}

func appendFrame(dst []byte, name string, content []byte) []byte {
	var lengths [8]byte
	binary.BigEndian.PutUint32(lengths[:4], uint32(len(name)))
	binary.BigEndian.PutUint32(lengths[4:], uint32(len(content)))
	dst = append(dst, lengths[:]...)
	dst = append(dst, name...)
	return append(dst, content...)
}

func breakpointCandidates(segments []Segment, defaultCalls int, profile Profile) ([]Breakpoint, bool, bool, error) {
	var candidates []Breakpoint
	var prefix []byte
	cumulativeTokens := 0
	previousCalls := math.MaxInt
	belowMinimum := false
	negative := false
	for index, segment := range segments {
		prefix = appendFrame(prefix, segment.Name, segment.Content)
		if segment.Tokens > math.MaxInt-cumulativeTokens {
			return nil, false, false, errors.New("cacheengine: cumulative token count overflow")
		}
		cumulativeTokens += segment.Tokens
		calls := segment.ExpectedCalls
		if calls == 0 {
			calls = defaultCalls
		}
		if calls > previousCalls {
			return nil, false, false, errors.New("cacheengine: longer prefix cannot have higher expected reuse")
		}
		previousCalls = calls
		if calls < 2 {
			continue
		}
		if cumulativeTokens > 0 && cumulativeTokens < profile.MinPrefixTokens {
			belowMinimum = true
			continue
		}
		net := 0.0
		if cumulativeTokens > 0 && profile.EconomicsKnown {
			rawNet := float64(cumulativeTokens) * (float64(calls) - profile.WriteMultiplier - float64(calls-1)*profile.ReadMultiplier)
			if math.IsNaN(rawNet) || math.IsInf(rawNet, 0) {
				return nil, false, false, errors.New("cacheengine: cache economics overflow")
			}
			net = roundUnits(rawNet)
			if net <= 0 {
				negative = true
				continue
			}
		}
		sum := sha256.Sum256(prefix)
		candidate := Breakpoint{
			AfterSegment:              segment.Name,
			PrefixSHA256:              hex.EncodeToString(sum[:]),
			PrefixTokens:              cumulativeTokens,
			ExpectedCalls:             calls,
			BreakEvenCalls:            breakEvenCalls(profile),
			ExpectedNetInputRateUnits: net,
			index:                     index,
		}
		if len(candidates) > 0 && candidates[len(candidates)-1].ExpectedCalls == calls {
			candidates[len(candidates)-1] = candidate
		} else {
			candidates = append(candidates, candidate)
		}
	}
	return candidates, belowMinimum, negative, nil
}

func breakEvenCalls(profile Profile) int {
	if !profile.EconomicsKnown {
		return 0
	}
	for calls := 2; calls <= 10_000; calls++ {
		if float64(calls)-profile.WriteMultiplier-float64(calls-1)*profile.ReadMultiplier > 0 {
			return calls
		}
	}
	return 0
}

func limitBreakpoints(candidates []Breakpoint, limit int) []Breakpoint {
	if len(candidates) <= limit {
		return append([]Breakpoint(nil), candidates...)
	}
	selected := append([]Breakpoint(nil), candidates...)
	sort.SliceStable(selected, func(i, j int) bool {
		if selected[i].ExpectedNetInputRateUnits == selected[j].ExpectedNetInputRateUnits {
			return selected[i].index < selected[j].index
		}
		return selected[i].ExpectedNetInputRateUnits > selected[j].ExpectedNetInputRateUnits
	})
	selected = selected[:limit]
	sort.Slice(selected, func(i, j int) bool { return selected[i].index < selected[j].index })
	return selected
}

func keyShard(request PlanRequest, profile Profile, maxShards int) (count, shard int, capped bool) {
	count = 1
	if request.ExpectedRequestsPerMinute > profile.MaxRPMPerKey {
		count = 1 + (request.ExpectedRequestsPerMinute-1)/profile.MaxRPMPerKey
		if count > maxShards {
			count = maxShards
			capped = true
		}
	}
	partition := request.PartitionKey
	if partition == "" {
		partition = request.Epoch
	}
	sum := sha256.Sum256([]byte(partition))
	shard = int(binary.BigEndian.Uint64(sum[:8]) % uint64(count))
	return count, shard, capped
}

func routingKey(scope, profileID, prefixSHA string, shard int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d", scope, profileID, prefixSHA, shard)))
	return hex.EncodeToString(sum[:16])
}

func epochKey(scope, epoch, profileID string) string {
	sum := sha256.Sum256([]byte(scope + "\x00" + epoch + "\x00" + profileID))
	return hex.EncodeToString(sum[:])
}

func finiteNonNegative(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func roundUnits(value float64) float64 {
	scaled := value * 1e9
	if math.IsInf(scaled, 0) {
		return value
	}
	return math.Round(scaled) / 1e9
}

func allTokenCountsUnavailable(breakpoints []Breakpoint) bool {
	if len(breakpoints) == 0 {
		return false
	}
	for _, breakpoint := range breakpoints {
		if breakpoint.PrefixTokens > 0 {
			return false
		}
	}
	return true
}

type prefixSafetyCache struct {
	mu    sync.Mutex
	safe  map[string]bool
	order []string
	cap   int
}

func newPrefixSafetyCache(capacity int) *prefixSafetyCache {
	return &prefixSafetyCache{safe: make(map[string]bool), cap: capacity}
}

func (c *prefixSafetyCache) inspect(prefix []byte) (string, bool) {
	sum := sha256.Sum256(prefix)
	digest := hex.EncodeToString(sum[:])
	c.mu.Lock()
	knownSafe := c.safe[digest]
	c.mu.Unlock()
	if knownSafe {
		return digest, false
	}
	if len(cacheguard.DetectVolatile(prefix)) > 0 {
		return digest, true
	}
	c.mu.Lock()
	if !c.safe[digest] {
		c.safe[digest] = true
		c.order = append(c.order, digest)
		for len(c.order) > c.cap {
			oldest := c.order[0]
			c.order = c.order[1:]
			delete(c.safe, oldest)
		}
	}
	c.mu.Unlock()
	return digest, false
}
