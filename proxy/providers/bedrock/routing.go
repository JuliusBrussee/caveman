package bedrock

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/shared/platform/env"
	"github.com/JuliusBrussee/caveman/shared/platform/ssrf"
)

const (
	runtimeService = "bedrock"
	mantleService  = "bedrock-mantle"

	endpointRuntime = "runtime"
	endpointMantle  = "mantle"
)

// defaultRegions is the built-in region allowlist. Operators override it with
// CAVE_BEDROCK_REGION_ALLOWLIST (comma-separated). Pinning regions prevents a
// client from invoking an unverified region whose pricing/availability the
// catalog does not cover.
var defaultRegions = []string{
	"af-south-1",
	"ap-east-2",
	"ap-northeast-1",
	"ap-northeast-2",
	"ap-northeast-3",
	"ap-south-1",
	"ap-south-2",
	"ap-southeast-1",
	"ap-southeast-2",
	"ap-southeast-3",
	"ap-southeast-4",
	"ap-southeast-5",
	"ap-southeast-6",
	"ap-southeast-7",
	"ca-central-1",
	"ca-west-1",
	"eu-central-1",
	"eu-central-2",
	"eu-north-1",
	"eu-south-1",
	"eu-south-2",
	"eu-west-1",
	"eu-west-2",
	"eu-west-3",
	"il-central-1",
	"me-central-1",
	"me-south-1",
	"sa-east-1",
	"us-east-1",
	"us-east-2",
	"us-gov-east-1",
	"us-gov-west-1",
	"us-west-1",
	"us-west-2",
}

// defaultAPIKeyRegions is narrower than Runtime availability. AWS publishes
// Bedrock API-key support separately; accepting a key in another Runtime region
// would create an onboarding path that can never authenticate.
var defaultAPIKeyRegions = []string{
	"ap-northeast-1",
	"ap-northeast-2",
	"ap-northeast-3",
	"ap-south-1",
	"ap-south-2",
	"ap-southeast-1",
	"ap-southeast-2",
	"ca-central-1",
	"eu-central-1",
	"eu-central-2",
	"eu-north-1",
	"eu-south-1",
	"eu-south-2",
	"eu-west-1",
	"eu-west-2",
	"eu-west-3",
	"sa-east-1",
	"us-east-1",
	"us-gov-east-1",
	"us-gov-west-1",
	"us-west-2",
}

// defaultMantleRegions follows AWS's endpoint-specific availability list.
// Mantle is not present in every region that serves Bedrock Runtime.
var defaultMantleRegions = []string{
	"ap-northeast-1",
	"ap-south-1",
	"ap-southeast-2",
	"ap-southeast-3",
	"eu-central-1",
	"eu-north-1",
	"eu-south-1",
	"eu-west-1",
	"eu-west-2",
	"sa-east-1",
	"us-east-1",
	"us-east-2",
	"us-gov-west-1",
	"us-west-2",
}

// defaultModelPrefixes is the built-in model-id allowlist, matched as prefixes
// so versioned model ids (e.g. anthropic.claude-3-5-sonnet-20241022-v2:0) and
// cross-region inference profiles (e.g. us.anthropic.claude-3-5-sonnet…) are
// covered. Operators override it with CAVE_BEDROCK_MODEL_ALLOWLIST.
var defaultModelPrefixes = []string{
	"anthropic.claude-",
	"amazon.nova-",
	"amazon.titan-",
	"meta.llama2",
	"meta.llama3",
	"meta.llama4",
	"mistral.",
	"cohere.",
	"ai21.",
}

