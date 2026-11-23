package gateway

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
	"github.com/JuliusBrussee/caveman/shared/platform/ssrf"
)

// With no total request deadline (CAVE_GATEWAY_UPSTREAM_TIMEOUT_MS defaults to
// 0) an upstream that connects and then never answers would hang the handler
// forever. The header deadline is the replacement bound; it must not be a body
// deadline, so the streaming suite stays the proof that long responses survive.
func TestDefaultUpstreamClientBoundsResponseHeaders(t *testing.T) {
	t.Setenv("CAVE_GATEWAY_RESPONSE_HEADER_TIMEOUT_MS", "150")
	hang := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-hang:
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	defer close(hang)

	s := New(Config{Adapters: []providers.Adapter{anthropic.New(upstream.URL)}, Auth: stubAuth{}, Creds: stubCreds{key: "sk-test"}})
	transport, ok := s.httpClient.Transport.(*http.Transport)
	if !ok || transport.ResponseHeaderTimeout != 150*time.Millisecond || transport.IdleConnTimeout == 0 {
		t.Fatalf("unbounded default transport: %#v", s.httpClient.Transport)
	}

	done := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-5","messages":[]}`)))
		done <- rec.Code
	}()
	select {
	case code := <-done:
		if code != http.StatusBadGateway {
			t.Fatalf("silent upstream = %d, want 502", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("silent upstream hung the handler: no bound left after the total deadline was removed")
	}
}

// The endpoint pre-flight in the Bedrock and Vertex adapters must be able to see
// that this request leaves through a proxy (#1001). The selector it reads comes
// from the transport that will actually carry the request.
func TestHandlerPublishesUpstreamProxySelectorToAdapters(t *testing.T) {
	// A proxy-only network: nothing in this process can resolve a hostname.
	previousResolver := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("no outbound DNS on this network")
	}}
	t.Cleanup(func() { net.DefaultResolver = previousResolver })

	unresolvable, _ := url.Parse("https://api.anthropic.invalid/v1/messages")
	respond := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})
	for _, tc := range []struct {
		name       string
		proxy      func(*http.Request) (*url.URL, error)
		wantPassed bool
	}{
		{name: "direct client resolves as before"},
		{name: "proxied client skips resolution", proxy: func(*http.Request) (*url.URL, error) {
			return url.Parse("http://proxy.corp.example:3128")
		}, wantPassed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &preflightAdapter{Adapter: anthropic.New("https://api.anthropic.invalid"), endpoint: unresolvable}
			transport := &http.Transport{Proxy: tc.proxy}
			transport.RegisterProtocol("https", respond)
			s := New(Config{Adapters: []providers.Adapter{recorder}, Auth: stubAuth{}, Creds: stubCreds{key: "sk-test"},
				HTTPClient: &http.Client{Transport: transport}})
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-5","messages":[]}`)))
			if !recorder.seen {
				t.Fatal("adapter never ran the pre-flight")
			}
			if passed := recorder.err == nil; passed != tc.wantPassed {
				t.Fatalf("pre-flight error = %v, want passed=%v", recorder.err, tc.wantPassed)
			}
		})
	}
}

// preflightAdapter runs the same managed-mode pre-flight the Bedrock and Vertex
// adapters run, against a host that does not resolve.
type preflightAdapter struct {
	providers.Adapter
	endpoint *url.URL
	seen     bool
	err      error
}

func (a *preflightAdapter) ResolveUpstreamURL(ctx context.Context, r *http.Request, route providers.RouteContext) (*url.URL, error) {
	a.seen = true
	a.err = providers.ValidateUpstreamEndpoint(ctx, a.endpoint, ssrf.ManagedConfig())
	return a.Adapter.ResolveUpstreamURL(ctx, r, route)
}

