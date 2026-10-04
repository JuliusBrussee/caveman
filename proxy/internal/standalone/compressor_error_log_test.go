package standalone

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/ccr"
)

// elidableSegment is long enough that the engine elides it (which RequiresCCR),
// so compressing it reaches engine.store.Put — the write that fails on a
// corrupted or closed recovery store.
func elidableSegment() []byte {
	var rows []string
	for i := 0; i < 60; i++ {
		rows = append(rows, fmt.Sprintf(`{"id":%d,"name":"user%d","city":"city%d","email":"u%d@example.com","active":true,"score":%d}`, i, i, i, i, i*7))
	}
	return []byte(`{"rows":[` + strings.Join(rows, ",") + `]}`)
}

// A broken CCR store made compression silently stop: the engine fails closed and
// returns its error, engineCompressor dropped it, and the gateway read the
// resulting (segment, 0, 0) as "nothing to compress". proxy.log stayed empty and
// `caveman status` reported no off-states, so #1149 went unnoticed for weeks.
// The pass-through itself is correct — never claim a delta without a durable
// handle — so what is asserted here is that the failure is no longer silent.
func TestEngineCompressor_LogsDroppedEngineError(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatalf("open ccr: %v", err)
	}
	segment := elidableSegment()

	var logs bytes.Buffer
	comp := NewEngineCompressorWithLogger(store, slog.New(slog.NewTextHandler(&logs, nil)))

	// Healthy store first: a working compression must stay quiet.
	if out, before, after := comp.CompressSegment(segment); after >= before || bytes.Equal(out, segment) {
		t.Fatalf("precondition: expected a real compression, got before=%d after=%d", before, after)
	}
	if logs.Len() != 0 {
		t.Fatalf("a successful compression logged a warning:\n%s", logs.String())
	}

	// Now break the store exactly as a corrupted ccr.db breaks it: Put fails.
	store.Close()

	out, before, after := comp.CompressSegment(segment)
	if !bytes.Equal(out, segment) || before != 0 || after != 0 {
		t.Fatalf("expected byte-safe pass-through, got before=%d after=%d", before, after)
	}
	got := logs.String()
	if !strings.Contains(got, "recovery store is closed") {
		t.Errorf("the engine's error never reached the log; got:\n%s", got)
	}
	if !strings.Contains(got, "level=WARN") {
		t.Errorf("expected a WARN; got:\n%s", got)
	}
}

// The typed and query-aware siblings run the same engine path and dropped the
// same error. A guard on CompressSegment alone would leave both silent.
func TestEngineCompressor_LogsDroppedEngineErrorOnSiblings(t *testing.T) {
	segment := elidableSegment()
	for _, tc := range []struct {
		name string
		call func(c *engineCompressor) ([]byte, int, int)
	}{
		{"CompressSegmentType", func(c *engineCompressor) ([]byte, int, int) {
			return c.CompressSegmentType(segment, "json")
		}},
		{"CompressSegmentQuery", func(c *engineCompressor) ([]byte, int, int) {
			return c.CompressSegmentQuery(segment, "city")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := ccr.OpenMemory()
			if err != nil {
				t.Fatalf("open ccr: %v", err)
			}
			store.Close()
			var logs bytes.Buffer
			comp := NewEngineCompressorWithLogger(store, slog.New(slog.NewTextHandler(&logs, nil))).(*engineCompressor)
			if out, before, after := tc.call(comp); !bytes.Equal(out, segment) || before != 0 || after != 0 {
				t.Fatalf("expected byte-safe pass-through, got before=%d after=%d", before, after)
			}
			if !strings.Contains(logs.String(), "recovery store is closed") {
				t.Errorf("%s dropped the engine error; log was:\n%s", tc.name, logs.String())
			}
		})
	}
}

// A broken store fails on every block of every request. Without a throttle the
// warning would flood proxy.log and bury whatever else is in it.
func TestEngineCompressor_ThrottlesRepeatedWarnings(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatalf("open ccr: %v", err)
	}
	store.Close()
	var logs bytes.Buffer
	comp := NewEngineCompressorWithLogger(store, slog.New(slog.NewTextHandler(&logs, nil)))
	for i := 0; i < 50; i++ {
		comp.CompressSegment(elidableSegment())
	}
	if n := strings.Count(logs.String(), "level=WARN"); n != 1 {
		t.Errorf("expected exactly 1 throttled warning across 50 failures, got %d:\n%s", n, logs.String())
	}
}

// A nil logger is the shipped shape for NewEngineCompressor and for the
// estimate-only compressor. It must stay a no-op, never a nil dereference.
func TestEngineCompressor_NilLoggerDoesNotPanic(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatalf("open ccr: %v", err)
	}
	store.Close()
	comp := NewEngineCompressor(store)
	if out, _, _ := comp.CompressSegment(elidableSegment()); !bytes.Equal(out, elidableSegment()) {
		t.Fatal("expected pass-through")
	}
}