var (
	accountIDPattern          = regexp.MustCompile(`^[0-9]{12}$`)
	foundationModelResource   = regexp.MustCompile(`^foundation-model/[a-z0-9-]{1,63}\.[a-z0-9-]{1,63}([.:]?[a-z0-9-]{1,63})*$`)
	customModelResource       = regexp.MustCompile(`^custom-model/[a-z0-9-]{1,63}\.[a-z0-9-]{1,63}/[a-z0-9]{12}$`)
	accountModelResource      = regexp.MustCompile(`^(imported-model|provisioned-model|custom-model-deployment)/[a-z0-9]{12}$`)
	inferenceProfileResource  = regexp.MustCompile(`^(inference-profile|application-inference-profile)/[a-zA-Z0-9-:.]+$`)
	promptResource            = regexp.MustCompile(`^prompt/[0-9a-zA-Z]{10}(:[0-9]{1,5})?$`)
	promptRouterResource      = regexp.MustCompile(`^(default-)?prompt-router/[a-zA-Z0-9-:.]+$`)
	sageMakerEndpointResource = regexp.MustCompile(`^endpoint/[a-zA-Z0-9-]+$`)
)

// ResolveUpstreamURL validates the endpoint kind, region, action, and model
// resource, then resolves one of two explicit Bedrock surfaces:
//
//   - Runtime: /bedrock/model/... -> bedrock-runtime.../model/...
//   - Mantle:  /bedrock/anthropic/v1/messages -> bedrock-mantle.../anthropic/...
//
// Mantle stays deployment-opt-in because its IAM service, model coverage,
// quotas, features, stream wire, and AWS invocation-logging support differ from
// Runtime. Both surfaces remain provider=bedrock in telemetry.
func (a Adapter) ResolveUpstreamURL(ctx context.Context, req *http.Request, route providers.RouteContext) (*url.URL, error) {
	baseURL := a.BaseURL
	if route.BaseURL != "" {
		baseURL = route.BaseURL
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("bedrock base url invalid: %w", err)
	}
	region, err := resolveRegion(req, base)
	if err != nil {
		return nil, err
	}

	kind := endpointKindForPath(req.URL.Path)
	if configuredKind := strings.ToLower(strings.TrimSpace(route.EndpointKind)); configuredKind != "" && configuredKind != kind {
		return nil, fmt.Errorf("bedrock configured endpoint kind %q does not allow request kind %q", configuredKind, kind)
	}
	switch kind {
	case endpointRuntime:
		if !RegionAllowed(region) {
			return nil, fmt.Errorf("bedrock region %q is not on the allowlist", region)
		}
		modelID, action := parseModelPath(req.URL.Path)
		if modelID == "" || action == "" {
			return nil, fmt.Errorf("bedrock request path %q does not name a model and action", req.URL.Path)
		}
		if !actionAllowed(action) {
			return nil, fmt.Errorf("bedrock action %q is not allowed", action)
		}
		if !modelAllowed(modelID) {
			return nil, fmt.Errorf("bedrock model %q is not on the allowlist", modelID)
		}
		base.Path = strings.TrimRight(base.Path, "/") + strings.TrimPrefix(req.URL.Path, "/bedrock")
	case endpointMantle:
		if !env.Bool("CAVE_BEDROCK_MANTLE_ENABLED", false) {
			return nil, fmt.Errorf("bedrock Mantle endpoint is not enabled")
		}
		if !MantleRegionAllowed(region) {
			return nil, fmt.Errorf("bedrock Mantle region %q is not on the allowlist", region)
		}
		if !mantleActionAllowed(req.URL.Path) {
			return nil, fmt.Errorf("bedrock Mantle path %q is not allowed", req.URL.Path)
		}
		base, err = resolveMantleBase(base, region)
		if err != nil {
			return nil, err
		}
		base.Path = strings.TrimRight(base.Path, "/") + strings.TrimPrefix(req.URL.Path, "/bedrock")
	default:
		return nil, fmt.Errorf("bedrock request path %q is not allowed", req.URL.Path)
	}
	base.RawQuery = req.URL.RawQuery

	if env.IsProduction() {
		if err := ssrf.ValidateURL(ctx, base.String(), ssrf.ManagedConfig()); err != nil {
			return nil, err
		}
		hostKind, _, ok := bedrockHostKind(base.Hostname())
		if !ok || hostKind != kind {
			return nil, fmt.Errorf("bedrock endpoint host %q does not match endpoint kind %q", base.Hostname(), kind)
		}
	}
	return base, nil
}

