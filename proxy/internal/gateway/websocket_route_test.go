package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

const websocketTestKey = "dGhlIHNhbXBsZSBub25jZQ=="

func websocketRequest(t *testing.T, target string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Connection", "keep-alive, Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Key", websocketTestKey)
	req.Header.Set("Sec-WebSocket-Version", "13")
	return req
}

func dialWebSocket(t *testing.T, req *http.Request) (net.Conn, *bufio.Reader, *http.Response) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", req.URL.Host, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		t.Fatal(err)
	}
	return conn, reader, resp
}

func upgradeWebSocket(t *testing.T, w http.ResponseWriter, r *http.Request) (net.Conn, *bufio.ReadWriter) {
	t.Helper()
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		t.Error(err)
		return nil, nil
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	accept := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	_, _ = fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\nSec-WebSocket-Protocol: responses\r\nX-Request-Id: upstream-ws\r\nX-Cave-Private: drop\r\n\r\n", base64.StdEncoding.EncodeToString(accept[:]))
	if err := rw.Flush(); err != nil {
		t.Error(err)
	}
	return conn, rw
}

func maskedWebSocketFrame(opcode byte, payload string) []byte {
	mask := []byte{1, 2, 3, 4}
	frame := append([]byte{0x80 | opcode, 0x80 | byte(len(payload))}, mask...)
	for i := range payload {
		frame = append(frame, payload[i]^mask[i%4])
	}
	return frame
}

func websocketServer(upstream string, client *http.Client) *Server {
	return New(Config{
		Adapters: []providers.Adapter{openai.New(upstream)},
		Auth:     stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}},
		Creds:    stubCreds{key: "sk-byok"}, HTTPClient: client,
	})
}

func waitWebSocketDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("WebSocket handler did not exit")
	}
}

func TestResponsesWebSocketTransparentDuplex(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/openai/v1/responses", "/w/codex/openai/v1/responses"} {
		t.Run(path, func(t *testing.T) {
			captureDir := t.TempDir()
			t.Setenv("CAVE_CAPTURE_DIR", captureDir)
			requestFrame := maskedWebSocketFrame(1, `{"type":"response.create","input":"unchanged"}`)
			// Fragmented text, a ping, binary bytes, and a close frame all stay opaque.
			responseFrames := []byte{0x01, 2, 'o', 'k', 0x80, 1, '!', 0x89, 1, '?', 0x82, 3, 0, 255, 1, 0x88, 2, 3, 232}
			backendDone := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(backendDone)
				if r.URL.RequestURI() != "/v1/responses?test=a%2Fb" || r.Header.Get("Authorization") != "Bearer sk-byok" {
					t.Errorf("wrong upstream route/auth: %s %q", r.URL, r.Header.Get("Authorization"))
				}
				for _, name := range []string{"OpenAI-Organization", "OpenAI-Project", "OpenAI-Beta", "Sec-WebSocket-Protocol"} {
					if r.Header.Get(name) != "responses" {
						t.Errorf("missing %s: %q", name, r.Header.Get(name))
					}
				}
				for _, name := range []string{"X-Cave-Private", "X-Forwarded-For", "Cookie", "X-Secret-Hop"} {
					if r.Header.Get(name) != "" {
						t.Errorf("private header forwarded: %s", name)
					}
				}
				conn, rw := upgradeWebSocket(t, w, r)
				if conn == nil {
					return
				}
				defer conn.Close()
				got := make([]byte, len(requestFrame))
				if _, err := io.ReadFull(rw, got); err != nil || !bytes.Equal(got, requestFrame) {
					t.Errorf("client frame changed: %x err=%v", got, err)
					return
				}
				_, _ = conn.Write(responseFrames)
			}))
			defer upstream.Close()
			var roundTrips atomic.Int32
			transport := http.DefaultTransport.(*http.Transport).Clone()
			defer transport.CloseIdleConnections()
			srv := websocketServer(upstream.URL, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				roundTrips.Add(1)
				return transport.RoundTrip(r)
			})})
			comp, sink := &liveZoneCompressor{}, &captureSink{}
			srv.compressor, srv.sink = comp, sink
			defer srv.Close()
			handlerDone := make(chan struct{})
			frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(handlerDone)
				srv.Handler().ServeHTTP(w, r)
			}))
			defer frontend.Close()
			req := websocketRequest(t, frontend.URL+path+"?test=a%2Fb")
			req.Header.Set("Connection", "Upgrade, X-Secret-Hop, Authorization")
			for _, name := range []string{"OpenAI-Organization", "OpenAI-Project", "OpenAI-Beta", "Sec-WebSocket-Protocol"} {
				req.Header.Set(name, "responses")
			}
			for _, name := range []string{"X-Cave-Private", "X-Forwarded-For", "Cookie", "X-Secret-Hop", "Authorization"} {
				req.Header.Set(name, "must-not-forward")
			}
			conn, reader, resp := dialWebSocket(t, req)
			if resp.StatusCode != 101 || resp.Header.Get("Sec-WebSocket-Accept") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" || resp.Header.Get("Sec-WebSocket-Protocol") != "responses" || resp.Header.Get("X-Request-Id") != "upstream-ws" || resp.Header.Get("X-Cave-Private") != "" {
				t.Fatalf("invalid relayed handshake: %d %v", resp.StatusCode, resp.Header)
			}
			if _, err := conn.Write(requestFrame); err != nil {
				t.Fatal(err)
			}
			got := make([]byte, len(responseFrames))
			if _, err := io.ReadFull(reader, got); err != nil || !bytes.Equal(got, responseFrames) {
				t.Fatalf("server frames changed: %x err=%v", got, err)
			}
			if _, err := reader.ReadByte(); err == nil {
				t.Fatal("upstream close did not reach client")
			}
			waitWebSocketDone(t, backendDone)
			waitWebSocketDone(t, handlerDone)
			files, err := os.ReadDir(captureDir)
			if err != nil || len(files) != 0 || comp.calls != 0 || len(sink.rows) != 0 || roundTrips.Load() != 1 || srv.inflight.Load() != 0 {
				t.Fatalf("WebSocket entered capture/transform/accounting/retry path: files=%d calls=%d rows=%d trips=%d inflight=%d err=%v", len(files), comp.calls, len(sink.rows), roundTrips.Load(), srv.inflight.Load(), err)
			}
		})
	}
}

