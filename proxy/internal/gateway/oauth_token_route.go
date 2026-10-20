package gateway

import (
	"bytes"
	"io"
	"net/http"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/shared/platform/httpx"
	"github.com/JuliusBrussee/caveman/shared/platform/redact"
)

// oauthTokenMaxBytes bounds a token request body. A refresh grant is a few
// hundred bytes and a federation assertion is capped at 16 KiB by the SDK.
const oauthTokenMaxBytes = 64 << 10

// matchOAuthTokenAdapter selects the adapter whose OAuth token endpoint this
// request targets (see providers.Base.OAuthTokenRoutes). Nil means 404, like
// matchAdapter. An encoded path never matches: the routes are exact.
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

// oauthTokenPassthrough relays a token grant to the provider and its answer
// back, both byte-for-byte.
//
// Like metadataPassthrough it stays off the inference pipeline: there is no
// prompt to compress and no usage to price, so it writes no spend row. It also
// resolves no credential. The grant authenticates itself (refresh token or
// identity assertion in the body), and attaching the operator's provider key to
// someone else's token exchange would hand that key to a request that never
// needed it. The request and response bodies carry live tokens, so neither is
// logged. The caller authenticates first (see Server.proxy).
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
	upstreamHeaders, err := adapter.SanitizeAndMapHeaders(providers.WithRequestPayloadHash(r.Context(), body), r, providers.Credential{}, upstreamURL)
	if err != nil {
		providerHeaderError(w, r, err)
		return
	}

	s.inflight.Add(1)
	resp, err := s.doUpstream(r.Context(), func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL.String(), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header = upstreamHeaders.Clone()
		return req, nil
	})
	s.inflight.Add(-1)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("provider oauth token request failed", "error", redact.Error(err), "request_id", requestID)
		}
		httpx.Error(w, r, http.StatusBadGateway, "cave_upstream_unavailable", "Upstream provider unavailable.")
		return
	}
	defer func() { _ = resp.Body.Close() }()

	copySafeResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil && s.logger != nil {
		s.logger.Warn("provider oauth token relay failed", "error", redact.Error(err), "request_id", requestID)
	}
}
