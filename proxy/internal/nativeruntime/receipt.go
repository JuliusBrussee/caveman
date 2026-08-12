package nativeruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/proxy/internal/sessionusage"
)

const ReceiptSchema = "caveman.native.receipt.v1"

type ReceiptMetric struct {
	Value  int64  `json:"value"`
	Unit   string `json:"unit"`
	Basis  string `json:"basis"`
	Detail string `json:"detail,omitempty"`
}

type Receipt struct {
	Schema        string                   `json:"schema"`
	SessionID     string                   `json:"session_id"`
	HostSession   string                   `json:"host_session_id,omitempty"`
	Agent         string                   `json:"agent"`
	GeneratedAt   time.Time                `json:"generated_at"`
	Outcome       map[string]string        `json:"outcome"`
	Execution     map[string]ReceiptMetric `json:"execution"`
	ProviderUsage sessionusage.Snapshot    `json:"provider_usage"`
	Policy        map[string]any           `json:"policy"`
	Claims        map[string]string        `json:"claim_status"`
}

func (r *Runtime) writeReceipt(request Request) (*Receipt, error) {
	objects, err := r.store.ListSessionObjects(request.Session.ID, 10000)
	if err != nil {
		return nil, err
	}
	var events, repeats, loopInterventions, recordedBytes, maskCandidateBytes, runtimeUS, repositoryFiles, evidenceBundles int64
	var activeSkill, skillEvidenceStatus string
	var testSucceeded, testFailed, buildSucceeded, buildFailed bool
	decisionIDs := make([]string, 0)
	for _, object := range objects {
		switch object.Type {
		case ccr.ObjectTaskContract:
			if object.Currentness == ccr.Current {
				var contract struct {
					ActiveSkill         string `json:"active_skill"`
					SkillEvidenceStatus string `json:"skill_evidence_status"`
				}
				if json.Unmarshal(object.Data, &contract) == nil {
					activeSkill = contract.ActiveSkill
					skillEvidenceStatus = contract.SkillEvidenceStatus
				}
			}
		case ccr.ObjectTaskDecision:
			events++
			var decision struct {
				DecisionID  string `json:"decision_id"`
				Action      string `json:"action"`
				Reason      string `json:"reason"`
				RecoveryRef string `json:"recovery_ref"`
				RuntimeUS   int64  `json:"runtime_us"`
				InputBasis  struct {
					ToolType   string `json:"tool_type"`
					ToolResult string `json:"tool_result"`
				} `json:"input_basis"`
			}
			if json.Unmarshal(object.Data, &decision) == nil {
				if decision.DecisionID != "" {
					decisionIDs = append(decisionIDs, decision.DecisionID)
				}
				if decision.Reason == "current_observation_reused" || decision.Reason == "repeat_hint_suppressed" || decision.Reason == "verified_repeat_loop_intervention" {
					repeats++
				}
				if decision.Reason == "verified_repeat_loop_intervention" {
					loopInterventions++
				}
				runtimeUS += decision.RuntimeUS
				switch decision.InputBasis.ToolType {
				case string(ccr.ObjectTestResult):
					testSucceeded = testSucceeded || decision.InputBasis.ToolResult == "succeeded"
					testFailed = testFailed || decision.InputBasis.ToolResult == "failed"
				case string(ccr.ObjectBuildResult):
					buildSucceeded = buildSucceeded || decision.InputBasis.ToolResult == "succeeded"
					buildFailed = buildFailed || decision.InputBasis.ToolResult == "failed"
				}
				if decision.Reason == "exact_output_mask_available" {
					if recovered, getErr := r.store.GetObject(trimCCRReference(decision.RecoveryRef)); getErr == nil {
						maskCandidateBytes += int64(recovered.OriginalByteLength)
					}
				}
			}
		case ccr.ObjectFileObservation, ccr.ObjectSearchResult, ccr.ObjectCommandResult,
			ccr.ObjectTestResult, ccr.ObjectBuildResult, ccr.ObjectDiffSnapshot,
			ccr.ObjectDocumentationExcerpt, ccr.ObjectBrowserSnapshot:
			recordedBytes += int64(object.OriginalByteLength)
		case ccr.ObjectRepositoryMap:
			var repositoryMap struct {
				Files []json.RawMessage `json:"files"`
			}
			if json.Unmarshal(object.Data, &repositoryMap) == nil {
				repositoryFiles = int64(len(repositoryMap.Files))
			}
		case ccr.ObjectEvidenceBundle:
			evidenceBundles++
		}
	}
	usage := sessionusage.Snapshot{
		Status:       "not_available",
		SessionID:    request.Session.ID,
		CostBasis:    "catalog_list_price_subtotal_provider_complete_priced_rows",
		SavingsBasis: "inferred_standalone_not_verified",
	}
	if r.usage != nil {
		if observed, usageErr := r.usage.SessionUsage(request.Session.ID); usageErr == nil {
			usage = observed
		} else {
			usage.Status = "temporarily_unavailable"
		}
	}
	compressionClaim := "not_attested_by_native_runtime"
	exactCorrelation := usage.CorrelationBasis == "" || usage.CorrelationBasis == "signed_marker" || usage.CorrelationBasis == "explicit_header"
	if usage.Status == "correlated" && exactCorrelation && usage.CompressionTokensBefore > usage.CompressionTokensAfter {
		compressionClaim = "inferred_correlated_" + firstNonEmpty(usage.CompressionTokenCountBasis, "unspecified_token_count")
	} else if usage.Status == "correlated" && !exactCorrelation {
		compressionClaim = "not_attested_due_approximate_session_correlation"
	}
	activePolicy := []string{"event-recording", "decision-ledger"}
	switch request.Profile {
	case "core":
		activePolicy = append(activePolicy, "core")
	case "core-lean-build":
		activePolicy = append(activePolicy, "core", "lean-build")
	case "ledger":
		activePolicy = append(activePolicy, "core", "task-contract", "inform-ledger", "typed-ccr")
	case "ccr-masking":
		activePolicy = append(activePolicy, "core", "typed-ccr", "threshold-masking")
	case "cache-aware":
		activePolicy = append(activePolicy, "core", "task-contract", "cache-aware-prompt")
	case "full-safe", "full-max":
		activePolicy = append(activePolicy, "core", "task-contract", "inform-ledger", "typed-ccr", "threshold-masking", "cache-aware-prompt", "repository-intelligence")
	}
	if activeSkill != "" {
		activePolicy = append(activePolicy, activeSkill)
	}
	outcome := map[string]string{
		"verifier": "not_observed",
		"tests":    observedOutcome(testSucceeded, testFailed),
	}
	if buildSucceeded || buildFailed {
		outcome["builds"] = observedOutcome(buildSucceeded, buildFailed)
	}
	receipt := &Receipt{
		Schema:      ReceiptSchema,
		SessionID:   request.Session.ID,
		HostSession: request.Session.HostSessionID,
		Agent:       request.Agent.ID,
		GeneratedAt: time.Now().UTC(),
		Outcome:     outcome,
		Execution: map[string]ReceiptMetric{
			"native_events": {
				Value: events, Unit: "events", Basis: "observed",
			},
			"repeated_actions_detected": {
				Value: repeats, Unit: "actions", Basis: "observed",
			},
			"repeat_loop_interventions": {
				Value: loopInterventions, Unit: "interventions", Basis: "observed_deterministic_third_repeat",
			},
			"exact_context_recorded": {
				Value: recordedBytes, Unit: "bytes", Basis: "observed",
			},
			"mask_candidate_context": {
				Value: maskCandidateBytes, Unit: "bytes", Basis: "observed",
				Detail: "eligible by deterministic byte threshold; host application not attested",
			},
			"native_runtime_handler": {
				Value: runtimeUS, Unit: "microseconds", Basis: "observed_handler_before_ledger_write",
			},
			"repository_files_mapped": {
				Value: repositoryFiles, Unit: "files", Basis: "observed_local_repository_metadata",
			},
			"repository_evidence_bundles": {
				Value: evidenceBundles, Unit: "bundles", Basis: "observed_local_repository_metadata",
			},
		},
		ProviderUsage: usage,
		Policy: map[string]any{
			"mode":                   firstNonEmpty(request.PolicyMode, "safe"),
			"profile":                firstNonEmpty(request.Profile, "record-only"),
			"active":                 activePolicy,
			"unresolved_assumptions": 0,
			"decision_ids":           decisionIDs,
			"active_skill":           firstNonEmpty(activeSkill, "none"),
			"skill_evidence_status":  firstNonEmpty(skillEvidenceStatus, "not_applicable"),
		},
		Claims: map[string]string{
			"compression_reduction": compressionClaim,
			"task_savings":          "not_verified_without_paired_baseline",
		},
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(r.receiptDir, 0o700); err != nil {
		return nil, fmt.Errorf("native runtime receipt mkdir: %w", err)
	}
	if err := os.Chmod(r.receiptDir, 0o700); err != nil {
		return nil, fmt.Errorf("native runtime receipt chmod: %w", err)
	}
	sum := sha256.Sum256([]byte(request.Session.ID))
	name := "session-" + hex.EncodeToString(sum[:12]) + ".json"
	if err := atomicReceipt(filepath.Join(r.receiptDir, name), data); err != nil {
		return nil, err
	}
	if err := atomicReceipt(filepath.Join(r.receiptDir, "latest.json"), data); err != nil {
		return nil, err
	}
	return receipt, nil
}

func observedOutcome(succeeded, failed bool) string {
	if failed {
		return "failed_observed_host_event"
	}
	if succeeded {
		return "passed_observed_host_event"
	}
	return "not_observed"
}

func compactReceiptLine(receipt *Receipt) string {
	if receipt == nil {
		return ""
	}
	tests := receipt.Outcome["tests"]
	switch tests {
	case "passed_observed_host_event":
		tests = "tests passed"
	case "failed_observed_host_event":
		tests = "tests failed"
	default:
		tests = "verification not observed"
	}
	repeats := receipt.Execution["repeated_actions_detected"].Value
	contextBytes := receipt.Execution["exact_context_recorded"].Value
	return fmt.Sprintf("Caveman · %s · %d repeats detected · %d B exact context recorded", tests, repeats, contextBytes)
}

func trimCCRReference(value string) string {
	const prefix = "ccr://"
	if len(value) >= len(prefix) && value[:len(prefix)] == prefix {
		return value[len(prefix):]
	}
	return value
}

func atomicReceipt(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".receipt-*.tmp")
	if err != nil {
		return fmt.Errorf("native runtime receipt temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("native runtime receipt rename: %w", err)
	}
	return nil
}
