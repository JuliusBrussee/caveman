package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

func (r *Runtime) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// A browser page is not an authorized local application. JSON-only bodies
	// and refusing all browser origins prevent drive-by access on loopback.
	if request.Header.Get("Origin") != "" || request.Header.Get("Sec-Fetch-Site") == "cross-site" {
		writeError(w, Failure{"forbidden_origin"})
		return
	}
	principal, err := r.cfg.Principal(request)
	if err != nil || principal == "" {
		writeError(w, Failure{"unauthorized"})
		return
	}
	if request.URL.Path == RoutePrefix+"capabilities" && request.Method == http.MethodGet {
		writeJSON(w, 200, r.caps)
		return
	}
	if request.Method != http.MethodPost {
		writeError(w, Failure{"not_found"})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), time.Duration(r.cfg.Limits.DeadlineMS)*time.Millisecond)
	defer cancel()
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(time.Duration(r.cfg.Limits.DeadlineMS) * time.Millisecond))
	defer controller.SetReadDeadline(time.Time{})
	select {
	case r.queue <- struct{}{}:
		defer func() { <-r.queue }()
	case <-ctx.Done():
		writeError(w, ctx.Err())
		return
	}
	contentType, _, _ := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if contentType != "application/json" {
		writeError(w, Failure{"invalid_request"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, request.Body, int64(r.cfg.Limits.RequestBytes)))
	if err != nil {
		writeError(w, Failure{"payload_limit"})
		return
	}
	if err = ctx.Err(); err != nil {
		writeError(w, err)
		return
	}
	decode := func(target any) error {
		if err := requestPresence(body, request.URL.Path); err != nil {
			return err
		}
		d := json.NewDecoder(bytes.NewReader(body))
		d.DisallowUnknownFields()
		if err := d.Decode(target); err != nil {
			return Failure{"invalid_request"}
		}
		if d.Decode(new(any)) != io.EOF {
			return Failure{"invalid_request"}
		}
		return nil
	}
	var out any
	switch request.URL.Path {
	case RoutePrefix + "optimize":
		var req OptimizeRequest
		if err = decode(&req); err == nil {
			out, err = r.optimize(ctx, principal, req, digest(body))
		}
	case RoutePrefix + "retrieve":
		var req RetrieveRequest
		if err = decode(&req); err == nil {
			out, err = r.retrieve(ctx, principal, req)
		}
	case RoutePrefix + "receipts":
		if len(body) > 16384 {
			err = Failure{"payload_limit"}
			break
		}
		var req Receipt
		if err = decode(&req); err == nil {
			err = r.receipt(ctx, principal, req)
			out = map[string]any{"schema_version": 1, "status": "recorded", "basis": "client_observed", "verified_saved_usd": 0}
		}
	case RoutePrefix + "sessions/delete":
		var req struct {
			SchemaVersion int   `json:"schema_version"`
			Scope         Scope `json:"scope"`
		}
		if err = decode(&req); err == nil {
			if req.SchemaVersion != ProtocolVersion || !scopeValid(req.Scope) {
				err = Failure{"invalid_request"}
				break
			}
			now := r.cfg.Now().Unix()
			err = r.cfg.Store.WithMiddleware(ctx, func(tx *store.MiddlewareTx) error { return tx.Delete(authority(principal, req.Scope), now) })
			out = map[string]any{"schema_version": 1, "status": "revoked", "originals_deleted": false}
		}
	default:
		err = Failure{"not_found"}
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, err error) {
	code, status := "runtime_unavailable", http.StatusServiceUnavailable
	var failure Failure
	switch {
	case errors.As(err, &failure):
		code = failure.Code
		switch code {
		case "unauthorized":
			status = 401
		case "forbidden_origin":
			status = 403
		case "not_found":
			status = 404
		case "deleted", "expired":
			status = 410
		case "epoch_changed":
			status = 409
		case "payload_limit":
			status = 413
		case "invalid_request", "unsupported_version", "unknown_capability", "invalid_range":
			status = 400
		}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code = "deadline"
		status = 504
	case errors.Is(err, store.ErrMiddlewareConflict):
		code = "identity_conflict"
		status = 409
	case errors.Is(err, store.ErrMiddlewareCapacity), errors.Is(err, ccr.ErrBudgetExceeded):
		code = "capacity"
	}
	writeJSON(w, status, map[string]any{"schema_version": ProtocolVersion, "error": Failure{code}})
}
