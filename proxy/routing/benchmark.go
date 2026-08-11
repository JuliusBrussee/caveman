package routing

import (
	"crypto/sha256"
	"encoding/binary"
	"strings"
)

const (
	BenchmarkFixedRouterVersion      = "benchmark-fixed-v1"
	BenchmarkRandomRouterVersion     = "benchmark-random-v1"
	BenchmarkRoundRobinRouterVersion = "benchmark-round-robin-v1"
)

// BenchmarkPolicy is a trusted, project-policy-only execution contract for
// fixed and negative-control benchmark arms. It is not selected by request
// content or headers. SystemArtifactHash binds telemetry to the precommitted
// executable/config artifact; it never affects selection.
type BenchmarkPolicy struct {
	Enabled            bool   `json:"enabled"`
	SystemArtifactHash string `json:"system_artifact_hash"`
	FixedActionID      string `json:"fixed_action_id,omitempty"`
	RandomSeed         string `json:"random_seed,omitempty"`
}

type BenchmarkContext struct {
	SessionID string
	TurnIndex int
}

type BenchmarkRouter struct {
	Version string
	Policy  BenchmarkPolicy
}

// PickSession makes baseline assignment reproducible from locked metadata.
// Candidates are ordered by action ID so config/YAML ordering cannot alter a
// round-robin or random arm after candidate_pool_hash was committed.
func (r BenchmarkRouter) PickSession(f Features, pool []Candidate, ctx BenchmarkContext) (Decision, error) {
	poolHash := CandidatePoolHash(pool)
	base := Decision{RouterVersion: r.Version, CandidatePoolHash: poolHash}
	if !r.Policy.Enabled {
		base.Reason = "benchmark_policy_disabled"
		return base, nil
	}
	if !validBenchmarkArtifactHash(r.Policy.SystemArtifactHash) {
		base.Reason = "benchmark_artifact_hash_invalid"
		return base, nil
	}
	if !f.BodyModelRewrite {
		base.Reason = "route_requires_body_model"
		return base, nil
	}
	if strings.TrimSpace(ctx.SessionID) == "" || ctx.TurnIndex < 0 || len(pool) == 0 {
		base.Reason = "benchmark_context_incomplete"
		return base, nil
	}
	ranked := append([]Candidate(nil), pool...)
	sortCandidatesByActionID(ranked)
	index := -1
	switch r.Version {
	case BenchmarkFixedRouterVersion:
		for i, candidate := range ranked {
			if CandidateActionID(candidate) == r.Policy.FixedActionID {
				index = i
				break
			}
		}
		if index < 0 {
			base.Reason = "benchmark_fixed_action_outside_pool"
			return base, nil
		}
	case BenchmarkRoundRobinRouterVersion:
		index = ctx.TurnIndex % len(ranked)
	case BenchmarkRandomRouterVersion:
		if strings.TrimSpace(r.Policy.RandomSeed) == "" {
			base.Reason = "benchmark_random_seed_missing"
			return base, nil
		}
		index = benchmarkRandomIndex(r.Policy.RandomSeed, ctx.SessionID, ctx.TurnIndex, len(ranked))
	default:
		base.Reason = "unknown_benchmark_router_version"
		return base, nil
	}
	chosen := ranked[index]
	return Decision{
		Provider: chosen.Provider, Model: chosen.Model, Effort: chosen.Effort,
		ActionID: CandidateActionID(chosen), Ranked: ranked,
		Reason: "benchmark_precommitted_action", RouterVersion: r.Version,
		CandidatePoolHash: poolHash,
	}, nil
}

func validBenchmarkArtifactHash(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, r := range strings.TrimPrefix(value, "sha256:") {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func benchmarkRandomIndex(seed, sessionID string, turnIndex, size int) int {
	hash := sha256.New()
	var length [8]byte
	for _, field := range []string{seed, sessionID} {
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(field))
	}
	binary.BigEndian.PutUint64(length[:], uint64(turnIndex))
	_, _ = hash.Write(length[:])
	return int(binary.BigEndian.Uint64(hash.Sum(nil)[:8]) % uint64(size))
}

func sortCandidatesByActionID(candidates []Candidate) {
	// Small benchmark pools; insertion sort keeps this file dependency-light and
	// makes action identity—not catalog/config order—the sole ordering contract.
	for i := 1; i < len(candidates); i++ {
		for j := i; j > 0 && CandidateActionID(candidates[j]) < CandidateActionID(candidates[j-1]); j-- {
			candidates[j], candidates[j-1] = candidates[j-1], candidates[j]
		}
	}
}