func TestResponsesWebSocketClientCloseAndServerShutdown(t *testing.T) {
	for _, closeServer := range []bool{false, true} {
		t.Run(fmt.Sprintf("server_close=%v", closeServer), func(t *testing.T) {
			backendDone, handlerDone := make(chan struct{}), make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(backendDone)
				conn, rw := upgradeWebSocket(t, w, r)
				if conn != nil {
					defer conn.Close()
					_, _ = io.Copy(io.Discard, rw)
				}
			}))
			defer upstream.Close()
			srv := websocketServer(upstream.URL, upstream.Client())
			defer srv.Close()
			frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(handlerDone)
				srv.Handler().ServeHTTP(w, r)
			}))
			defer frontend.Close()
			conn, reader, resp := dialWebSocket(t, websocketRequest(t, frontend.URL+"/v1/responses"))
			if resp.StatusCode != 101 {
				t.Fatalf("handshake failed: %d", resp.StatusCode)
			}
			if closeServer {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := frontend.Config.Shutdown(ctx); err != nil {
					t.Fatal(err)
				}
				closed := make(chan struct{})
				go func() { _ = srv.Close(); close(closed) }()
				waitWebSocketDone(t, closed)
				if _, err := reader.ReadByte(); err == nil {
					t.Fatal("shutdown left client connection open")
				}
				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, websocketRequest(t, "/v1/responses"))
				if rec.Code != http.StatusServiceUnavailable {
					t.Errorf("closed server admitted upgrade: %d", rec.Code)
				}
			} else {
				_ = conn.Close()
			}
			waitWebSocketDone(t, backendDone)
			waitWebSocketDone(t, handlerDone)
			if srv.inflight.Load() != 0 {
				t.Fatal("WebSocket handler leaked")
			}
		})
	}
}

func TestResponsesWebSocketRejectedHandshake(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusTemporaryRedirect} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "12")
				w.Header().Set("X-Request-Id", "rejected-upgrade")
				w.Header().Set("X-Cave-Private", "drop")
				w.Header().Set("Location", "/must-not-follow")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":"provider rejection"}`)
			}))
			defer upstream.Close()
			srv := websocketServer(upstream.URL, upstream.Client())
			defer srv.Close()
			frontend := httptest.NewServer(srv.Handler())
			defer frontend.Close()
			_, _, resp := dialWebSocket(t, websocketRequest(t, frontend.URL+"/v1/responses"))
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil || resp.StatusCode != status || string(body) != `{"error":"provider rejection"}` || resp.Header.Get("Retry-After") != "12" || resp.Header.Get("X-Request-Id") != "rejected-upgrade" || resp.Header.Get("X-Cave-Private") != "" || calls.Load() != 1 {
				t.Fatalf("provider rejection changed/replayed: status=%d headers=%v body=%s calls=%d err=%v", resp.StatusCode, resp.Header, body, calls.Load(), err)
			}
		})
	}
}

