package gateway

import (
	"bytes"
	"io"
	"net/http"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/shared/platform/httpx"
	"github.com/JuliusBrussee/caveman/shared/platform/redact"
)

// A refresh grant is small; WIF assertions are limited to 16 KiB by the SDK.
const oauthTokenMaxBytes = 64 << 10

func (s *Server) matchOAuthTokenAdapter(r *http.Request) providers.Adapter {
	if r == nil || r.URL == nil || r.URL.RawPath != "" {
		return nil
	}
	for _, adapter := range s.adapters {
		if router, ok := adapter.(providers.OAuthTokenRouter); ok && router.MatchOAuthTokenRoute(r.Method, r.URL.Path) {
			return adapter
		}
	}
	return nil
}

// oauthTokenPassthrough relays credential grants after local authentication.
// The request and response carry live tokens, so they bypass inference
// inspection, transformations, payload capture, and spend accounting entirely.
func (s *Server) oauthTokenPassthrough(w http.ResponseWriter, r *http.Request, adapter providers.Adapter, requestID string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, oauthTokenMaxBytes+1))
	_ = r.Body.Close()
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "cave_request_read_failed", "Request body could not be read completely.")
		return
	}
	if len(body) > oauthTokenMaxBytes {
		httpx.Error(w, r, http.StatusRequestEntityTooLarge, "cave_request_too_large", "Request body exceeds the proxy limit.")
		return
	}
	upstreamURL, err := adapter.ResolveUpstreamURL(r.Context(), r, providers.RouteContext{})
	if err != nil {
		httpx.Error(w, r, http.StatusBadGateway, "cave_upstream_unavailable", "Upstream route could not be resolved.")
		return
	}
	headers, err := adapter.SanitizeAndMapHeaders(providers.WithRequestPayloadHash(r.Context(), body), r, providers.Credential{}, upstreamURL)
	if err != nil {
		providerHeaderError(w, r, err)
		return
	}
	// The grant authenticates itself. Do not resolve or attach an operator key.
	s.inflight.Add(1)
	defer s.inflight.Add(-1)
	resp, err := s.doUpstream(r.Context(), func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL.String(), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header = headers.Clone()
		return req, nil
	})
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("provider token exchange failed", "error", redact.Error(err), "request_id", requestID)
		}
		httpx.Error(w, r, http.StatusBadGateway, "cave_upstream_unavailable", "Upstream provider unavailable.")
		return
	}
	defer resp.Body.Close()
	copySafeResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		if s.logger != nil {
			s.logger.Warn("provider token response interrupted", "error", redact.Error(err), "request_id", requestID)
		}
		panic(http.ErrAbortHandler)
	}
}
