package middleware

import (
	"slices"
	"strings"
)

func token(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._:-/", c)) {
			return false
		}
	}
	return true
}
func hash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func scopeValid(s Scope) bool {
	return token(s.Namespace) && token(s.SessionID) && token(s.BranchID) && token(s.CacheEpoch)
}

func (r *Runtime) validate(req OptimizeRequest, n negotiated) error {
	if req.SchemaVersion != ProtocolVersion {
		return Failure{CodeUnsupportedVersion}
	}
	if !scopeValid(req.Scope) || !token(req.RequestID) || !token(req.LogicalCallID) || !token(req.AttemptID) || !token(req.IdempotencyKey) ||
		!token(req.Adapter.ID) || !token(req.Adapter.Version) || !token(req.Adapter.FrameworkVersion) || !token(req.Adapter.SerializationRevision) || req.Sequence < 0 || req.Sequence > 9007199254740991 ||
		(req.Mode != "record" && req.Mode != "compress") || len(req.Segments) > r.cfg.Limits.MaxSegments || len(req.ContextManifest) > r.cfg.Limits.MaxManifestItems {
		return Failure{CodeInvalidRequest}
	}
	if req.Model != nil && (!token(req.Model.Provider) || !token(req.Model.ID) || !token(req.Model.Protocol)) {
		return Failure{CodeInvalidRequest}
	}
	// revision_tolerant: every requested transform is currently advertised to
	// this request's view, whatever revision the client cached (§5). Otherwise
	// the revision must also match exactly, as in 1.0.
	caps, transforms := r.view(n)
	if !n.revisionTolerant && req.Policy.Revision != caps.PolicyRevision {
		return Failure{CodeUnknownCapability}
	}
	if len(req.Policy.Transforms) > len(transforms) {
		return Failure{CodeUnknownCapability}
	}
	seen := map[string]bool{}
	for _, id := range req.Policy.Transforms {
		if _, ok := transforms[id]; !ok || seen[id] {
			return Failure{CodeUnknownCapability}
		}
		seen[id] = true
	}
	seen = map[string]bool{}
	for _, s := range req.Segments {
		if !token(s.ID) || !token(s.SourceID) || !hash(s.SHA256) || s.SHA256 != digest([]byte(s.Content)) || seen[s.ID] ||
			!slices.Contains([]string{"instruction", "user_intent", "tool_schema", "skill", "memory", "history", "tool_result", "artifact", "error", "output_contract"}, s.Kind) ||
			!slices.Contains([]string{"frozen_prefix", "live_zone", "uncached"}, s.CacheRegion) {
			return Failure{CodeInvalidRequest}
		}
		seen[s.ID] = true
	}
	seen = map[string]bool{}
	for _, item := range req.ContextManifest {
		if !token(item.ID) || !hash(item.SHA256) || seen[item.ID] {
			return Failure{CodeInvalidRequest}
		}
		seen[item.ID] = true
	}
	if b := req.RecoveryBinding; b != nil {
		if !token(b.ID) || !slices.Contains([]string{"host_tool", "source_reader"}, b.Kind) || b.ToolName != RecoveryToolName || len(b.OverheadText) > 32768 {
			if n.statusV2 {
				return Failure{CodeInvalidRequest}
			}
			return Failure{CodeRecoveryUnavailable} // 1.0's answer (legacy_conditions)
		}
	}
	return nil
}