// Go keeps two idle connections per host by default; eight callers at once
// would then open six new upstream connections every round. The default
// client keeps them all.
func TestDefaultUpstreamClientKeepsConnectionsForConcurrentCallers(t *testing.T) {
	const callers, rounds = 8, 3
	var opened atomic.Int64
	var mu sync.Mutex
	arrived, release := 0, make(chan struct{})
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		if arrived++; arrived == callers {
			close(release) // every caller holds a connection at once
		}
		wait := release
		mu.Unlock()
		<-wait
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatRespBody)
	}))
	upstream.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			opened.Add(1)
		}
	}
	upstream.Start()
	defer upstream.Close()
	s := New(Config{Adapters: []providers.Adapter{openai.New(upstream.URL)}, Auth: stubAuth{rc: RequestContext{RuntimeMode: "record"}}, Creds: stubCreds{key: "sk-test"}})
	for round := 0; round < rounds; round++ {
		mu.Lock()
		arrived, release = 0, make(chan struct{})
		mu.Unlock()
		var done sync.WaitGroup
		for i := 0; i < callers; i++ {
			done.Go(func() {
				rec := httptest.NewRecorder()
				s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatReqBody)))
				if rec.Code != http.StatusOK {
					t.Errorf("status %d: %s", rec.Code, rec.Body.String())
				}
			})
		}
		done.Wait()
	}
	if got := opened.Load(); got != callers {
		t.Fatalf("%d callers over %d rounds opened %d upstream connections, want %d", callers, rounds, got, callers)
	}
}

// blackholeConn wraps a live TCP connection and, once blackholed, silently
// drops all outbound writes (mimicking a dead NAT entry or vanished IPv6
// prefix where neither RST nor FIN arrives) until Close tears down the socket.
type blackholeConn struct {
	net.Conn
	blackholed atomic.Bool
	closed     atomic.Bool
}

func (c *blackholeConn) Write(p []byte) (int, error) {
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	if c.blackholed.Load() {
		return len(p), nil
	}
	return c.Conn.Write(p)
}

func (c *blackholeConn) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

// #1543: a pooled HTTP/2 upstream connection that dies silently (no RST, no
// FIN) must not hang requests until the 15-minute header deadline or poison
// every subsequent request on the pool.
func TestDeadPooledHTTP2ConnFailsFastAndRecovers(t *testing.T) {
	const reqBody = `{"model":"claude-sonnet-5","messages":[]}`
	const respBody = `{"id":"msg","type":"message","model":"claude-sonnet-5","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`

	newTestGateway := func(t *testing.T) (*Server, *atomic.Int64, func() *blackholeConn) {
		t.Helper()
		upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, respBody)
		}))
		upstream.EnableHTTP2 = true
		upstream.StartTLS()
		t.Cleanup(upstream.Close)

		var dials atomic.Int64
		var mu sync.Mutex
		var lastConn *blackholeConn

		transport := upstream.Client().Transport.(*http.Transport).Clone()
		transport.ForceAttemptHTTP2 = true
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			raw, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			dials.Add(1)
			bc := &blackholeConn{Conn: raw}
			mu.Lock()
			lastConn = bc
			mu.Unlock()
			return bc, nil
		}
		BoundUpstreamTransport(transport)
		if transport.HTTP2 != nil && transport.HTTP2.SendPingTimeout > 0 && transport.HTTP2.PingTimeout > 0 {
			// Compress the production 15s/5s health-check timers so the test
			// proves real HTTP/2 ping eviction in milliseconds.
			transport.HTTP2.SendPingTimeout = 50 * time.Millisecond
			transport.HTTP2.PingTimeout = 50 * time.Millisecond
		}
		t.Cleanup(transport.CloseIdleConnections)

		s := New(Config{
			Adapters:   []providers.Adapter{anthropic.New(upstream.URL)},
			Auth:       stubAuth{rc: RequestContext{RuntimeMode: "record"}},
			Creds:      stubCreds{key: "sk-test"},
			HTTPClient: &http.Client{Transport: transport},
		})
		return s, &dials, func() *blackholeConn {
			mu.Lock()
			defer mu.Unlock()
			return lastConn
		}
	}

	call := func(t *testing.T, s *Server, timeout time.Duration) (int, bool) {
		t.Helper()
		done := make(chan int, 1)
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		go func() {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reqBody)).WithContext(ctx)
			s.Handler().ServeHTTP(rec, req)
			done <- rec.Code
		}()
		select {
		case code := <-done:
			return code, false
		case <-time.After(timeout):
			cancel()
			<-done
			return 0, true
		}
	}

	t.Run("default transport enables HTTP/2 ping health checks", func(t *testing.T) {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		BoundUpstreamTransport(transport)
		if transport.HTTP2 == nil || transport.HTTP2.SendPingTimeout != 15*time.Second || transport.HTTP2.PingTimeout != 5*time.Second {
			t.Fatalf("HTTP2 config = %+v, want SendPingTimeout=15s PingTimeout=5s", transport.HTTP2)
		}
	})

	t.Run("in-flight request on dead pooled conn fails fast and next request recovers", func(t *testing.T) {
		s, dials, lastConn := newTestGateway(t)

		if code, hung := call(t, s, 2*time.Second); hung || code != http.StatusOK {
			t.Fatalf("warm request: code=%d hung=%v, want 200", code, hung)
		}
		if got := dials.Load(); got != 1 {
			t.Fatalf("warm dials = %d, want 1", got)
		}

		// Silently drop all packets on the pooled HTTP/2 connection.
		lastConn().blackholed.Store(true)

		code, hung := call(t, s, 2*time.Second)
		if hung {
			t.Fatal("request on dead pooled HTTP/2 conn hung past deadline")
		}
		if code != http.StatusBadGateway {
			t.Fatalf("request on dead pooled conn = %d, want 502", code)
		}

		// Dead conn must be evicted so the next request dials fresh and succeeds.
		if code, hung := call(t, s, 2*time.Second); hung || code != http.StatusOK {
			t.Fatalf("recovery request: code=%d hung=%v, want 200", code, hung)
		}
		if got := dials.Load(); got != 2 {
			t.Fatalf("recovery dials = %d, want 2", got)
		}
	})

	t.Run("idle dead pooled conn is evicted before next request", func(t *testing.T) {
		s, dials, lastConn := newTestGateway(t)

		if code, hung := call(t, s, 2*time.Second); hung || code != http.StatusOK {
			t.Fatalf("warm request: code=%d hung=%v, want 200", code, hung)
		}

		lastConn().blackholed.Store(true)
		// Wait for SendPingTimeout + PingTimeout so background health check evicts it.
		time.Sleep(250 * time.Millisecond)

		code, hung := call(t, s, 2*time.Second)
		if hung {
			t.Fatal("request after idle conn death hung past deadline")
		}
		if code != http.StatusOK {
			t.Fatalf("request after idle eviction = %d, want 200", code)
		}
		if got := dials.Load(); got != 2 {
			t.Fatalf("dials after idle eviction = %d, want 2", got)
		}
	})
}

