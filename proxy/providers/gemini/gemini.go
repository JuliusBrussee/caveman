package gemini

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/url"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
	"github.com/JuliusBrussee/caveman/shared/platform/env"
	"github.com/JuliusBrussee/caveman/shared/platform/ssrf"
)

// ErrRequestCredentials contains no caller values and is safe for an HTTP error.
var ErrRequestCredentials = providers.ErrGoogleRequestCredentials

// RequestAPIKey resolves Google's equivalent API-key system parameters. The
// legacy x-api-key alias remains lower priority than the native header, but a
// conflicting URL credential is never silently assigned to either account.
func RequestAPIKey(req *http.Request) (string, error) {
	return providers.GoogleRequestAPIKey(req)
}

func (a Adapter) ResolveUpstreamURL(ctx context.Context, req *http.Request, route providers.RouteContext) (*url.URL, error) {
	if _, err := RequestAPIKey(req); err != nil {
		return nil, err
	}
	u, err := a.Base.ResolveUpstreamURL(ctx, req, route)
	if err != nil {
		return nil, err
	}
	u.RawQuery = providers.WithoutGoogleAPIKeyQuery(u.RawQuery)
	if env.IsProduction() {
		if err := providers.ValidateUpstreamEndpoint(ctx, u, ssrf.ManagedConfig()); err != nil {
			return nil, err
		}
	}
	return u, nil
}

func (a Adapter) SanitizeAndMapHeaders(ctx context.Context, req *http.Request, credential providers.Credential, upstream *url.URL) (http.Header, error) {
	key, err := RequestAPIKey(req)
	if err != nil {
		return nil, err
	}
	out, err := a.Base.SanitizeAndMapHeaders(ctx, req, credential, upstream)
	authScheme, authKey, _ := strings.Cut(strings.TrimSpace(req.Header.Get("Authorization")), " ")
	if err == nil && key != "" && credential.Mode == "ephemeral_header" && credential.Scheme == "bearer" &&
		credential.Key != "no-key-required" && strings.EqualFold(authScheme, "Bearer") && strings.TrimSpace(authKey) == credential.Key {
		// Preserve both inputs of an explicitly selected caller OAuth request.
		// A separately resolved managed credential remains authoritative: inbound
		// query keys must never replace or augment another selected principal.
		out.Set("x-goog-api-key", key)
	}
	return out, err
}

const (
	geminiPrefixedRoutePrefix = "/gemini/v1beta/models/"
	geminiBareRoutePrefix     = "/v1beta/models/"
	geminiStableRoutePrefix   = "/gemini/v1/models/"
	geminiStableBarePrefix    = "/v1/models/"
)

var geminiRoutePrefixes = []string{geminiPrefixedRoutePrefix, geminiBareRoutePrefix, geminiStableRoutePrefix, geminiStableBarePrefix}
var geminiRouteMethods = []string{"generateContent", "streamGenerateContent", "countTokens"}

// CompressionRoutePatterns exposes the route templates derived from the same
// prefix/method registries MatchRoute uses. Coverage tests compare this set to
// the checked-in compression matrix, so adding either a prefix or method cannot
// silently bypass a supported/unsupported decision.
func CompressionRoutePatterns() []string {
	routes := make([]string, 0, len(geminiRoutePrefixes)*len(geminiRouteMethods))
	for _, prefix := range geminiRoutePrefixes {
		for _, method := range geminiRouteMethods {
			routes = append(routes, prefix+"{model}:"+method)
		}
	}
	return routes
}

func New(baseURL string) providers.Adapter {
	return Adapter{Base: providers.Base{Provider: "gemini", BaseURL: baseURL, Routes: []string{geminiPrefixedRoutePrefix, geminiStableRoutePrefix}}}
}

func (a Adapter) MatchRoute(method string, path string) bool {
	if method != http.MethodPost {
		return false
	}
	// Prefixed and bare routes share one exact allowlist. Base.MatchRoute treats
	// trailing-slash routes as arbitrary subtrees, which would forward unknown,
	// unpriced Gemini operations through the managed and standalone gateways.
	_, _, ok := parseGeminiRoute(path)
	return ok
}

func (a Adapter) InspectRequest(ctx context.Context, body providers.BodyReader, headers http.Header) (providers.RequestMetadata, error) {
	meta, err := a.Base.InspectRequest(ctx, body, headers)
	if err != nil {
		return meta, err
	}
	if model, routeMethod, ok := parseGeminiRoute(headers.Get("x-cave-route-path")); ok {
		meta.Model = model
		meta.Endpoint = routeMethod
		meta.Stream = routeMethod == "streamGenerateContent"
	}
	return meta, nil
}

// CachedPrefixComponents splits a direct Gemini generateContent request into
// `[systemInstruction, tools, content_0, ...]`, aligning with Vertex Gemini and
// the gateway's `sharedComponents(adapter) == 2` invariant so generationConfig
// changes never trigger false busts and prefix_monitor headDrift inspects tools.
func (a Adapter) CachedPrefixComponents(body []byte, meta providers.RequestMetadata) ([][]byte, int, bool) {
	if strings.Contains(strings.ToLower(meta.Endpoint), "counttokens") {
		return nil, 0, false
	}
	root, ok := jsonsplice.Root(body)
	if !ok {
		return nil, 0, false
	}
	contentsSpan, exists := jsonsplice.Field(body, root, "contents")
	if !exists || contentsSpan.Start >= contentsSpan.End || body[contentsSpan.Start] != '[' {
		return nil, 0, false
	}
	items, valid := jsonsplice.Elements(body, contentsSpan)
	if !valid {
		return nil, 0, false
	}
	out := make([][]byte, 0, 2+len(items))
	for _, name := range []string{"systemInstruction", "tools"} {
		var value []byte
		if span, found := jsonsplice.Field(body, root, name); found {
			value = body[span.Start:span.End]
		}
		out = append(out, geminiFrameComponent(name, value))
	}
	for _, item := range items {
		out = append(out, geminiFrameComponent("content", body[item.Start:item.End]))
	}
	return out, len(out), true
}

func geminiFrameComponent(name string, value []byte) []byte {
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(len(name)))
	binary.BigEndian.PutUint32(hdr[4:], uint32(len(value)))
	out := make([]byte, 0, 8+len(name)+len(value))
	out = append(out, hdr[:]...)
	out = append(out, name...)
	out = append(out, value...)
	return out
}

func parseGeminiRoute(path string) (string, string, bool) {
	for _, prefix := range geminiRoutePrefixes {
		rest, ok := strings.CutPrefix(path, prefix)
		if !ok {
			continue
		}
		for _, routeMethod := range geminiRouteMethods {
			suffix := ":" + routeMethod
			if !strings.HasSuffix(rest, suffix) {
				continue
			}
			model := strings.TrimSuffix(rest, suffix)
			if model == "" || strings.Contains(model, "/") {
				return "", "", false
			}
			return model, routeMethod, true
		}
	}
	return "", "", false
}
