package translate

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A gated answer always names its type and says nosniff, so no browser
// renders an upstream's bytes as a page; a type a path set is kept.
func TestGateHeadTypesTheAnswer(t *testing.T) {
	for _, tc := range []struct{ set, want string }{{"", "application/json"}, {"text/event-stream", "text/event-stream"}} {
		rec := httptest.NewRecorder()
		g := &gate{w: rec, header: http.Header{}}
		if tc.set != "" {
			g.Header().Set("Content-Type", tc.set)
		}
		_, _ = g.Write([]byte("<html><script>"))
		g.commit()
		_, _ = g.Write([]byte("</script>"))
		res := rec.Result()
		if got := res.Header.Get("Content-Type"); got != tc.want {
			t.Errorf("set %q: Content-Type = %q, want %q", tc.set, got, tc.want)
		}
		if got := res.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("set %q: X-Content-Type-Options = %q, want nosniff", tc.set, got)
		}
		if got := rec.Body.String(); got != "<html><script></script>" {
			t.Errorf("set %q: body = %q", tc.set, got)
		}
	}
}