func TestResponsesWebSocketExactAuthenticatedRoutes(t *testing.T) {
	rt := &captureTransport{}
	srv := websocketServer("https://upstream.test", &http.Client{Transport: rt})
	creds := &websocketCredentialCounter{}
	srv.creds = creds
	defer srv.Close()
	for _, path := range []string{"/v1/responses/", "/v1/responses/input_tokens", "/v1/chat/completions", "/compat/openai-compatible/v1/responses", "/anthropic/v1/responses", "/v1/%72esponses"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, websocketRequest(t, path))
		if rec.Code != http.StatusNotFound {
			t.Errorf("unexpected WebSocket route %s: %d", path, rec.Code)
		}
	}
	srv.auth = rejectingAuth{}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, websocketRequest(t, "/v1/responses"))
	if rec.Code != http.StatusUnauthorized || len(rt.bodies) != 0 {
		t.Fatalf("WebSocket bypassed local auth/route gates: %d trips=%d", rec.Code, len(rt.bodies))
	}
	// A non-upgrade GET must not become inference, and malformed upgrade input
	// must not consume provider credentials or open an upstream connection.
	srv.auth = stubAuth{}
	for _, name := range []string{"Origin", "Sec-Fetch-Site", "Sec-Fetch-Mode"} {
		req := websocketRequest(t, "/v1/responses")
		req.Header.Set(name, "https://untrusted.example")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || creds.calls != 0 {
			t.Errorf("browser handshake borrowed provider authority: status=%d credentials=%d", rec.Code, creds.calls)
		}
	}
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Header.Del("Sec-WebSocket-Key") },
		func(r *http.Request) { r.Header.Set("Sec-WebSocket-Version", "12") },
		func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("x")); r.ContentLength = 1 },
	} {
		req := websocketRequest(t, "/v1/responses")
		mutate(req)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("malformed WebSocket handshake admitted: %d", rec.Code)
		}
	}
	if len(rt.bodies) != 0 || creds.calls != 0 {
		t.Fatal("rejected handshake reached upstream")
	}
}

type websocketCredentialCounter struct{ calls int }

func (c *websocketCredentialCounter) Resolve(string, *http.Request) providers.Credential {
	c.calls++
	return providers.Credential{Mode: "ephemeral_header", Key: "sk-byok"}
}

func TestResponsesWebSocketCloseUnblocksStalledClient(t *testing.T) {
	front, client := net.Pipe()
	back, provider := net.Pipe()
	defer client.Close()
	defer provider.Close()
	blockedWrite := make(chan struct{}, 1)
	conn := &websocketNotifyWriteConn{Conn: front, writes: blockedWrite}
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 101, Header: http.Header{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}}, Body: back, Request: r}, nil
	})
	srv := websocketServer("https://upstream.test", &http.Client{Transport: rt})
	defer srv.Close()
	writer := &websocketPipeWriter{ResponseRecorder: httptest.NewRecorder(), conn: conn}
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.Handler().ServeHTTP(writer, websocketRequest(t, "/v1/responses"))
	}()
	if _, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: http.MethodGet}); err != nil {
		t.Fatal(err)
	}
	conn.notify.Store(true)
	providerDone := make(chan struct{})
	go func() {
		defer close(providerDone)
		_, _ = provider.Write(bytes.Repeat([]byte("opaque frame bytes"), 1<<16))
	}()
	select {
	case <-blockedWrite:
	case <-time.After(3 * time.Second):
		t.Fatal("fixture never blocked writing to unread frontend")
	}
	closed := make(chan struct{})
	go func() { _ = srv.Close(); close(closed) }()
	waitWebSocketDone(t, closed)
	waitWebSocketDone(t, done)
	waitWebSocketDone(t, providerDone)
}

type websocketPipeWriter struct {
	*httptest.ResponseRecorder
	conn net.Conn
}

func (w *websocketPipeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.conn, bufio.NewReadWriter(bufio.NewReader(w.conn), bufio.NewWriter(w.conn)), nil
}

type websocketNotifyWriteConn struct {
	net.Conn
	notify atomic.Bool
	writes chan<- struct{}
}

func (c *websocketNotifyWriteConn) Write(b []byte) (int, error) {
	if c.notify.Load() {
		select {
		case c.writes <- struct{}{}:
		default:
		}
	}
	return c.Conn.Write(b)
}

func TestResponsesWebSocketCloseCancelsPendingHandshake(t *testing.T) {
	started, backendDone := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		defer close(backendDone)
		<-r.Context().Done()
	}))
	defer upstream.Close()
	srv := websocketServer(upstream.URL, upstream.Client())
	defer srv.Close()
	frontend := httptest.NewServer(srv.Handler())
	defer frontend.Close()
	clientDone := make(chan struct{})
	req := websocketRequest(t, frontend.URL+"/v1/responses")
	go func() {
		defer close(clientDone)
		resp, err := frontend.Client().Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadGateway {
				t.Errorf("canceled handshake status=%d", resp.StatusCode)
			}
		}
	}()
	waitWebSocketDone(t, started)
	closed := make(chan struct{})
	go func() { _ = srv.Close(); close(closed) }()
	waitWebSocketDone(t, closed)
	waitWebSocketDone(t, backendDone)
	waitWebSocketDone(t, clientDone)
}
