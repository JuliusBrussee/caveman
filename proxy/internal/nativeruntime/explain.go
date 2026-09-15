package nativeruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/JuliusBrussee/caveman/engine/ccr"
)

const WhySchema = "caveman.native.why.v1"

var decisionIDPattern = regexp.MustCompile(`^dec_[0-9a-f]{24}$`)

// DecisionExplanation is deterministic user-facing projection of one Decision
// Ledger record. It contains decision basis, never raw prompt or tool output.
type DecisionExplanation struct {
	Schema               string         `json:"schema"`
	DecisionID           string         `json:"decision_id"`
	SessionID            string         `json:"session_id"`
	TimestampMS          int64          `json:"timestamp_ms"`
	Action               string         `json:"action"`
	Reason               string         `json:"reason"`
	InputBasis           map[string]any `json:"input_basis"`
	AlternativesRejected []string       `json:"alternatives_rejected"`
	TaskStateBefore      string         `json:"task_state_before"`
	TaskStateAfter       string         `json:"task_state_after"`
	Currentness          string         `json:"currentness"`
	RecoveryRef          string         `json:"recovery_ref,omitempty"`
}

func ExplainDecision(store *ccr.Store, decisionID string) (DecisionExplanation, error) {
	if !decisionIDPattern.MatchString(decisionID) {
		return DecisionExplanation{}, errors.New("native runtime: invalid decision id")
	}
	object, err := store.FindTaskDecision(decisionID)
	if err != nil {
		return DecisionExplanation{}, err
	}
	var record struct {
		Schema               string         `json:"schema"`
		DecisionID           string         `json:"decision_id"`
		TimestampMS          int64          `json:"timestamp_ms"`
		Action               string         `json:"action"`
		Reason               string         `json:"reason"`
		InputBasis           map[string]any `json:"input_basis"`
		AlternativesRejected []string       `json:"alternatives_rejected"`
		TaskStateBefore      string         `json:"task_state_before"`
		TaskStateAfter       string         `json:"task_state_after"`
		Currentness          string         `json:"currentness"`
		RecoveryRef          string         `json:"recovery_ref"`
	}
	if err := json.Unmarshal(object.Data, &record); err != nil {
		return DecisionExplanation{}, fmt.Errorf("native runtime: decode decision: %w", err)
	}
	if record.Schema != "caveman.native.decision.v1" || record.DecisionID != decisionID || record.Action == "" || record.Reason == "" {
		return DecisionExplanation{}, errors.New("native runtime: invalid decision record")
	}
	return DecisionExplanation{
		Schema:               WhySchema,
		DecisionID:           record.DecisionID,
		SessionID:            object.SessionID,
		TimestampMS:          record.TimestampMS,
		Action:               record.Action,
		Reason:               record.Reason,
		InputBasis:           record.InputBasis,
		AlternativesRejected: record.AlternativesRejected,
		TaskStateBefore:      record.TaskStateBefore,
		TaskStateAfter:       record.TaskStateAfter,
		Currentness:          record.Currentness,
		RecoveryRef:          record.RecoveryRef,
	}, nil
}
