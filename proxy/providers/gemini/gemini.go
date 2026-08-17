package gemini

import (
	"context"
	"net/http"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

const (
	geminiPrefixedRoutePrefix = "/gemini/v1beta/models/"
	geminiBareRoutePrefix     = "/v1beta/models/"
)

var geminiRoutePrefixes = []string{geminiPrefixedRoutePrefix, geminiBareRoutePrefix}
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
	return Adapter{Base: providers.Base{Provider: "gemini", BaseURL: baseURL, Routes: []string{geminiPrefixedRoutePrefix}}}
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
