package openaicompat

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func New(baseURL string) providers.Adapter {
	return Adapter{Base: providers.Base{Provider: "openai_compatible", BaseURL: baseURL, Routes: []string{"/compat/"}}}
}

type Adapter struct{ providers.Base }

// MatchRoute rejects path spellings whose decoded form could claim a different
// compatibility mount than the caller sent. The gateway performs the same
// check against URL.RawPath before it selects an adapter; this decoded check
// keeps direct adapter users fail-closed as well.
func (a Adapter) MatchRoute(method, path string) bool {
	if err := validateCompatPath(path, ""); err != nil {
		return false
	}
	return a.Base.MatchRoute(method, path)
}

// ResolveUpstreamURL removes only the default public /compat mount before
// resolving the provider path. Named adapters and the reserved legacy routes
// keep their dedicated resolver/compatibility behavior; this method exists for
// the bare providers.openai_compatible route only.
func (a Adapter) ResolveUpstreamURL(_ context.Context, req *http.Request, route providers.RouteContext) (*url.URL, error) {
	if req == nil || req.URL == nil || !strings.HasPrefix(req.URL.Path, "/compat/") {
		path := ""
		if req != nil && req.URL != nil {
			path = req.URL.Path
		}
		return nil, fmt.Errorf("compat route %q does not match default /compat/ mount", path)
	}
	if err := validateCompatPath(req.URL.Path, req.URL.RawPath); err != nil {
		return nil, err
	}

	// /compat/stub and /compat/openai-compatible were the original default
	// routes. They share the same strict path and query handling as the bare
	// mount, but retain their historical prefix stripping.
	requestPath := strings.TrimPrefix(req.URL.Path, "/compat")
	for _, legacy := range []string{"/stub", "/openai-compatible"} {
		if hasCompatPrefix(req.URL.Path, "/compat"+legacy) {
			requestPath = strings.TrimPrefix(req.URL.Path, "/compat"+legacy)
			break
		}
	}

	baseURL := a.Base.BaseURL
	if route.BaseURL != "" {
		baseURL = route.BaseURL
	}
	base, err := parseBaseURL(baseURL, a.Base.Provider)
	if err != nil {
		return nil, err
	}

	base.Path = joinCompatPath(base.Path, requestPath)
	// The parsed base URL may carry an encoded path hint that no longer matches
	// the joined decoded path. Clearing it lets net/url encode the final path
	// from Path without retaining stale escaping.
	base.RawPath = ""
	base.RawQuery = joinCompatQuery(base.RawQuery, req.URL.RawQuery)
	return base, nil
}

// joinCompatPath appends the public route's provider path to the configured
// base path, collapsing an overlapping segment. Ollama's documented base URL
// is /v1 while the compatibility request is /compat/v1/..., so the overlap must
// not become /v1/v1/.... Other configured prefixes (for example /api) remain
// intact.
func joinCompatPath(basePath, requestPath string) string {
	baseParts := splitCompatPath(basePath)
	requestParts := splitCompatPath(requestPath)
	if len(baseParts) == 0 {
		return "/" + strings.Join(requestParts, "/")
	}
	if len(requestParts) == 0 {
		return "/" + strings.Join(baseParts, "/")
	}
	maxOverlap := len(baseParts)
	if len(requestParts) < maxOverlap {
		maxOverlap = len(requestParts)
	}
	for overlap := maxOverlap; overlap > 0; overlap-- {
		match := true
		for i := 0; i < overlap; i++ {
			if baseParts[len(baseParts)-overlap+i] != requestParts[i] {
				match = false
				break
			}
		}
		if match {
			joined := append(append([]string{}, baseParts...), requestParts[overlap:]...)
			return "/" + strings.Join(joined, "/")
		}
	}
	return "/" + strings.Join(append(baseParts, requestParts...), "/")
}

func splitCompatPath(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func hasCompatPrefix(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func joinCompatQuery(baseQuery, requestQuery string) string {
	switch {
	case baseQuery == "":
		return requestQuery
	case requestQuery == "":
		return baseQuery
	default:
		return baseQuery + "&" + requestQuery
	}
}

func (a Adapter) InspectRequest(ctx context.Context, body providers.BodyReader, headers http.Header) (providers.RequestMetadata, error) {
	return inspectOpenAICompatible(ctx, a.Base, body, headers)
}

func (a Adapter) ExtractCompressible(body []byte, meta providers.RequestMetadata) ([][]byte, func([][]byte) ([]byte, error), bool) {
	return openai.ExtractCompressible(body, meta)
}

func (a Adapter) ExtractStabilizable(body []byte, meta providers.RequestMetadata) ([]providers.RewritableBlock, func([][]byte) ([]byte, error), bool) {
	return openai.ExtractStabilizable(body, meta)
}

func (a namedAdapter) ExtractCompressible(body []byte, meta providers.RequestMetadata) ([][]byte, func([][]byte) ([]byte, error), bool) {
	return openai.ExtractCompressible(body, meta)
}

func (a namedAdapter) ExtractStabilizable(body []byte, meta providers.RequestMetadata) ([]providers.RewritableBlock, func([][]byte) ([]byte, error), bool) {
	return openai.ExtractStabilizable(body, meta)
}

type namedAdapter struct {
	providers.Base
	prefix string
}

func (a namedAdapter) MatchRoute(method, path string) bool {
	if err := validateCompatPath(path, ""); err != nil {
		return false
	}
	return a.Base.MatchRoute(method, path)
}

func (a namedAdapter) InspectRequest(ctx context.Context, body providers.BodyReader, headers http.Header) (providers.RequestMetadata, error) {
	return inspectOpenAICompatible(ctx, a.Base, body, headers)
}

func inspectOpenAICompatible(ctx context.Context, base providers.Base, body providers.BodyReader, headers http.Header) (providers.RequestMetadata, error) {
	meta, err := base.InspectRequest(ctx, body, headers)
	if path := headers.Get("x-cave-route-path"); path != "" {
		meta.Endpoint = path
	}
	return meta, err
}

// NewNamed builds a dedicated OpenAI-compatible upstream mounted at
// /compat/<name>/. The provider enum intentionally stays openai_compatible so
// usage/cost telemetry keeps using the shared OpenAI-shape parser.
func NewNamed(name, baseURL string) (providers.Adapter, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	baseURL = strings.TrimSpace(baseURL)
	if err := ValidateBaseURL(baseURL); err != nil {
		return nil, fmt.Errorf("compat upstream %q base_url: %w", name, err)
	}
	prefix := "/compat/" + name
	return namedAdapter{
		Base: providers.Base{
			Provider: "openai_compatible",
			BaseURL:  baseURL,
			Routes:   []string{prefix + "/"},
		},
		prefix: prefix,
	}, nil
}

func ValidateName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("compat upstream name %q must match [a-z0-9][a-z0-9._-]{0,63}", name)
	}
	switch name {
	case "stub", "openai-compatible":
		return fmt.Errorf("compat upstream name %q is reserved", name)
	default:
		return nil
	}
}