func endpointKindForPath(path string) string {
	switch {
	case strings.HasPrefix(path, "/bedrock/model/"):
		return endpointRuntime
	case strings.HasPrefix(path, "/bedrock/anthropic/"):
		return endpointMantle
	default:
		return ""
	}
}

func mantleActionAllowed(path string) bool {
	switch path {
	case "/bedrock/anthropic/v1/messages", "/bedrock/anthropic/v1/messages/count_tokens":
		return true
	default:
		return false
	}
}

func resolveMantleBase(base *url.URL, region string) (*url.URL, error) {
	if kind, _, ok := bedrockHostKind(base.Hostname()); ok {
		if kind == endpointMantle {
			clone := *base
			return &clone, nil
		}
		return url.Parse(MantleBaseURL(region))
	}
	// Local/self-hosted test endpoints use the configured base and only swap the
	// path. Production rejects non-Bedrock hosts below.
	clone := *base
	return &clone, nil
}

// RuntimeBaseURL and MantleBaseURL construct endpoints only after callers have
// validated region with RegionAllowed.
func RuntimeBaseURL(region string) string {
	return "https://bedrock-runtime." + region + ".amazonaws.com"
}

func MantleBaseURL(region string) string {
	return "https://bedrock-mantle." + region + ".api.aws"
}

func actionAllowed(action string) bool {
	switch action {
	case "invoke", "invoke-with-response-stream", "converse", "converse-stream":
		return true
	default:
		return false
	}
}

// InspectRequest fills in request metadata. Bedrock carries the model id in the
// request path (/bedrock/model/{modelId}/…) rather than the body, so the model
// is resolved from the route path the proxy records in x-cave-route-path. The
// base inspector handles input bytes, stream flag, and message/tool counts.
func (a Adapter) InspectRequest(ctx context.Context, body providers.BodyReader, headers http.Header) (providers.RequestMetadata, error) {
	meta, err := a.Base.InspectRequest(ctx, body, headers)
	if err != nil {
		return meta, err
	}
	meta.Provider = a.Provider
	routePath := headers.Get("x-cave-route-path")
	switch endpointKindForPath(routePath) {
	case endpointRuntime:
		if modelID, action := parseModelPath(routePath); modelID != "" {
			meta.Model = modelID
			meta.Endpoint = action
		}
	case endpointMantle:
		if !env.Bool("CAVE_BEDROCK_MANTLE_ENABLED", false) {
			return meta, fmt.Errorf("bedrock Mantle endpoint is not enabled")
		}
		if !mantleActionAllowed(routePath) {
			return meta, fmt.Errorf("bedrock Mantle path %q is not allowed", routePath)
		}
		if meta.Model == "" || meta.Model == "unknown" || !mantleModelAllowed(meta.Model) {
			return meta, fmt.Errorf("bedrock model %q is not on the allowlist", meta.Model)
		}
		if strings.HasSuffix(routePath, "/count_tokens") {
			meta.Endpoint = "mantle_count_tokens"
		} else {
			meta.Endpoint = "mantle_messages"
		}
	}
	if region := strings.TrimSpace(headers.Get("x-cave-aws-region")); region != "" {
		meta.Region = region
	} else if region := regionFromHost(headers.Get("x-cave-bedrock-host")); region != "" {
		meta.Region = region
	} else {
		meta.Region = env.String("CAVE_BEDROCK_REGION", "us-east-1")
	}
	if headers.Get("x-amzn-bedrock-guardrail-identifier") != "" || headers.Get("x-amzn-bedrock-guardrail-version") != "" {
		// Guardrails are billed in text/image units outside model token rates.
		meta.PricingUnsupportedReason = "unsupported_provider_guardrail_charge"
	}
	if tier := strings.TrimSpace(headers.Get("x-amzn-bedrock-service-tier")); tier != "" {
		meta.ServiceTier = tier
	}
	// converse-stream / invoke-with-response-stream are streaming by path, not by
	// a body "stream" flag.
	if strings.Contains(routePath, "-stream") {
		meta.Stream = true
	}
	return meta, nil
}

