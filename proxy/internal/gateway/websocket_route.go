package gateway

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/shared/platform/httpx"
	"github.com/JuliusBrussee/caveman/shared/platform/redact"
)

// Responses WebSocket mode uses the same URL and bearer credential as HTTP:
// https://developers.openai.com/api/reference/resources/responses/methods/connect
// Only the OpenAI adapter's two exact Responses paths can upgrade. Other APIs,
// compat mounts, methods, and encoded paths retain their existing route policy.
func (s *Server) matchResponsesWebSocket(r *http.Request) providers.Adapter {
	if r.Method != http.MethodGet || r.URL.RawPath != "" ||
		(r.URL.Path != "/v1/responses" && r.URL.Path != "/openai/v1/responses") ||
		!strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || !hasUpgradeToken(r.Header) {
		return nil
	}
	for _, adapter := range s.adapters {
		if adapter.Name() == "openai" && adapter.MatchRoute(http.MethodPost, r.URL.Path) {
			return adapter
		}
	}
	return nil
}

func hasUpgradeToken(headers http.Header) bool {
	_, ok := connectionTokens(headers)["upgrade"]
	return ok
}

// responsesWebSocket forwards the handshake and then opaque bytes in both
// directions. It never parses, captures, transforms, retries, or meters frames.
// ReverseProxy uses the configured transport directly: SSRF dial guards, outbound
// proxies, TLS roots, and handshake bounds remain in force; redirects are not
// followed. Its upgrade copier propagates cancellation and either peer's close.
func (s *Server) responsesWebSocket(w http.ResponseWriter, r *http.Request, adapter providers.Adapter, requestID string) {
	// Native API clients use Authorization headers. Browsers can open a local
	// WebSocket without CORS preflight, while loopback proxy auth may be optional;
	// never let a web page borrow the operator's configured provider credential.
	for name := range r.Header {
		if strings.EqualFold(name, "Origin") || strings.HasPrefix(strings.ToLower(name), "sec-fetch-") {
			httpx.Error(w, r, http.StatusForbidden, "cave_websocket_origin_forbidden", "Browser-origin WebSocket connections are not supported.")
			return
		}
	}
	key, err := base64.StdEncoding.DecodeString(r.Header.Get("Sec-WebSocket-Key"))
	if err != nil || len(key) != 16 || r.Header.Get("Sec-WebSocket-Version") != "13" ||
		r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		httpx.Error(w, r, http.StatusBadRequest, "cave_invalid_websocket", "Invalid WebSocket opening handshake.")
		return
	}
	ctx := r.Context()
	if s.httpClient.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.httpClient.Timeout)
		defer cancel()
	}
	ctx, finish, ok := s.websockets.start(ctx)
	if !ok {
		httpx.Error(w, r, http.StatusServiceUnavailable, "cave_proxy_stopping", "Proxy is shutting down.")
		return
	}
	defer finish()
	r = r.WithContext(ctx)
	upstream, err := adapter.ResolveUpstreamURL(ctx, r, providers.RouteContext{})
	if err != nil {
		httpx.Error(w, r, http.StatusBadGateway, "cave_upstream_unavailable", "Upstream route could not be resolved.")
		return
	}
	credential := s.creds.Resolve(adapter.Name(), r)
	headers, err := adapter.SanitizeAndMapHeaders(ctx, r, credential, upstream)
	if err != nil {
		providerHeaderError(w, r, err)
		return
	}
	s.applyUpstreamAuthFallback(adapter.Name(), credential, headers)
	for _, name := range []string{"Content-Type", "Content-Length", "Content-Encoding", "Accept-Encoding"} {
		headers.Del(name)
	}
	nominated := connectionTokens(r.Header)
	for _, name := range []string{"Sec-WebSocket-Key", "Sec-WebSocket-Version", "Sec-WebSocket-Protocol", "Sec-WebSocket-Extensions", "OpenAI-Beta"} {
		if !unsafeForwardHeader(name, nominated) {
			headers[http.CanonicalHeaderKey(name)] = append([]string(nil), r.Header.Values(name)...)
		}
	}
	headers.Set("Connection", "Upgrade")
	headers.Set("Upgrade", "websocket")
	transport := s.httpClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	var responseBody io.ReadCloser
	defer func() {
		// Also closes a 101 response when hijacking the client fails, before the
		// standard library's duplex copier can take ownership of its backend.
		if responseBody != nil {
			_ = responseBody.Close()
		}
	}()
	proxy := httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(p *httputil.ProxyRequest) {
			urlCopy := *upstream
			p.Out.URL = &urlCopy
			p.Out.Host = upstream.Host
			p.Out.Header = headers.Clone()
			p.Out.Body = nil
			p.Out.ContentLength = 0
			p.Out.TransferEncoding = nil
		},
		ModifyResponse: func(resp *http.Response) error {
			responseBody = resp.Body
			upgraded := resp.StatusCode == http.StatusSwitchingProtocols
			if upgraded {
				if !strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") || !hasUpgradeToken(resp.Header) {
					return errors.New("upstream returned an invalid WebSocket upgrade")
				}
				duplex, ok := resp.Body.(io.ReadWriteCloser)
				if !ok {
					return errors.New("upstream WebSocket response is not duplex")
				}
				resp.Body = websocketDuplexBody{duplex}
			}
			clean := make(http.Header)
			copySafeResponseHeaders(clean, resp.Header)
			if upgraded {
				clean.Set("Connection", "Upgrade")
				clean.Set("Upgrade", "websocket")
			}
			resp.Header = clean
			for name := range resp.Trailer {
				if unsafeForwardHeader(name, nil) {
					delete(resp.Trailer, name)
				}
			}
			return nil
		},
		ErrorLog: log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			if s.logger != nil {
				s.logger.Warn("provider WebSocket failed", "error", redact.Error(err), "request_id", requestID)
			}
			httpx.Error(w, req, http.StatusBadGateway, "cave_upstream_unavailable", "Upstream provider unavailable.")
		},
	}
	s.inflight.Add(1)
	defer s.inflight.Add(-1)
	writer := &websocketResponseWriter{ResponseWriter: w, ctx: ctx}
	defer writer.close()
	proxy.ServeHTTP(writer, r)
}

