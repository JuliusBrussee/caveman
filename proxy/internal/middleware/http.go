package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

// negotiated is the client's Caveman-Middleware-Features header intersected with
// this runtime's features (§3). The runtime advertises every 1.1 feature, so the
// header alone decides. Unknown and malformed tokens are ignored.
type negotiated struct {
	// client means the header is present: a protocol 1.1+ client. It gets the
	// 1.1 capabilities view and the tolerant reader (§4), which the server
	// advertises and 1.1 clients rely on without requesting it.
	client           bool
	statusV2         bool
	revisionTolerant bool
}

func negotiate(h http.Header) negotiated {
	values := h.Values(HeaderFeatures)
	n := negotiated{client: len(values) > 0}
	for _, value := range values {
		for _, feature := range strings.Split(value, ",") {
			switch strings.TrimSpace(feature) {
			case FeatureHTTPStatusV2:
				n.statusV2 = true
			case FeatureRevisionTolerant:
				n.revisionTolerant = true
			}
		}
	}
	return n
}

func (r *Runtime) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	start := time.Now()
	o := outcome{route: strings.TrimPrefix(request.URL.Path, RoutePrefix)}
	defer func() { r.record(&o, start) }()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	n := negotiate(request.Header)
	fail := func(err error) { o.status, o.code, o.responseBytes = writeError(w, err, n.statusV2) }
	// A browser page is not an authorized local application. JSON-only bodies
	// and refusing all browser origins prevent drive-by access on loopback.
	if request.Header.Get("Origin") != "" || request.Header.Get("Sec-Fetch-Site") == "cross-site" {
		fail(Failure{CodeForbiddenOrigin})
		return
	}
	principal, err := r.cfg.Identify(request)
	if err != nil || principal.Name == "" {
		fail(Failure{CodeUnauthorized})
		return
	}
	o.principal, o.mechanism, o.client = principal.Name, principal.Mechanism, logSafe(request.Header.Get(HeaderClient))
	if request.URL.Path == RoutePrefix+"capabilities" && request.Method == http.MethodGet {
		caps, _ := r.view(n)
		o.status, o.responseBytes = http.StatusOK, writeJSON(w, http.StatusOK, caps)
		return
	}
	if request.Method != http.MethodPost {
		fail(Failure{CodeNotFound})
		return
	}
	if ok, wait := r.quota.allow(principal.Name, principal.Quota.RequestsPerMinute, r.cfg.Now()); !ok {
		fail(retryAfter{Failure{CodeQuotaExceeded}, wait})
		return
	}
	// retrieve reads originals on its own queue and deadline, so a burst of
	// recoveries cannot starve optimize and a large page is not held to 500ms.
	queue, budget := r.queue, time.Duration(r.cfg.Limits.DeadlineMS)*time.Millisecond
	if o.route == "retrieve" {
		queue, budget = r.retrieveQueue, time.Duration(r.cfg.Limits.RetrieveDeadlineMS)*time.Millisecond
	}
	ctx, cancel := context.WithTimeout(request.Context(), budget)
	defer cancel()
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(budget))
	defer controller.SetReadDeadline(time.Time{})
	select {
	case queue <- struct{}{}:
		defer func() { <-queue }()
	case <-ctx.Done():
		if n.statusV2 {
			fail(Failure{CodeCapacity})
		} else {
			fail(Failure{CodeDeadline}) // 1.0's answer (legacy_conditions)
		}
		return
	}
	contentType, _, _ := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if contentType != "application/json" {
		fail(Failure{CodeInvalidRequest})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, request.Body, int64(r.cfg.Limits.RequestBytes)))
	o.requestBytes = len(body)
	if err != nil {
		// Only an actual size overrun is payload_limit; anything else is a body
		// that did not arrive in time (1.0 called both payload_limit).
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			fail(Failure{CodePayloadLimit})
		} else {
			// Never reuse the connection: net/http would otherwise block
			// discarding the rest of a body that is not coming.
			w.Header().Set("Connection", "close")
			fail(Failure{CodeRequestTimeout})
		}
		return
	}
	if err = ctx.Err(); err != nil {
		fail(err)
		return
	}
	// decode also authorizes: scope points into target, and its namespace must be
	// one the principal may use, on every route (§2).
	decode := func(target any, scope *Scope) error {
		if err := requestPresence(body, request.URL.Path); err != nil {
			return err
		}
		d := json.NewDecoder(bytes.NewReader(body))
		if !n.client {
			d.DisallowUnknownFields() // protocol 1.0 rejected unknown fields
		}
		if err := d.Decode(target); err != nil {
			return Failure{CodeInvalidRequest}
		}
		if d.Decode(new(any)) != io.EOF {
			return Failure{CodeInvalidRequest}
		}
		if !principal.Allows(scope.Namespace) {
			return Failure{CodeForbiddenNamespace}
		}
		return nil
	}
	var out any
	switch request.URL.Path {
	case RoutePrefix + "optimize":
		var req OptimizeRequest
		if err = decode(&req, &req.Scope); err == nil {
			o.scope = short(authority(principal.Name, req.Scope))
			var plan OptimizeResponse
			if plan, err = r.optimize(ctx, principal, req, digest(body), n); err == nil {
				o.planStatus, o.reason = plan.Status, plan.Reason
			}
			out = plan
		}
	case RoutePrefix + "retrieve":
		var req RetrieveRequest
		if err = decode(&req, &req.Scope); err == nil {
			o.scope, o.handle = short(authority(principal.Name, req.Scope)), short(digest([]byte(req.Handle)))
			out, err = r.retrieve(ctx, principal, req, n)
		}
	case RoutePrefix + "receipts":
		if len(body) > r.cfg.Limits.ReceiptBytes {
			err = Failure{CodePayloadLimit}
			break
		}
		var req Receipt
		if err = decode(&req, &req.Scope); err == nil {
			o.scope = short(authority(principal.Name, req.Scope))
			err = r.receipt(ctx, principal, req)
			out = ReceiptResponse{SchemaVersion: ProtocolVersion, Status: "recorded", Basis: "client_observed"}
		}
	case RoutePrefix + "sessions/delete":
		var req SessionDeleteRequest
		if err = decode(&req, &req.Scope); err == nil {
			if req.SchemaVersion != ProtocolVersion || !scopeValid(req.Scope) {
				err = Failure{CodeInvalidRequest}
				break
			}
			o.scope = short(authority(principal.Name, req.Scope))
			var deleted SessionDeleteResponse
			deleted, err = r.deleteSession(ctx, principal, req.Scope)
			o.deleted, out = deleted.Deleted, deleted
		}
	default:
		err = Failure{CodeNotFound}
	}
	if err != nil {
		fail(err)
		return
	}
	o.status, o.responseBytes = http.StatusOK, writeJSON(w, http.StatusOK, out)
}

