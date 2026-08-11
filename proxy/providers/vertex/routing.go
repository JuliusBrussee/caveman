package vertex

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/shared/platform/env"
	"github.com/JuliusBrussee/caveman/shared/platform/ssrf"
)

// defaultPublishers is the built-in publisher allowlist. Operators override it
// with CAVE_VERTEX_PUBLISHER_ALLOWLIST (comma-separated). Pinning publishers to
// the families this gateway prices/supports keeps a client from invoking an
// unpriced partner model whose cost the catalog does not cover.
var defaultPublishers = []string{"google", "anthropic"}

// defaultModelPrefixes is the built-in model-id allowlist, matched as prefixes
// so versioned ids (gemini-2.5-pro, claude-sonnet-4-5@20250929) are covered.
// Operators override it with CAVE_VERTEX_MODEL_ALLOWLIST.
var defaultModelPrefixes = []string{"gemini-", "claude-"}

// ResolveUpstreamURL validates the Vertex request shape (publisher + model on
// the allowlist), enforces SSRF/host constraints against the aiplatform
// endpoint, and resolves the upstream URL. The /vertex prefix is stripped so the
// upstream path is the native aiplatform path
// (/v1/projects/{p}/locations/{l}/publishers/{pub}/models/{model}:{method}). The
// query string (e.g. ?alt=sse) is preserved unchanged — byte-safe passthrough.
func (a Adapter) ResolveUpstreamURL(ctx context.Context, req *http.Request, route providers.RouteContext) (*url.URL, error) {
	publisher, model, method := parsePredictPath(req.URL.Path)
	if publisher == "" || model == "" || method == "" {
		return nil, fmt.Errorf("vertex request path %q does not name a publisher, model, and method", req.URL.Path)
	}
	if !methodAllowed(publisher, method) {
		return nil, fmt.Errorf("vertex method %q is not allowed for publisher %q", method, publisher)
	}
	if !publisherAllowed(publisher) {
		return nil, fmt.Errorf("vertex publisher %q is not on the allowlist", publisher)
	}
	if !modelAllowed(model) {
		return nil, fmt.Errorf("vertex model %q is not on the allowlist", model)
	}

	baseURL := a.BaseURL
	if route.BaseURL != "" {
		baseURL = route.BaseURL
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("vertex base url invalid: %w", err)
	}
	base.Path = strings.TrimRight(base.Path, "/") + strings.TrimPrefix(req.URL.Path, "/vertex")
	base.RawQuery = req.URL.RawQuery

	// SSRF/host validation on the resolved endpoint. Active in managed (prod)
	// mode; local/self-hosted (stub) endpoints are permitted so the dry-run and
	// examples can target the provider-stub.
	if env.IsProduction() {
		if err := ssrf.ValidateURL(ctx, base.String(), ssrf.ManagedConfig()); err != nil {
			return nil, err
		}
		if host := base.Hostname(); !isVertexHost(host) {
			return nil, fmt.Errorf("vertex endpoint host %q is not an aiplatform host", host)
		}
	}
	return base, nil
}

func methodAllowed(publisher, method string) bool {
	switch publisher {
	case "google":
		return method == "generateContent" || method == "streamGenerateContent"
	case "anthropic":
		return method == "rawPredict" || method == "streamRawPredict"
	default:
		return false
	}
}

// InspectRequest fills in request metadata. Vertex carries the model id in the
// request path (.../models/{model}:{method}) rather than the body, so the model
// is resolved from the route path the proxy records in x-cave-route-path. The
// base inspector handles input bytes and message/tool counts.
func (a Adapter) InspectRequest(ctx context.Context, body providers.BodyReader, headers http.Header) (providers.RequestMetadata, error) {
	meta, err := a.Base.InspectRequest(ctx, body, headers)
	if err != nil {
		return meta, err
	}
	meta.Provider = a.Provider
	routePath := headers.Get("x-cave-route-path")
	if _, model, _ := parsePredictPath(routePath); model != "" {
		meta.Model = model
	}
	meta.Region = locationFromPredictPath(routePath)
	if requestType := strings.ToLower(strings.TrimSpace(headers.Get("x-vertex-ai-llm-request-type"))); requestType != "" {
		switch requestType {
		case "shared":
			meta.ServiceTier = "standard"
		case "dedicated":
			meta.ServiceTier = "provisioned_throughput"
		default:
			meta.PricingUnsupportedReason = "unsupported_traffic_type"
		}
	}
	// Streaming is selected by the method suffix (:streamGenerateContent /
	// :streamRawPredict), not by a body "stream" flag.
	if strings.Contains(routePath, ":streamGenerateContent") || strings.Contains(routePath, ":streamRawPredict") {
		meta.Stream = true
	}
	return meta, nil
}

// parsePredictPath extracts the publisher, model id, and method from a Vertex
// path of the form
// /vertex/v1/projects/{p}/locations/{l}/publishers/{publisher}/models/{model}:{method}.
// The model id may carry an @date version (claude-sonnet-4-5@20250929) but never
// a colon, so the trailing path segment splits on its last colon into
// model:method.
func parsePredictPath(path string) (publisher, model, method string) {
	rest := strings.TrimPrefix(path, "/vertex")
	parts := strings.Split(strings.TrimPrefix(rest, "/"), "/")
	for i, p := range parts {
		switch p {
		case "publishers":
			if i+1 < len(parts) {
				publisher = parts[i+1]
			}
		case "models":
			if i+1 < len(parts) {
				seg := parts[i+1]
				if idx := strings.LastIndex(seg, ":"); idx > 0 && idx < len(seg)-1 {
					model = seg[:idx]
					method = seg[idx+1:]
				}
			}
		}
	}
	return publisher, model, method
}

func locationFromPredictPath(path string) string {
	rest := strings.TrimPrefix(path, "/vertex")
	parts := strings.Split(strings.TrimPrefix(rest, "/"), "/")
	for i, part := range parts {
		if part == "locations" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

func publisherAllowed(publisher string) bool {
	return onAllowlist(publisher, allowlist("CAVE_VERTEX_PUBLISHER_ALLOWLIST", defaultPublishers), false)
}

func modelAllowed(model string) bool {
	return onAllowlist(model, allowlist("CAVE_VERTEX_MODEL_ALLOWLIST", defaultModelPrefixes), true)
}

// onAllowlist reports membership. When prefix is true an entry matches as a
// prefix of value (for versioned model ids); otherwise it is an exact match.
func onAllowlist(value string, list []string, prefix bool) bool {
	for _, entry := range list {
		if value == entry || (prefix && strings.HasPrefix(value, entry)) {
			return true
		}
	}
	return false
}

func allowlist(envKey string, fallback []string) []string {
	if raw := env.String(envKey, ""); raw != "" {
		out := []string{}
		for _, v := range strings.Split(raw, ",") {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return fallback
}

// isVertexHost guards against the endpoint being repointed at a non-Vertex host
// in managed mode (defense-in-depth on top of the SSRF guard). It accepts the
// global host (aiplatform.googleapis.com), regional and global hosts
// ({location}-aiplatform.googleapis.com, incl. global-aiplatform...), and
// multi-region hosts (aiplatform.{us,eu}.rep.googleapis.com).
func isVertexHost(host string) bool {
	if host == "aiplatform.googleapis.com" {
		return true
	}
	if strings.HasSuffix(host, "-aiplatform.googleapis.com") {
		return true
	}
	if strings.HasPrefix(host, "aiplatform.") && strings.HasSuffix(host, ".rep.googleapis.com") {
		return true
	}
	return false
}