// ReverseProxy cancellation closes the upstream, but a blocked frontend write
// or read can outlive that close. Own the hijacked client too, so shutdown always
// unblocks both copy directions, including when a client stops reading frames.
type websocketResponseWriter struct {
	http.ResponseWriter
	ctx  context.Context
	conn net.Conn
	stop func() bool
}

func (w *websocketResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *websocketResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.conn = conn
		w.stop = context.AfterFunc(w.ctx, func() { _ = conn.Close() })
	}
	if err != nil {
		return conn, rw, err
	}
	return websocketClientConn{conn}, rw, nil
}

// WebSockets do not use TCP half-close. Hide optional CloseWrite methods from
// ReverseProxy so either peer's EOF ends the whole stream instead of retaining
// an idle copier waiting for the other peer to finish a half-closed connection.
type websocketClientConn struct{ net.Conn }
type websocketDuplexBody struct{ io.ReadWriteCloser }

func (w *websocketResponseWriter) close() {
	if w.stop != nil {
		w.stop()
	}
	if w.conn != nil {
		_ = w.conn.Close()
	}
}

// http.Server.Shutdown does not own hijacked connections. Keep each handshake
// and upgraded handler registered until its duplex copier returns. Close blocks
// new registrations, cancels all remaining streams, then waits for them to exit.
type websocketSessions struct {
	mu     sync.Mutex
	closed bool
	active map[*context.CancelFunc]struct{}
	wg     sync.WaitGroup
}

func (s *websocketSessions) start(parent context.Context) (context.Context, func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(parent)
	if s.active == nil {
		s.active = make(map[*context.CancelFunc]struct{})
	}
	s.active[&cancel] = struct{}{}
	s.wg.Add(1)
	return ctx, func() {
		cancel()
		s.mu.Lock()
		delete(s.active, &cancel)
		s.mu.Unlock()
		s.wg.Done()
	}, true
}

func (s *websocketSessions) close() {
	s.mu.Lock()
	s.closed = true
	for cancel := range s.active {
		(*cancel)()
	}
	s.mu.Unlock()
	s.wg.Wait()
}