// parseModelPath extracts the model id and action from a bedrock-runtime path of
// the form /bedrock/model/{modelId}/{action}. The model id may itself contain a
// version suffix with a colon (…-v2:0); only the trailing path segment is the
// action.
func parseModelPath(path string) (modelID, action string) {
	rest := strings.TrimPrefix(path, "/bedrock")
	rest = strings.TrimPrefix(rest, "/model/")
	if rest == path || rest == "" {
		return "", ""
	}
	idx := strings.LastIndex(rest, "/")
	if idx <= 0 || idx == len(rest)-1 {
		return "", ""
	}
	return rest[:idx], rest[idx+1:]
}

// resolveRegion keeps routing, allowlisting, and signing on one region. A
// recognized endpoint host is authoritative; a contradictory client header is
// rejected rather than validating one region and dialing another.
func resolveRegion(req *http.Request, base *url.URL) (string, error) {
	requested := strings.TrimSpace(req.Header.Get("x-cave-aws-region"))
	hostRegion := ""
	if base != nil {
		hostRegion = regionFromHost(base.Hostname())
	}
	if requested != "" && hostRegion != "" && requested != hostRegion {
		return "", fmt.Errorf("bedrock requested region %q does not match endpoint region %q", requested, hostRegion)
	}
	if hostRegion != "" {
		return hostRegion, nil
	}
	if requested != "" {
		return requested, nil
	}
	if r := regionFromHost(req.Header.Get("x-cave-bedrock-host")); r != "" {
		return r, nil
	}
	return env.String("CAVE_BEDROCK_REGION", "us-east-1"), nil
}

// signingRegion picks the AWS region the SigV4 scope is built for. The resolved
// upstream host wins so direct adapter callers cannot produce a valid signature
// scope for one region while forwarding to another.
func signingRegion(req *http.Request, upstream *url.URL) string {
	if upstream != nil {
		if r := regionFromHost(upstream.Hostname()); r != "" {
			return r
		}
	}
	if r := strings.TrimSpace(req.Header.Get("x-cave-aws-region")); r != "" {
		return r
	}
	return env.String("CAVE_BEDROCK_REGION", "us-east-1")
}

func regionFromHost(host string) string {
	_, region, ok := bedrockHostKind(host)
	if !ok {
		return ""
	}
	return region
}

func regionAllowed(region string) bool {
	return onAllowlist(region, allowlist("CAVE_BEDROCK_REGION_ALLOWLIST", defaultRegions), false)
}

// RegionAllowed is the shared closed region validator used by adapter routing
// and control-plane Bedrock connection metadata.
func RegionAllowed(region string) bool {
	return regionAllowed(strings.TrimSpace(region))
}

// APIKeyRegionAllowed validates the independently published Bedrock API-key
// region inventory. Operators may narrow it without widening Runtime support.
func APIKeyRegionAllowed(region string) bool {
	region = strings.TrimSpace(region)
	return RegionAllowed(region) &&
		onAllowlist(region, allowlist("CAVE_BEDROCK_API_KEY_REGION_ALLOWLIST", defaultAPIKeyRegions), false)
}

// MantleRegionAllowed validates the endpoint-specific Mantle inventory.
// Operators may narrow it without widening the shared Runtime inventory.
func MantleRegionAllowed(region string) bool {
	region = strings.TrimSpace(region)
	return RegionAllowed(region) &&
		onAllowlist(region, allowlist("CAVE_BEDROCK_MANTLE_REGION_ALLOWLIST", defaultMantleRegions), false)
}

