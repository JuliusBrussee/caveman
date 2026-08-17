package evals

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	ProbeRetained    = "retained"
	ProbeRecoverable = "recoverable"
	ProbeLost        = "lost"
)

// RetentionProbe names one critical value that a transform must keep visible or
// make byte-exactly recoverable. Values come from committed fixture manifests;
// reports carry only their hashes so hidden-suite answers are not disclosed.
type RetentionProbe struct {
	Dimension string `yaml:"dimension"`
	Value     string `yaml:"value"`
}

// ProbeResult is one classified probe. ValueSHA256 identifies which committed value
// was checked without copying hidden fixture answers into signed public reports.
type ProbeResult struct {
	Dimension   string `json:"dimension"`
	ValueSHA256 string `json:"value_sha256"`
	State       string `json:"state"`
}

// ProbeTally is a retained/recoverable/lost count.
type ProbeTally struct {
	Total       int `json:"total"`
	Retained    int `json:"retained"`
	Recoverable int `json:"recoverable"`
	Lost        int `json:"lost"`
}

// ProbeSummary aggregates probe outcomes without erasing per-fixture detail.
type ProbeSummary struct {
	ProbeTally
	ByDimension map[string]ProbeTally `json:"by_dimension,omitempty"`
}

func classifyProbes(probes []RetentionProbe, output []byte, recoverable bool) ([]ProbeResult, ProbeSummary, []string) {
	if len(probes) == 0 {
		return nil, ProbeSummary{}, nil
	}
	results := make([]ProbeResult, 0, len(probes))
	summary := ProbeSummary{ByDimension: map[string]ProbeTally{}}
	var failures []string
	for i, probe := range probes {
		value := probe.Value
		if value == "" {
			failures = append(failures, fmt.Sprintf("retention probe %d has empty value", i))
			continue
		}
		dimension := strings.TrimSpace(probe.Dimension)
		if dimension == "" {
			dimension = "unspecified"
		}
		state := ProbeLost
		if bytes.Contains(output, []byte(value)) {
			state = ProbeRetained
		} else if recoverable {
			state = ProbeRecoverable
		}
		sum := sha256.Sum256([]byte(value))
		results = append(results, ProbeResult{
			Dimension:   dimension,
			ValueSHA256: "sha256:" + hex.EncodeToString(sum[:]),
			State:       state,
		})
		addProbeState(&summary.ProbeTally, state)
		tally := summary.ByDimension[dimension]
		addProbeState(&tally, state)
		summary.ByDimension[dimension] = tally
		if state == ProbeLost {
			failures = append(failures, fmt.Sprintf("retention probe %s was lost", dimension))
		}
	}
	return results, summary, failures
}

func addProbeState(tally *ProbeTally, state string) {
	tally.Total++
	switch state {
	case ProbeRetained:
		tally.Retained++
	case ProbeRecoverable:
		tally.Recoverable++
	case ProbeLost:
		tally.Lost++
	}
}

func mergeProbeSummary(dst *ProbeSummary, src ProbeSummary) {
	dst.Total += src.Total
	dst.Retained += src.Retained
	dst.Recoverable += src.Recoverable
	dst.Lost += src.Lost
	if len(src.ByDimension) == 0 {
		return
	}
	if dst.ByDimension == nil {
		dst.ByDimension = map[string]ProbeTally{}
	}
	for dimension, srcTally := range src.ByDimension {
		dstTally := dst.ByDimension[dimension]
		dstTally.Total += srcTally.Total
		dstTally.Retained += srcTally.Retained
		dstTally.Recoverable += srcTally.Recoverable
		dstTally.Lost += srcTally.Lost
		dst.ByDimension[dimension] = dstTally
	}
}
