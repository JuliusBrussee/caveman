package middleware

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/JuliusBrussee/caveman/engine"
	ident "github.com/JuliusBrussee/caveman/proxy/internal/identity"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

// retrieve never takes the metadata writer: the grant and its original come
// from one read snapshot, decryption and paging run outside any transaction,
// and the sliding renewal is batched (Run).
func (r *Runtime) retrieve(ctx context.Context, principal ident.Principal, req RetrieveRequest, n negotiated) (RetrieveResponse, error) {
	response := RetrieveResponse{SchemaVersion: ProtocolVersion, Handle: req.Handle, Kind: "original_page"}
	if req.SchemaVersion != ProtocolVersion {
		return response, Failure{CodeUnsupportedVersion}
	}
	if !scopeValid(req.Scope) || !token(req.Handle) || req.Offset < 0 || req.Limit < 0 || len(req.Query) > 1024 {
		return response, Failure{CodeInvalidRequest}
	}
	if req.Limit == 0 {
		req.Limit = r.cfg.Limits.PageBytes
	}
	if req.Limit > r.cfg.Limits.PageBytes || req.Limit < 4 {
		if n.statusV2 {
			return response, Failure{CodeInvalidRange}
		}
		return response, Failure{CodePayloadLimit}
	}
	auth := authority(principal.Name, req.Scope)
	var choice Replacement
	var handle, keyID string
	var sealed []byte
	err := r.cfg.Store.ReadMiddleware(ctx, func(tx *store.MiddlewareTx) error {
		body, h, expires, err := tx.Grant(auth, req.Handle)
		if errors.Is(err, sql.ErrNoRows) {
			return Failure{CodeNotFound}
		}
		if err != nil {
			return err // a storage failure is runtime_unavailable, never not_found
		}
		if expires <= 0 {
			return Failure{CodeDeleted}
		}
		if expires <= r.cfg.Now().Unix() {
			return Failure{CodeExpired}
		}
		if json.Unmarshal(body, &choice) != nil {
			return Failure{CodeRecoveryUnavailable}
		}
		if handle = h; handle != "" {
			return nil // issued by protocol 1.0: the original is in CCR
		}
		sealed, keyID, err = tx.Original(auth, choice.OriginalSHA256)
		if errors.Is(err, sql.ErrNoRows) {
			return Failure{CodeRecoveryUnavailable}
		}
		return err
	})
	if err != nil {
		return response, err
	}
	var original []byte
	if handle != "" {
		original, err = r.eng.Retrieve(handle)
	} else {
		original, err = r.cfg.Keys.open(auth, choice.OriginalSHA256, sealed, keyID)
		if keyID == "" && r.cfg.Keys != nil {
			if err != nil {
				r.metrics.plaintextRefused.Add(1)
			} else {
				r.metrics.plaintextAllowed.Add(1)
			}
		}
	}
	if err != nil || digest(original) != choice.OriginalSHA256 {
		if err != nil {
			r.warn("middleware original unreadable", err)
		}
		return response, Failure{CodeRecoveryUnavailable}
	}
	response.SourceID = choice.SourceID
	response.OriginalSHA256 = choice.OriginalSHA256
	response.TotalBytes = len(original)
	data := original
	if req.Query != "" {
		if req.Offset != 0 {
			return response, Failure{CodeInvalidRange}
		}
		if data, err = r.narrow(handle, original, req.Query); err != nil {
			return response, Failure{CodeRecoveryUnavailable}
		}
		response.Kind = "excerpt"
	}
	if req.Offset > len(data) || (req.Offset < len(data) && !utf8.RuneStart(data[req.Offset])) {
		return response, Failure{CodeInvalidRange}
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
	r.renew(auth)
	return response, nil
}

// narrow runs the Engine's query ranking: through CCR for a grant protocol 1.0
// issued, directly on a middleware-owned original otherwise, with
// RetrieveQuery's rule that only a strictly shorter view replaces the original.
func (r *Runtime) narrow(handle string, original []byte, query string) ([]byte, error) {
	if handle != "" {
		return r.eng.RetrieveQuery(handle, query)
	}
	if strings.TrimSpace(query) == "" {
		return original, nil
	}
	if narrowed, ok := engine.NarrowToQuery(original, query); ok && len(narrowed) < len(original) {
		return narrowed, nil
	}
	return original, nil
}

// deleteSession revokes the request's authority and deletes everything it owns
// (§12). Grants protocol 1.0 issued keep their original in the shared CCR,
// which this runtime cannot delete, so originals_deleted says so.
//
// The revocation commits first, so every later use answers "deleted" however
// large the session. Its content then goes in bounded batches, each its own
// transaction: a delete that runs out of time answers an error, and its retry
// resumes where it stopped (the sweep finishes one nobody retries). The counts
// are what this call's batches removed: nothing holds the authority between
// them (SQLite has no per-authority lock; Postgres's ends with each batch), so
// a concurrent sweep may remove the rest and the counts under-report.
func (r *Runtime) deleteSession(ctx context.Context, principal ident.Principal, scope Scope) (SessionDeleteResponse, error) {
	auth := authority(principal.Name, scope)
	var deleted store.MiddlewareDeleted
	err := r.write(ctx, principal, func(tx *store.MiddlewareTx) error {
		var err error
		deleted, err = tx.Revoke(auth, r.cfg.Now().Unix())
		return err
	})
	for more := err == nil; more; {
		err = r.write(ctx, principal, func(tx *store.MiddlewareTx) error {
			batch, next, err := tx.PurgeBatch(auth, deleteBatch)
			if err == nil {
				deleted.Add(batch)
				more = next
			}
			return err
		})
		more = more && err == nil
	}
	// A failed delete is 503 (§12): a write conflict is not the caller's
	// concurrent-writer condition to settle.
	if errors.Is(err, store.ErrMiddlewareConflict) {
		err = Failure{CodeRuntimeUnavailable}
	}
	// Every choice row carries exactly one grant (grant_id is its unique key),
	// so the grants revoked are the choices revoked.
	return SessionDeleteResponse{SchemaVersion: ProtocolVersion, Status: "revoked", OriginalsDeleted: deleted.Legacy == 0,
		Deleted: &DeleteCounts{Scopes: deleted.Scopes, Choices: deleted.Choices, Grants: deleted.Choices, Originals: deleted.Originals}}, err
}

// deleteBatch bounds one sessions/delete purge transaction (see
// store.MiddlewarePurgeBatch).
const deleteBatch = store.MiddlewarePurgeBatch

func (r *Runtime) receipt(ctx context.Context, principal ident.Principal, req Receipt) error {
	if req.SchemaVersion != ProtocolVersion {
		return Failure{CodeUnsupportedVersion}
	}
	if !scopeValid(req.Scope) || !token(req.LogicalCallID) || !token(req.AttemptID) || (req.PlanID != nil && !hash(*req.PlanID)) ||
		!slices.Contains([]string{"dispatch_intent", "completed", "failed", "cancelled"}, req.EventKind) {
		return Failure{CodeInvalidRequest}
	}
	if req.ProviderRequestSHA256 != nil && !hash(*req.ProviderRequestSHA256) {
		return Failure{CodeInvalidRequest}
	}
	if u := req.Usage; u != nil {
		if req.EventKind != "completed" || u.Provenance != "client_observed_sdk" {
			return Failure{CodeInvalidRequest}
		}
		for _, n := range []*int64{u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens, u.ReasoningTokens} {
			if n != nil && (*n < 0 || *n > 9007199254740991) {
				return Failure{CodeInvalidRequest}
			}
		}
		if u.Complete && (u.InputTokens == nil || u.OutputTokens == nil) {
			return Failure{CodeInvalidRequest}
		}
	}
	b, _ := json.Marshal(req)
	// Receipts are authority-keyed observations that can outlive every scope
	// that produced them (a bypassed optimize writes no scope at all), so they
	// carry their own expiry: retention, never longer (§12). sessions/delete
	// removes them with the rest of the authority.
	expires := r.cfg.Now().Unix() + int64(r.cfg.Retention.Seconds())
	auth := authority(principal.Name, req.Scope)
	return r.write(ctx, principal, func(tx *store.MiddlewareTx) error {
		// A revoked authority stores nothing new (§12).
		revoked, err := tx.Revoked(auth)
		if err != nil {
			return err
		}
		if revoked {
			return Failure{CodeDeleted}
		}
		return tx.Receipt(auth, identity(req.LogicalCallID, req.AttemptID, req.EventKind), digest(b), b, expires)
	})
}
