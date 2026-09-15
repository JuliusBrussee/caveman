package middleware

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

// Profile the same 100 KiB log body as the local HTTP benchmark. These component
// timings diagnose work; only performance.mjs measures queueing and round trips.
func BenchmarkMiddlewarePreparation(b *testing.B) {
	var source strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&source, "[INFO] row %d repeated diagnostic details with exact detail-%d\n", i, i)
	}
	content := source.String()[:100<<10]
	dir := b.TempDir()
	state, err := store.Open(filepath.Join(dir, "metadata.db"), nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = state.Close() })
	recovery, err := ccr.Open(filepath.Join(dir, "ccr.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = recovery.Close() })
	r, err := New(Config{Store: state, Recovery: recovery, Mode: "compress", Principal: func(*http.Request) (string, error) { return "benchmark", nil }})
	if err != nil {
		b.Fatal(err)
	}
	req := requestFor(r)
	req.Segments[0].Content, req.Segments[0].SHA256 = content, digest([]byte(content))
	b.Run("engine", func(b *testing.B) {
		b.SetBytes(int64(len(content)))
		b.ReportAllocs()
		for b.Loop() {
			if _, err := r.eng.Compress([]byte(content), engine.Options{Mode: engine.ModeCompress, ExternalRecovery: true}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("prepare", func(b *testing.B) {
		b.SetBytes(int64(len(content)))
		b.ReportAllocs()
		for b.Loop() {
			if _, err := r.prepareChoice(context.Background(), "benchmark", req, req.Segments[0]); err != nil {
				b.Fatal(err)
			}
		}
	})
}
