package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers/gemini"
)

func TestBareGeminiPixelRouteAndRecordMode(t *testing.T) {
	t.Setenv("CAVE_PIXEL_MODELS", "gemini-bare")
	path := "/v1beta/models/gemini-bare:generateContent"
	body := geminiPixelBody()

	t.Run("pixel", func(t *testing.T) {
		upstream, got := capturePixelUpstream(t, geminiPixelResponse())
		defer upstream.Close()

		sink := &captureSink{}
		comp := &pixelStoreCompressor{handle: "ccr_pixel_bare_gemini"}
		srv := newPixelTestServer(t, upstream.URL, "pixel", gemini.New(upstream.URL), sink, comp, false)

		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		upstreamBody := string(*got)
		if !bytes.Equal(*got, body) {
			t.Fatalf("Gemini bare pixel should pass through until a Gemini live-zone walker exists:\n got %s\nwant %s", upstreamBody, string(body))
		}
		if got := rec.Header().Get("x-caveman-recovery-handle"); got != "" {
			t.Fatalf("Gemini bare pass-through should not set handle, got %q", got)
		}
		row := sink.last(t)
		if row.Provider != "gemini" || row.Model != "gemini-bare" {
			t.Fatalf("row provider/model = %s/%s, want gemini/gemini-bare", row.Provider, row.Model)
		}
	})

	t.Run("record", func(t *testing.T) {
		upstream, got := capturePixelUpstream(t, geminiPixelResponse())
		defer upstream.Close()

		sink := &captureSink{}
		comp := &pixelStoreCompressor{handle: "ccr_record_bare_gemini"}
		srv := newPixelTestServer(t, upstream.URL, "record", gemini.New(upstream.URL), sink, comp, false)

		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		if !bytes.Equal(*got, body) {
			t.Fatalf("record mode changed bytes:\n got %s\nwant %s", string(*got), string(body))
		}
		if comp.storeCalls != 0 || rec.Header().Get("x-caveman-recovery-handle") != "" {
			t.Fatalf("record mode touched pixel recovery: store=%d handle=%q", comp.storeCalls, rec.Header().Get("x-caveman-recovery-handle"))
		}
		row := sink.last(t)
		if row.RawRequestSHA256 != row.TransformedRequestSHA256 {
			t.Fatalf("record mode row hashes differ: raw=%s transformed=%s", row.RawRequestSHA256, row.TransformedRequestSHA256)
		}
	})
}
