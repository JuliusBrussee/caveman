package translate

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A ping or keepalive goes only between two of the upstream's events, never
// inside one: the Anthropic SDK drops a delta or stop split that way.
// (Found by review: the gate hold tick fired between two lines of an event.)

// slowRecorder is a client reading slowly, so the upstream is ahead of it.
type slowRecorder struct{ *httptest.ResponseRecorder }

func (s slowRecorder) Write(p []byte) (int, error) {
	time.Sleep(200 * time.Microsecond)
	return s.ResponseRecorder.Write(p)
}

// framingBroken: an "event:" line followed by another "event:" before its
// data, or a data line whose type differs from its event name.
func framingBroken(out string) string {
	for _, block := range strings.Split(out, "\n\n") {
		var name, data string
		events := 0
		for _, line := range strings.Split(block, "\n") {
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				name = v
				events++
			}
			if v, ok := strings.CutPrefix(line, "data: "); ok {
				if data != "" {
					return "two data lines: " + block
				}
				data = v
			}
		}
		if events > 1 {
			return "two event lines: " + block
		}
		if data != "" && !strings.Contains(data, `"type":"`+name+`"`) {
			return "name/data mismatch: " + block
		}
		if data != "" && name == "" {
			return "data without event: " + block
		}
	}
	return ""
}

func TestPingNeverSplitsARelayedEvent(t *testing.T) {
	defer func(ping, hold time.Duration) { pingInterval, gateHold = ping, hold }(pingInterval, gateHold)
	pingInterval = time.Hour
	frames := []string{`{"type":"message_start","message":{"id":"m","model":"x","usage":{"input_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`}
	for i := range 300 {
		frames = append(frames, fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"w%d "}}`, i))
	}
	frames = append(frames, `{"type":"content_block_stop","index":0}`, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`, `{"type":"message_stop"}`)
	payload := sse(frames...)
	broken := 0
	for run := range 20 {
		gateHold = time.Duration(5+run) * time.Millisecond
		rec := slowRecorder{httptest.NewRecorder()}
		_, err := Relay(Messages, []byte(`{"stream":true}`), "m").Serve(rec, &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload))})
		if err != nil {
			t.Fatalf("err %v", err)
		}
		if why := framingBroken(rec.Body.String()); why != "" {
			broken++
			if broken == 1 {
				t.Logf("run %d: %s", run, why)
			}
		}
	}
	if broken > 0 {
		t.Errorf("%d/20 relayed streams had a ping spliced into an event", broken)
	}
}

func TestPingNeverSplitsAPacedRelayedEvent(t *testing.T) {
	defer func(ping, hold time.Duration) { pingInterval, gateHold = ping, hold }(pingInterval, gateHold)
	pingInterval = time.Hour
	broken := 0
	runs := 200
	for run := range runs {
		gateHold = time.Duration(3000+run*37) * time.Microsecond
		reader, writer := io.Pipe()
		go func() {
			_, _ = io.WriteString(writer, sse(`{"type":"message_start","message":{"id":"m","model":"x","usage":{"input_tokens":1}}}`, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
			for i := range 60 {
				_, _ = io.WriteString(writer, sse(fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"w%d "}}`, i)))
				time.Sleep(100 * time.Microsecond)
			}
			_, _ = io.WriteString(writer, sse(`{"type":"content_block_stop","index":0}`, `{"type":"message_stop"}`))
			_ = writer.Close()
		}()
		rec := httptest.NewRecorder()
		_, _ = Relay(Messages, []byte(`{"stream":true}`), "m").Serve(rec, &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(reader)})
		if framingBroken(rec.Body.String()) != "" {
			broken++
		}
	}
	if broken > 0 {
		t.Errorf("%d/%d paced streams had a ping spliced into an event", broken, runs)
	}
}

// A relay that returns early (a failure before content) stops its line
// readers and their timers instead of leaving them blocked.
func TestEarlyReturnStopsTheLineReaders(t *testing.T) {
	before := runtime.NumGoroutine()
	for range 50 {
		reader, writer := io.Pipe()
		go func() {
			_, _ = io.WriteString(writer, sse(`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`))
			_, _ = io.WriteString(writer, sse(`{"type":"ping"}`))
			_ = writer.Close()
		}()
		recorder := httptest.NewRecorder()
		_, err := Relay(Messages, []byte(`{"stream":true}`), "m").Serve(recorder, &http.Response{StatusCode: 200, Header: http.Header{}, Body: reader})
		_ = reader.Close() // as the gateway does when Serve returns
		if err != ErrNotServed {
			t.Fatalf("err %v", err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before+5 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before+5 {
		buf := make([]byte, 1<<16)
		t.Fatalf("%d goroutines left behind (%d before)\n%s", n-before, before, buf[:runtime.Stack(buf, true)])
	}
}
