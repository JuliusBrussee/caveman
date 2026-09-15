package standalone

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/internal/config"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

func TestStandaloneGeminiQueryCredentialsEndToEnd(t *testing.T) {
	const key = "caller-query-key"
	const path = "/gemini/v1beta/models/gemini-2.5-pro:generateContent"
	const body = `{"contents":[{"parts":[{"text":"hello"}]}]}`
	for _, tc := range []struct {
		name, query, header, authorization, wantQuery, wantKey, wantAuth string
		reject                                                           bool
	}{
		{name: "key query overrides env", query: "key=" + key, wantKey: key},
		{name: "dollar key overrides env", query: "$key=" + key, wantKey: key},
		{name: "encoded key and name", query: "%24key=caller%2Dquery%2Dkey", wantKey: key},
		{name: "other query bytes preserved", query: "alt=sse&key=" + key + "&trace=a%2fb&trace=b+c&fields=usage%2Ctext", wantKey: key, wantQuery: "alt=sse&trace=a%2fb&trace=b+c&fields=usage%2Ctext"},
		{name: "same key repeated", query: "key=" + key + "&$key=" + key + "&key=" + key, wantKey: key},
		{name: "native header agrees", query: "key=" + key, header: key, wantKey: key},
		{name: "query displaces synthetic bearer", query: "key=" + key, authorization: "Bearer no-key-required", wantKey: key},
		{name: "empty key retains fallback", query: "key=&$key=&alt=sse", wantKey: "different-env-account", wantQuery: "alt=sse"},
		{name: "OAuth and query retain both caller inputs", query: "key=" + key, authorization: "Bearer caller-oauth", wantKey: key, wantAuth: "Bearer caller-oauth"},
		{name: "conflicting header", query: "key=" + key, header: "different-header-account", reject: true},
		{name: "conflicting system parameters", query: "key=" + key + "&$key=different-query-account", reject: true},
		{name: "conflicting duplicates", query: "key=" + key + "&key=different-query-account", reject: true},
		{name: "malformed escaped key", query: "key=caller%ZZ", reject: true},
		{name: "header injection in key", query: "key=caller%0D%0AInjected%3Avalue", reject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GEMINI_API_KEY", "different-env-account")
			t.Setenv("GOOGLE_API_KEY", "another-env-account")
			upstream := &captureUpstreamTransport{response: `{}`}
			spend, err := store.Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer spend.Close()
			srv := New(config.Config{Mode: "record", Providers: map[string]config.ProviderConfig{
				"gemini": {BaseURL: "https://upstream.test"},
			}}, spend, Options{HTTPClient: &http.Client{Transport: upstream}})
			req := httptest.NewRequest(http.MethodPost, path+"?"+tc.query, strings.NewReader(body))
			if tc.header != "" {
				req.Header.Set("x-goog-api-key", tc.header)
			}
			if tc.authorization != "" {
				req.Header.Set("Authorization", tc.authorization)
				req.Header.Set("x-goog-user-project", "caller-project")
			}
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if tc.reject {
				if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "cave_provider_credentials_conflict") {
					t.Fatalf("want credential conflict, got status=%d body=%s", rec.Code, rec.Body.String())
				}
				if upstream.headers != nil {
					t.Fatal("conflicting or invalid credential reached upstream")
				}
				for _, secret := range []string{key, "different-header-account", "different-query-account", "different-env-account"} {
					if strings.Contains(rec.Body.String(), secret) {
						t.Fatal("credential value appeared in error response")
					}
				}
				return
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if upstream.headers.Get("x-goog-api-key") != tc.wantKey || upstream.headers.Get("Authorization") != tc.wantAuth {
				t.Fatalf("upstream credential changed: headers=%v", upstream.headers)
			}
			wantURL := "https://upstream.test/v1beta/models/gemini-2.5-pro:generateContent"
			if tc.wantQuery != "" {
				wantURL += "?" + tc.wantQuery
			}
			if upstream.url != wantURL {
				t.Fatalf("upstream URL = %q, want query credentials removed and other bytes preserved: %q", upstream.url, wantURL)
			}
			if string(upstream.body) != body {
				t.Fatal("record request body changed")
			}
			if tc.wantAuth != "" && upstream.headers.Get("x-goog-user-project") != "caller-project" {
				t.Fatal("OAuth quota project changed")
			}
		})
	}
}

// x-api-key is not one of Google's credential spellings (only x-goog-api-key
// and the key/$key system parameters are). Treating it as one made a client
// that stamps an unrelated x-api-key conflict with its own ?key=, and let that
// foreign key displace the Google principal the caller named in Authorization.
func TestGeminiForeignAPIKeyHeaderNeitherConflictsNorDisplaces(t *testing.T) {
	const path = "/gemini/v1beta/models/gemini-2.5-pro:generateContent"
	for _, tc := range []struct {
		name, query, authorization, wantKey, wantAuth string
	}{
		{name: "query key wins over foreign header", query: "?key=caller-query-key", wantKey: "caller-query-key"},
		{name: "caller OAuth wins over foreign header", authorization: "Bearer caller-oauth", wantAuth: "Bearer caller-oauth"},
		{name: "foreign header still authenticates alone", wantKey: "sk-ant-foreign"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GEMINI_API_KEY", "operator-env-account")
			upstream := &captureUpstreamTransport{response: `{}`}
			spend, err := store.Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer spend.Close()
			srv := New(config.Config{Mode: "record", Providers: map[string]config.ProviderConfig{
				"gemini": {BaseURL: "https://upstream.test"},
			}}, spend, Options{HTTPClient: &http.Client{Transport: upstream}})
			req := httptest.NewRequest(http.MethodPost, path+tc.query, strings.NewReader(`{"contents":[]}`))
			req.Header.Set("x-api-key", "sk-ant-foreign")
			if tc.authorization != "" {
				req.Header.Set("Authorization", tc.authorization)
				// Google requires the caller's quota project for user OAuth.
				req.Header.Set("x-goog-user-project", "caller-project")
			}
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if upstream.headers.Get("x-goog-api-key") != tc.wantKey || upstream.headers.Get("Authorization") != tc.wantAuth {
				t.Fatalf("upstream credential = %v", upstream.headers)
			}
		})
	}
}

// A caller that authenticates with an OAuth token in the URL has already named
// a principal. Adding the operator's environment key beside it would bill that
// caller's request to an account it never chose.
func TestGeminiQueryOAuthTokenSuppressesEnvironmentFallback(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "operator-env-account")
	upstream := &captureUpstreamTransport{response: `{}`}
	spend, err := store.Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer spend.Close()
	srv := New(config.Config{Mode: "record", Providers: map[string]config.ProviderConfig{
		"gemini": {BaseURL: "https://upstream.test"},
	}}, spend, Options{HTTPClient: &http.Client{Transport: upstream}})
	req := httptest.NewRequest(http.MethodPost,
		"/gemini/v1beta/models/gemini-2.5-pro:generateContent?access_token=ya29.caller-token", strings.NewReader(`{"contents":[]}`))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := upstream.headers.Get("x-goog-api-key"); got != "" {
		t.Fatalf("operator key added beside a caller URL credential: %q", got)
	}
	if !strings.Contains(upstream.url, "access_token=ya29.caller-token") {
		t.Fatalf("caller URL credential not forwarded: %s", upstream.url)
	}
}