func ValidateBaseURL(raw string) error {
	_, err := parseBaseURL(raw, "compat")
	return err
}

// ResolveUpstreamURL intentionally ignores RouteContext.BaseURL. Named mounts
// are configured as independent static upstreams (`compat:` in standalone or
// CAVE_COMPAT_UPSTREAMS in managed mode); applying the generic project
// openai_compatible override here would collapse every named route onto one
// target. The default /compat/ adapter is the route that honors BaseURL.
func (a namedAdapter) ResolveUpstreamURL(_ context.Context, req *http.Request, _ providers.RouteContext) (*url.URL, error) {
	if req == nil || req.URL == nil {
		return nil, fmt.Errorf("compat request URL is missing")
	}
	if err := validateCompatPath(req.URL.Path, req.URL.RawPath); err != nil {
		return nil, err
	}
	base, err := parseBaseURL(a.BaseURL, a.Provider)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(req.URL.Path, a.prefix+"/") {
		return nil, fmt.Errorf("compat route %q does not match prefix %q", req.URL.Path, a.prefix+"/")
	}
	path := strings.TrimPrefix(req.URL.Path, a.prefix)
	base.Path = joinCompatPath(base.Path, path)
	base.RawPath = ""
	base.RawQuery = joinCompatQuery(base.RawQuery, req.URL.RawQuery)
	return base, nil
}

// ValidateRequestPath rejects ambiguous path encodings before adapter selection.
// Gateways call this with the original URL (including RawPath), while the
// adapter MatchRoute/ResolveUpstreamURL checks provide a second fail-closed
// boundary for direct callers.
func ValidateRequestPath(u *url.URL) error {
	if u == nil {
		return fmt.Errorf("request URL is missing")
	}
	if !strings.HasPrefix(u.Path, "/compat/") {
		return nil
	}
	return validateCompatPath(u.Path, u.RawPath)
}

func validateCompatPath(path, rawPath string) error {
	if !strings.HasPrefix(path, "/compat/") {
		return nil
	}
	if err := validatePathComponents(path, rawPath); err != nil {
		return fmt.Errorf("compat route path rejected: %w", err)
	}
	return nil
}

func validatePathComponents(path, rawPath string) error {
	if strings.Contains(path, `\`) {
		return fmt.Errorf("backslash is not allowed in path")
	}
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if segment == "" && i > 0 && i < len(segments)-1 {
			return fmt.Errorf("repeated path separators are not allowed")
		}
		if segment == "." || segment == ".." {
			return fmt.Errorf("dot segments are not allowed in path")
		}
	}
	// URL.Path is decoded by net/url while RawPath retains a valid escaped
	// spelling. Reject separators, backslashes, and dot bytes in either form so
	// a path cannot change route identity after another decoder or proxy hop.
	for _, escape := range []string{"%2f", "%5c", "%2e"} {
		if strings.Contains(strings.ToLower(path), escape) || strings.Contains(strings.ToLower(rawPath), escape) {
			return fmt.Errorf("ambiguous escaped path sequence %s", escape)
		}
	}
	return nil
}

func parseBaseURL(raw, provider string) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("provider %q has no configured upstream URL", provider)
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("provider %q upstream URL scheme %q is not allowed", provider, u.Scheme)
	}
	if !u.IsAbs() || u.Host == "" || u.Hostname() == "" {
		return nil, fmt.Errorf("provider %q upstream URL must be an absolute URL with a host", provider)
	}
	if u.User != nil {
		return nil, fmt.Errorf("provider %q upstream URL must not include userinfo", provider)
	}
	if u.Fragment != "" {
		return nil, fmt.Errorf("provider %q upstream URL must not include a fragment", provider)
	}
	if err := validatePathComponents(u.Path, u.RawPath); err != nil {
		return nil, fmt.Errorf("provider %q upstream URL path rejected: %w", provider, err)
	}
	return u, nil
}