// short is a log-safe identifier for a digest.
func short(hexDigest string) string { return hexDigest[:16] }

// retryAfter carries a Retry-After longer than the default second.
type retryAfter struct {
	Failure
	seconds int
}

func (e retryAfter) Unwrap() error { return e.Failure }

func writeJSON(w http.ResponseWriter, status int, value any) int {
	b, _ := json.Marshal(value)
	b = append(b, '\n')
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	written, _ := w.Write(b)
	return written
}

// errorCode names the protocol error for err: an explicit Failure, or a
// storage/context condition. Anything else is an outage.
func errorCode(err error) string {
	var failure Failure
	switch {
	case errors.As(err, &failure):
		return failure.Code
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return CodeDeadline
	case errors.Is(err, store.ErrMiddlewareConflict):
		return CodeIdentityConflict
	case errors.Is(err, store.ErrMiddlewareCapacity), errors.Is(err, ccr.ErrBudgetExceeded):
		return CodeCapacity
	}
	return CodeRuntimeUnavailable
}

// statusFor maps a code to its status (§6). Without http_status_v2 a request
// gets 1.0's status, and codes 1.0 never sent become 1.0's nearest answer.
func statusFor(code string, v2 bool) (int, string) {
	switch code {
	case CodeInvalidRequest, CodeUnsupportedVersion, CodeUnknownCapability, CodeInvalidRange:
		return http.StatusBadRequest, code
	case CodeUnauthorized:
		return http.StatusUnauthorized, code
	case CodeForbiddenOrigin, CodeForbiddenNamespace:
		return http.StatusForbidden, code
	case CodeNotFound:
		return http.StatusNotFound, code
	case CodeEpochChanged, CodeIdentityConflict:
		return http.StatusConflict, code
	case CodeDeleted, CodeExpired:
		return http.StatusGone, code
	case CodePayloadLimit:
		return http.StatusRequestEntityTooLarge, code
	case CodeDeadline:
		return http.StatusGatewayTimeout, code
	case CodeRequestTimeout:
		if v2 {
			return http.StatusRequestTimeout, code
		}
		return http.StatusRequestEntityTooLarge, CodePayloadLimit
	case CodeCapacity, CodeQuotaExceeded:
		if v2 {
			return http.StatusTooManyRequests, code
		}
		return http.StatusServiceUnavailable, CodeCapacity
	}
	// runtime_unavailable, recovery_unavailable, and the optimize decisions
	// 1.0 answered as outages: not_smaller, cache_state_unavailable.
	return http.StatusServiceUnavailable, code
}

func writeError(w http.ResponseWriter, err error, v2 bool) (int, string, int) {
	status, code := statusFor(errorCode(err), v2)
	if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
		seconds := 1
		var wait retryAfter
		if errors.As(err, &wait) {
			seconds = max(wait.seconds, 1)
		}
		w.Header().Set(HeaderRetryAfter, strconv.Itoa(seconds))
	}
	return status, code, writeJSON(w, status, ErrorEnvelope{SchemaVersion: ProtocolVersion, Error: Failure{code}})
}