type closeIdleTrackingTransport struct {
	roundTrip  func(*http.Request) (*http.Response, error)
	closedIdle atomic.Int64
}

func (t *closeIdleTrackingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.roundTrip(r)
}

func (t *closeIdleTrackingTransport) CloseIdleConnections() {
	t.closedIdle.Add(1)
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "read tcp: connection timed out" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// #1543: when an upstream request or stream dies with a network timeout (such
// as a vanished IPv6 source prefix), sibling idle connections in the pool are
// likely bound to the same dead address and must be dropped immediately.
func TestUpstreamTimeoutEvictsIdlePool(t *testing.T) {
	t.Run("doUpstream timeout closes idle connections", func(t *testing.T) {
		tr := &closeIdleTrackingTransport{
			roundTrip: func(*http.Request) (*http.Response, error) {
				return nil, timeoutErr{}
			},
		}
		s := New(Config{HTTPClient: &http.Client{Transport: tr}})
		_, err := s.doUpstream(context.Background(), func() (*http.Request, error) {
			return http.NewRequest(http.MethodPost, "https://provider.test/v1/messages", strings.NewReader(`{}`))
		})
		if err == nil {
			t.Fatal("expected timeout error")
		}
		if tr.closedIdle.Load() == 0 {
			t.Fatal("upstream timeout did not close idle connections")
		}
	})

	t.Run("stream read timeout closes idle connections", func(t *testing.T) {
		tr := &closeIdleTrackingTransport{}
		s := New(Config{HTTPClient: &http.Client{Transport: tr}})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		_, code := s.streamResponse(rec, req, errReader{err: timeoutErr{}}, true, "req-1", nil)
		if code != "cave_upstream_body_read_failed" {
			t.Fatalf("code = %q, want cave_upstream_body_read_failed", code)
		}
		if tr.closedIdle.Load() == 0 {
			t.Fatal("stream read timeout did not close idle connections")
		}
	})
}
