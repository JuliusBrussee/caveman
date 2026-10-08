package gateway

import (
	"errors"
	"io"
	"net/http"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/openaicompat"
	"github.com/JuliusBrussee/caveman/shared/platform/httpx"
	"github.com/JuliusBrussee/caveman/shared/platform/redact"
)

// matchMetadataAdapter selects the adapter serving a read-only provider
// metadata request (model discovery). It mirrors matchAdapter — same compat
// path validation, same fail-closed "nil means 404" contract — but consults the
// GET-only MetadataRoutes allowlist. An adapter that does not implement
// providers.MetadataRouter, or that declares no metadata routes, never matches.
func (s *Server) matchMetadataAdapter(r *http.Request) providers.Adapter {
	if r == nil || r.URL == nil || openaicompat.ValidateRequestPath(r.URL) != nil {
		return nil
	}
	for _, adapter := range s.adapters {
		// Shared spellings (bare /v1/models) are decided by the request's wire
		// protocol, never by registration order (issue #1187).
		if matcher, ok := adapter.(providers.MetadataRequestMatcher); ok {
			if matcher.MatchMetadataRequest(r) {
				return adapter
			}
			continue
		}
		router, ok := adapter.(providers.MetadataRouter)
		if !ok {
			continue
		}
		if router.MatchMetadataRoute(r.Method, r.URL.Path) {
			return adapter
		}
	}
	return nil
}

// metadataPassthrough forwards a read-only provider metadata request upstream
// unchanged and relays the answer byte-for-byte.
//
// It is deliberately NOT the main proxy path. Everything that path does —
// buffer and inspect a body, plan cache breakpoints, compress, parse usage,
// price the call and write a spend row — presupposes an inference request. A
// catalog read has no body, no tokens and no cost, so running it through that
// pipeline would either crash on the missing body or book a zero-token row that
// makes the ledger less truthful, not more. The caller still authenticates
// first, so this mount cannot be used as a route oracle (see Server.proxy).
// requestID is the id Server.proxy already minted and echoed to the client, so
// a reported x-cave-request-id matches what any log line here records.
func (s *Server) metadataPassthrough(w http.ResponseWriter, r *http.Request, adapter providers.Adapter, credential providers.Credential, requestID string) {
	upstreamURL, err := adapter.ResolveUpstreamURL(r.Context(), r, providers.RouteContext{})
	if err != nil {
		if errors.Is(err, providers.ErrGoogleRequestCredentials) {
			httpx.Error(w, r, http.StatusBadRequest, "cave_provider_credentials_conflict", providers.ErrGoogleRequestCredentials.Error())
			return
		}
		httpx.Error(w, r, http.StatusBadGateway, "cave_upstream_unavailable", "Upstream route could not be resolved.")
		return
	}
	// Hash the empty payload the way the main path hashes the real one, so a
	// signing adapter (SigV4) signs the bytes that will actually be sent.
	authContext := providers.WithRequestPayloadHash(r.Context(), nil)
	upstreamHeaders, err := adapter.SanitizeAndMapHeaders(authContext, r, credential, upstreamURL)
	if err != nil {
		providerHeaderError(w, r, err)
		return
	}
	s.applyUpstreamAuthFallback(adapter.Name(), credential, upstreamHeaders)
	// A GET carries no payload: send no body and no content-type/length, so a
	// strict upstream cannot reject the catalog read over a phantom entity.
	upstreamHeaders.Del("content-type")
	upstreamHeaders.Del("content-length")
	upstreamHeaders.Del("content-encoding")

	s.inflight.Add(1)
	resp, err := s.doUpstream(r.Context(), func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header = upstreamHeaders.Clone()
		return req, nil
	})
	s.inflight.Add(-1)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("provider metadata read failed", "error", redact.Error(err), "request_id", requestID)
		}
		httpx.Error(w, r, http.StatusBadGateway, "cave_upstream_unavailable", "Upstream provider unavailable.")
		return
	}
	defer func() { _ = resp.Body.Close() }()

	copySafeResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil && s.logger != nil {
		s.logger.Warn("provider metadata relay failed", "error", redact.Error(err), "request_id", requestID)
	}
}