func modelAllowed(modelID string) bool {
	rawAllowlist := env.String("CAVE_BEDROCK_MODEL_ALLOWLIST", "")
	if validBedrockARN(modelID) || validSageMakerEndpointARN(modelID) {
		if rawAllowlist != "" {
			return onAllowlist(modelID, splitAllowlist(rawAllowlist), true)
		}
		return true
	}
	if !validModelIdentifier(modelID) {
		return false
	}
	if rawAllowlist != "" {
		return onAllowlist(modelID, splitAllowlist(rawAllowlist), true)
	}
	return onAllowlist(StripInferenceProfileScope(modelID), defaultModelPrefixes, true)
}

// inferenceProfileScopes are the routing-scope prefixes AWS prepends to a base
// model id for global and geographic cross-region inference profiles
// (global.anthropic.claude-…, us.anthropic.claude-…). One list serves both the
// model allowlist and the cache-point eligibility predicate — when the two
// diverged, profile ids the router accepted were silently invisible to the
// cache transform.
var inferenceProfileScopes = []string{"global.", "us.", "eu.", "apac.", "jp.", "au."}

// StripInferenceProfileScope returns the model id minus at most one leading
// inference-profile routing scope. Ids without a scope pass through unchanged.
func StripInferenceProfileScope(modelID string) string {
	for _, scope := range inferenceProfileScopes {
		if strings.HasPrefix(modelID, scope) {
			return strings.TrimPrefix(modelID, scope)
		}
	}
	return modelID
}

func mantleModelAllowed(modelID string) bool {
	if !validModelIdentifier(modelID) {
		return false
	}
	// The implemented Mantle lane is specifically the Anthropic Messages wire.
	// AWS's OpenAI-compatible Mantle surfaces are separate future routes.
	return strings.HasPrefix(modelID, "anthropic.claude-")
}

func validModelIdentifier(modelID string) bool {
	if modelID == "" || len(modelID) > 512 {
		return false
	}
	for _, r := range modelID {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case strings.ContainsRune("._:-", r):
		default:
			return false
		}
	}
	return true
}

func validBedrockARN(modelID string) bool {
	parts := strings.SplitN(modelID, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || (parts[1] != "aws" && parts[1] != "aws-us-gov") || parts[2] != "bedrock" {
		return false
	}
	if !RegionAllowed(parts[3]) || strings.ContainsAny(parts[5], " \t\r\n") {
		return false
	}
	resource := parts[5]
	switch {
	case foundationModelResource.MatchString(resource):
		return parts[4] == ""
	case customModelResource.MatchString(resource),
		accountModelResource.MatchString(resource),
		inferenceProfileResource.MatchString(resource),
		promptResource.MatchString(resource),
		promptRouterResource.MatchString(resource):
		return accountIDPattern.MatchString(parts[4])
	default:
		return false
	}
}

func validSageMakerEndpointARN(modelID string) bool {
	parts := strings.SplitN(modelID, ":", 6)
	return len(parts) == 6 &&
		parts[0] == "arn" &&
		(parts[1] == "aws" || parts[1] == "aws-us-gov") &&
		parts[2] == "sagemaker" &&
		RegionAllowed(parts[3]) &&
		accountIDPattern.MatchString(parts[4]) &&
		sageMakerEndpointResource.MatchString(parts[5])
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
		out := splitAllowlist(raw)
		if len(out) > 0 {
			return out
		}
	}
	return fallback
}

func splitAllowlist(raw string) []string {
	out := []string{}
	for _, v := range strings.Split(raw, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func isBedrockHost(host string) bool {
	_, _, ok := bedrockHostKind(host)
	return ok
}

func bedrockHostKind(host string) (kind, region string, ok bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	parts := strings.Split(host, ".")
	if len(parts) != 4 {
		return "", "", false
	}
	switch {
	case (parts[0] == "bedrock-runtime" || parts[0] == "bedrock-runtime-fips") && parts[2] == "amazonaws" && parts[3] == "com":
		kind = endpointRuntime
	case parts[0] == "bedrock-mantle" && parts[2] == "api" && parts[3] == "aws":
		kind = endpointMantle
	default:
		return "", "", false
	}
	if !RegionAllowed(parts[1]) {
		return "", "", false
	}
	return kind, parts[1], true
}
