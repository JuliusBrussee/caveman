package middleware

import (
	"context"
	"encoding/json"
	"slices"
	"unicode/utf8"

	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

func (r *Runtime) retrieve(ctx context.Context, principal string, req RetrieveRequest) (RetrieveResponse, error) {
	response := RetrieveResponse{SchemaVersion: ProtocolVersion, Handle: req.Handle, Kind: "original_page"}
	if req.SchemaVersion != ProtocolVersion {
		return response, Failure{"unsupported_version"}
	}
	if !scopeValid(req.Scope) || !token(req.Handle) || req.Offset < 0 || req.Limit < 0 || len(req.Query) > 1024 {
		return response, Failure{"invalid_request"}
	}
	if req.Limit == 0 {
		req.Limit = r.cfg.Limits.PageBytes
	}
	if req.Limit > r.cfg.Limits.PageBytes || req.Limit < 4 {
		return response, Failure{"payload_limit"}
	}
	if r.cfg.Recovery == nil {
		return response, Failure{"recovery_unavailable"}
	}
	auth := authority(principal, req.Scope)
	err := r.cfg.Store.WithMiddleware(ctx, func(tx *store.MiddlewareTx) error {
		body, handle, expires, err := tx.Grant(auth, req.Handle)
		if err != nil {
			return Failure{"not_found"}
		}
		if expires <= 0 {
			return Failure{"deleted"}
		}
		if expires <= r.cfg.Now().Unix() {
			return Failure{"expired"}
		}
		var choice Replacement
		if json.Unmarshal(body, &choice) != nil {
			return Failure{"recovery_unavailable"}
		}
		original, err := r.eng.Retrieve(handle)
		if err != nil || digest(original) != choice.OriginalSHA256 {
			return Failure{"recovery_unavailable"}
		}
		response.SourceID = choice.SourceID
		response.OriginalSHA256 = choice.OriginalSHA256
		response.TotalBytes = len(original)
		data := original
		if req.Query != "" {
			if req.Offset != 0 {
				return Failure{"invalid_range"}
			}
			data, err = r.eng.RetrieveQuery(handle, req.Query)
			if err != nil {
				return Failure{"recovery_unavailable"}
			}
			response.Kind = "excerpt"
		}
		if req.Offset > len(data) || (req.Offset < len(data) && !utf8.RuneStart(data[req.Offset])) {
			return Failure{"invalid_range"}
		}
		end := min(len(data), req.Offset+req.Limit)
		for end < len(data) && !utf8.RuneStart(data[end]) {
			end--
		}
		response.Text = string(data[req.Offset:end])
		response.Offset = req.Offset
		response.Complete = req.Query == "" && req.Offset == 0 && end == len(data)
		if end < len(data) {
			response.NextOffset = &end
		}
		if req.Query != "" {
			start := 0
			response.NextOffset = &start
		}
		return tx.Renew(auth, r.cfg.Now().Unix(), r.cfg.Now().Unix()+r.caps.RetentionSeconds)
	})
	return response, err
}

func (r *Runtime) receipt(ctx context.Context, principal string, req Receipt) error {
	if req.SchemaVersion != ProtocolVersion {
		return Failure{"unsupported_version"}
	}
	if !scopeValid(req.Scope) || !token(req.LogicalCallID) || !token(req.AttemptID) || (req.PlanID != nil && !hash(*req.PlanID)) ||
		!slices.Contains([]string{"dispatch_intent", "completed", "failed", "cancelled"}, req.EventKind) {
		return Failure{"invalid_request"}
	}
	if req.ProviderRequestSHA256 != nil && !hash(*req.ProviderRequestSHA256) {
		return Failure{"invalid_request"}
	}
	if u := req.Usage; u != nil {
		if req.EventKind != "completed" || u.Provenance != "client_observed_sdk" {
			return Failure{"invalid_request"}
		}
		for _, n := range []*int64{u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens, u.ReasoningTokens} {
			if n != nil && (*n < 0 || *n > 9007199254740991) {
				return Failure{"invalid_request"}
			}
		}
		if u.Complete && (u.InputTokens == nil || u.OutputTokens == nil) {
			return Failure{"invalid_request"}
		}
	}
	b, _ := json.Marshal(req)
	// Receipts are authority-keyed observations that can outlive every scope
	// that produced them (a bypassed optimize writes no scope at all), so they
	// carry their own retention rather than riding scope expiry.
	expires := r.cfg.Now().Unix() + store.MiddlewareGraceSeconds
	return r.cfg.Store.WithMiddleware(ctx, func(tx *store.MiddlewareTx) error {
		return tx.Receipt(authority(principal, req.Scope), identity(req.LogicalCallID, req.AttemptID, req.EventKind), digest(b), b, expires)
	})
}
