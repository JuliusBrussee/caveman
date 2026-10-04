package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeMiddleware stands in for middleware.Runtime, which gateway cannot import.
type fakeMiddleware struct{ readyErr error }

func (fakeMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusUnauthorized)
}
func (f fakeMiddleware) Ready(context.Context) error { return f.readyErr }
func (fakeMiddleware) WriteMetrics(w io.Writer) {
	_, _ = io.WriteString(w, "# TYPE caveman_middleware_requests_total counter\n")
}

func get(t *testing.T, srv *Server, path, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// Middleware 401s bypass rejectUnauthorized; the mount must still count them,
// and /metrics must describe every series.
func TestMiddlewareUnauthorizedCountedAndMetricsTyped(t *testing.T) {
	srv := New(Config{Middleware: fakeMiddleware{}})
	if rec := get(t, srv, "/caveman/v1/middleware/capabilities", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	metrics := get(t, srv, "/metrics", "").Body.String()
	for _, want := range []string{
		"# TYPE cave_proxy_inflight_requests gauge", "# HELP cave_proxy_unauthorized_total ",
		"# TYPE cave_proxy_unauthorized_total counter\ncave_proxy_unauthorized_total 1\n",
		"# TYPE caveman_middleware_requests_total counter",
	} {
		if !strings.Contains(metrics, want) {
			t.Fatalf("metrics lack %q:\n%s", want, metrics)
		}
	}
}

func TestMetricsTokenGatesMetricsOnly(t *testing.T) {
	srv := New(Config{MetricsToken: "metrics-secret-value"})
	for bearer, want := range map[string]int{"": 401, "wrong": 401, "metrics-secret-value": 200} {
		if rec := get(t, srv, "/metrics", bearer); rec.Code != want {
			t.Fatalf("bearer %q: status %d, want %d", bearer, rec.Code, want)
		}
	}
	if rec := get(t, srv, "/health/ready", ""); rec.Code != http.StatusOK {
		t.Fatalf("metrics token gated readiness: %d", rec.Code)
	}
}

// closedAuth is a listener whose provider routes refuse every request.
type closedAuth struct{}

func (closedAuth) Authenticate(context.Context, *http.Request) (RequestContext, error) {
	return RequestContext{}, errors.New("closed")
}
func (closedAuth) RefusesAll() bool { return true }

// A middleware store outage degrades readiness only where the middleware is all
// the listener serves; provider inference keeps its replicas in the Service.
func TestReadinessReflectsMiddlewareStore(t *testing.T) {
	down := fakeMiddleware{readyErr: errors.New("attempt to write a readonly database")}
	for _, tc := range []struct {
		middleware http.Handler
		auth       Authenticator
		status     int
		state      string
	}{
		{nil, nil, 200, "unavailable"},
		{fakeMiddleware{}, nil, 200, "ok"},
		{down, nil, 200, "degraded"},
		{fakeMiddleware{}, closedAuth{}, 200, "ok"},
		{down, closedAuth{}, 503, "degraded"},
	} {
		rec := get(t, New(Config{Middleware: tc.middleware, Auth: tc.auth}), "/health/ready", "")
		var body struct {
			OK         bool   `json:"ok"`
			Middleware string `json:"middleware"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != tc.status || body.Middleware != tc.state || body.OK != (tc.status == 200) {
			t.Fatalf("ready = %d %s, want %d %s", rec.Code, rec.Body, tc.status, tc.state)
		}
	}
	if rec := get(t, New(Config{Middleware: fakeMiddleware{readyErr: errors.New("down")}}), "/health/live", ""); rec.Code != 200 {
		t.Fatal("liveness followed middleware readiness")
	}
}
