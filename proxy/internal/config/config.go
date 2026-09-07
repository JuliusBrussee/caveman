// Package config loads the standalone proxy's caveman.yaml and resolves BYOK
// provider keys from the environment. Secrets never live in the YAML file — only
// the mode, listen address, optimizer flags, and per-provider base URLs do; the
// API keys are read from the environment at request time.
package config

import (
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/openaicompat"
	"github.com/JuliusBrussee/caveman/shared/platform/cabundle"
	"github.com/JuliusBrussee/caveman/shared/platform/env"
	"golang.org/x/net/http/httpproxy"
	"gopkg.in/yaml.v3"
)

// DefaultListen is where standalone mode binds when caveman.yaml does not say.
const DefaultListen = "127.0.0.1:8787"

// DefaultBedrockRegion is AWS's documented default in Caveman's standalone
// setup. Operators can pin another source region in caveman.yaml or environment.
const DefaultBedrockRegion = "us-east-1"

// Config is the parsed caveman.yaml plus its defaults.
type Config struct {
	// Label tags local telemetry rows. Trial runs set this through CAVEMAN_LABEL
	// (e.g. "trial:trial_...") so reports can isolate one wrapped session.
	Label string `yaml:"label"`
	// Mode is the runtime mode: "record" (default, always pass-through) or a more
	// aggressive mode that applies byte-safe optimizers. Unknown modes fall back
	// to "record" (fail closed).
	Mode string `yaml:"mode"`
	// Listen is the host:port standalone mode binds to.
	Listen string `yaml:"listen"`
	// Optimizers gates provider-native optimizers by id.
	Optimizers map[string]bool `yaml:"optimizers"`
	// SubscriptionCompress is the operator off-switch for subscription-auth
	// live-zone compression. Empty/default and "live_zone" both mean "allowed";
	// "off" disables it; unknown values fail closed to "off". There is
	// no account gate alongside it — local compression runs without a Caveman
	// account. Subscription rows stay tokens-only and dollar-free regardless.
	SubscriptionCompress string `yaml:"subscription_compress"`
	// ToolSchemaStrip selects the tool-schema annotation strip. It is DEFAULT OFF:
	// only the explicit value "annotations" turns it on, and "", "off", and any
	// unrecognized value all mean off. The strip changes model-visible bytes, so it
	// may only default on under the local-wrap clause (recovery + CCR) —
	// which it does not; it stays an explicit opt-in.
	ToolSchemaStrip string `yaml:"toolschema_strip"`
	// BreakpointPlan selects the cache-breakpoint planner. Empty defaults to
	// "frontier" so optimization modes need no cache-specific setup. Explicit
	// "off" and unrecognized values fail closed. Planner metadata changes no
	// model-visible bytes; record mode remains an unconditional pass-through.
	BreakpointPlan string `yaml:"breakpoint_plan"`
	// ObserveEstimate turns on record-mode observe-only estimation. When true AND
	// Mode is "record", the proxy runs the compressor on COPIES of each live-zone
	// segment to measure the tokens compression WOULD have cut, without ever
	// mutating the forwarded request and without storing any CCR original. It is
	// set only through the CAVEMAN_OBSERVE_ESTIMATE env. It never books a saving —
	// record mode stays byte-safe pass-through and savings_usd stays 0.
	ObserveEstimate bool `yaml:"-"`
	// Providers carries per-provider base-URL overrides (e.g. an Azure resource or
	// a self-hosted OpenAI-compatible endpoint).
	Providers map[string]ProviderConfig `yaml:"providers"`
	// Compat carries named OpenAI-compatible upstreams mounted at /compat/<name>/.
	Compat map[string]CompatConfig `yaml:"compat"`
	// UpstreamProxy routes provider traffic through an HTTP proxy. Empty and
	// "env" (the default) honour HTTPS_PROXY/HTTP_PROXY/NO_PROXY like curl and
	// every other tool on the host; "off" dials providers directly regardless;
	// a URL (http://, https://, socks5://, optional user:pass@) pins one proxy
	// for provider traffic only, without exporting process-wide proxy variables
	// that the wrapped agent's tool executions would inherit. Also set via
	// CAVE_UPSTREAM_PROXY. See ssrf.Config.Proxy for the guard contract.
	UpstreamProxy string `yaml:"upstream_proxy"`
	// CABundle is a PEM file of extra roots to trust for provider TLS, on top of
	// the system store — what corporate TLS inspection (Zscaler, Netskope, …)
	// needs. Also set via CAVE_CA_BUNDLE. Independently of this key, bundles
	// named by SSL_CERT_FILE, REQUESTS_CA_BUNDLE and NODE_EXTRA_CA_CERTS are
	// appended too, so an environment already set up for curl, Python or Claude
	// Code works unchanged. All bundles are additive; a corrupt one fails Load.
	CABundle string `yaml:"ca_bundle"`
	// SkippedCABundles lists inherited CA env vars (never ca_bundle itself)
	// whose file was missing or unusable. Load skips them rather than refusing
	// to start — Go's own loader and Node both tolerate a bad inherited bundle,
	// and the SSL_CERT_FILE-points-at-a-directory mixup is common — and the
	// binary logs them at startup. An unusable bundle contributes nothing, never
	// a partial set of roots.
	SkippedCABundles []string `yaml:"-"`

	rootCAs *x509.CertPool
}

// ProviderConfig is the per-provider configuration in caveman.yaml.
type ProviderConfig struct {
	BaseURL     string `yaml:"base_url"`
	BillingTier string `yaml:"billing_tier"`
	Region      string `yaml:"region"`
}

// CompatConfig is one named OpenAI-compatible upstream in caveman.yaml.
type CompatConfig struct {
	BaseURL   string `yaml:"base_url"`
	APIKeyEnv string `yaml:"api_key_env"`
}

// knownModes is the set of accepted runtime modes; anything else fails closed to
// "record" so an unrecognized config can never silently enable transforms.
// "compress" is the S4 lossy mode: it runs the content compressor on the upstream
// request and is recoverable via CCR — it only does anything when a compressor
// seam is wired (otherwise it falls back to a record-mode pass-through). "pixel"
// is the S4 lossy text-to-PNG mode: it runs only for allowlisted models and is
// CCR-recoverable, otherwise it passes through unchanged.
var knownModes = map[string]bool{"record": true, "recommend": true, "shadow": true, "canary": true, "active": true, "compress": true, "pixel": true}

// Load reads caveman.yaml from path, applying defaults. A missing file yields the
// default config (record mode on 127.0.0.1:8787) rather than an error: a bare
// `caveman start` with no config file is a valid record-only session.
func Load(path string) (Config, error) {
	cfg := Config{}
	raw, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		// no file — defaults only
	case err != nil:
		return cfg, err
	default:
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return cfg, err
		}
	}
	cfg = cfg.withDefaults()
	if err := validateListen(cfg.Listen); err != nil {
		return Config{}, err
	}
	if err := cfg.validateCompat(); err != nil {
		return Config{}, err
	}
	if _, err := parseUpstreamProxy(cfg.UpstreamProxy); err != nil {
		return Config{}, err
	}
	if err := cfg.loadRootCAs(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// inheritedCABundleEnv names the CA bundle variables other toolchains already
// read: Go/OpenSSL, Python requests, and Node (which Claude Code runs on).
var inheritedCABundleEnv = []string{"SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "NODE_EXTRA_CA_CERTS"}

// loadRootCAs builds the provider trust store. It stays nil — Go's default
// verification — when no bundle is configured, so the common case keeps the
// platform verifier untouched.
func (c *Config) loadRootCAs() error {
	var certs []*x509.Certificate
	if c.CABundle = strings.TrimSpace(c.CABundle); c.CABundle != "" {
		loaded, err := cabundle.Certificates(c.CABundle)
		if err != nil {
			return fmt.Errorf("ca_bundle: %w", err)
		}
		certs = append(certs, loaded...)
	}
	for _, name := range inheritedCABundleEnv {
		path := strings.TrimSpace(env.String(name, ""))
		if path == "" {
			continue
		}
		loaded, err := cabundle.Certificates(path)
		if err != nil {
			c.SkippedCABundles = append(c.SkippedCABundles, name+": "+err.Error())
			continue
		}
		certs = append(certs, loaded...)
	}
	if len(certs) == 0 {
		return nil
	}
	pool, err := cabundle.PoolOf(certs)
	if err != nil {
		return fmt.Errorf("ca bundle: %w", err)
	}
	c.rootCAs = pool
	return nil
}

// RootCAs returns the provider TLS trust store, or nil for Go's default.
func (c Config) RootCAs() *x509.CertPool { return c.rootCAs }

// UpstreamProxyFunc returns the Transport.Proxy selector for UpstreamProxy, or
// nil for a direct client. Load has already rejected unparseable values; a
// Config built by hand with a bad value panics rather than silently dialing
// direct, which would be #1001's symptom with no diagnostic.
func (c Config) UpstreamProxyFunc() func(*http.Request) (*url.URL, error) {
	fn, err := parseUpstreamProxy(c.UpstreamProxy)
	if err != nil {
		panic(err)
	}
	return fn
}

func parseUpstreamProxy(raw string) (func(*http.Request) (*url.URL, error), error) {
	raw = strings.TrimSpace(raw)
	switch strings.ToLower(raw) {
	case "", "env":
		// ProxyFromEnvironment snapshots the proxy variables once per process
		// (sync.Once), so a test that t.Setenv's HTTPS_PROXY must build its own
		// httpproxy.Config selector instead — see ssrf_test.go.
		return http.ProxyFromEnvironment, nil
	case "off":
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("upstream_proxy %q must be \"env\", \"off\" or a proxy URL", raw)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("upstream_proxy %q: unsupported scheme %q", raw, u.Scheme)
	}
	// Same selector semantics as env mode: localhost/loopback destinations (an
	// allowlisted Ollama) and NO_PROXY matches are dialed direct rather than
	// handed to a corporate proxy that cannot reach them.
	selector := (&httpproxy.Config{HTTPProxy: raw, HTTPSProxy: raw, NoProxy: env.String("NO_PROXY", env.String("no_proxy", ""))}).ProxyFunc()
	return func(req *http.Request) (*url.URL, error) { return selector(req.URL) }, nil
}

// validateListen keeps standalone's unauthenticated BYOK proxy local to one
// operator. Binding an empty, wildcard, or non-loopback host would expose every
// configured provider credential to the network with no inbound authentication.
func validateListen(listen string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil || port == "" {
		return fmt.Errorf("listen address %q must be loopback host:port", listen)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("listen address %q is not loopback; standalone proxy has no inbound authentication", listen)
	}
	return nil
}

func (c Config) withDefaults() Config {
	if label := env.String("CAVEMAN_LABEL", ""); label != "" {
		c.Label = label
	}
	if c.Label == "" {
		c.Label = "local"
	}
	if mode := env.String("CAVEMAN_MODE", ""); mode != "" {
		c.Mode = mode
	}
	if listen := env.String("CAVEMAN_LISTEN", ""); listen != "" {
		c.Listen = listen
	}
	if sub := env.String("CAVEMAN_SUBSCRIPTION_COMPRESS", ""); sub != "" {
		c.SubscriptionCompress = sub
	}
	if strip := env.String("CAVEMAN_TOOLSCHEMA_STRIP", ""); strip != "" {
		c.ToolSchemaStrip = strip
	}
	if plan := env.String("CAVEMAN_BREAKPOINT_PLAN", ""); plan != "" {
		c.BreakpointPlan = plan
	}
	if env.Bool("CAVEMAN_OBSERVE_ESTIMATE", false) {
		c.ObserveEstimate = true
	}
	if proxy := env.String("CAVE_UPSTREAM_PROXY", ""); proxy != "" {
		c.UpstreamProxy = proxy
	}
	if bundle := env.String("CAVE_CA_BUNDLE", ""); bundle != "" {
		c.CABundle = bundle
	}
	if c.Listen == "" {
		c.Listen = DefaultListen
	}
	if !knownModes[c.Mode] {
		c.Mode = "record"
	}
	switch c.SubscriptionCompress {
	case "", "live_zone", "off":
	default:
		c.SubscriptionCompress = "off"
	}
	// Normalize to one spelling of off so the decision point is a single equality
	// against "annotations"; an unrecognized value can never read as enabled.
	switch c.ToolSchemaStrip {
	case "annotations":
	default:
		c.ToolSchemaStrip = "off"
	}
	// Cache planning is safe metadata and defaults on in optimization modes. Keep
	// one explicit off spelling and fail closed for unknown values.
	switch c.BreakpointPlan {
	case "":
		c.BreakpointPlan = "frontier"
	case "frontier", "off":
	default:
		c.BreakpointPlan = "off"
	}
	if c.Optimizers == nil {
		c.Optimizers = map[string]bool{}
	}
	for _, optimizerID := range []string{"anthropic-cache-breakpoints", "openai-prompt-cache-key", "bedrock-cache-points"} {
		if _, configured := c.Optimizers[optimizerID]; !configured {
			c.Optimizers[optimizerID] = true
		}
	}
	return c
}

// BaseURL returns the configured base URL for a provider, or the supplied default.
func (c Config) BaseURL(provider, fallback string) string {
	if pc, ok := c.Providers[provider]; ok && pc.BaseURL != "" {
		return pc.BaseURL
	}
	return fallback
}

// BillingTiers returns only trusted, recognized provider billing modes. Unknown
// values are omitted so cost accounting fails closed rather than guessing.
func (c Config) BillingTiers() map[string]string {
	out := map[string]string{}
	for provider, pc := range c.Providers {
		switch tier := strings.ToLower(strings.TrimSpace(pc.BillingTier)); tier {
		case "paid", "free":
			out[provider] = tier
		}
	}
	return out
}

// BedrockRegion resolves the source/signing region from trusted operator
// configuration. A provider-specific YAML value wins, followed by Caveman's
// explicit override and the standard AWS SDK region variables.
func (c Config) BedrockRegion() string {
	if pc, ok := c.Providers["bedrock"]; ok {
		if region := strings.TrimSpace(pc.Region); region != "" {
			return region
		}
	}
	for _, key := range []string{"CAVE_BEDROCK_REGION", "AWS_REGION", "AWS_DEFAULT_REGION"} {
		if region := strings.TrimSpace(env.String(key, "")); region != "" {
			return region
		}
	}
	return DefaultBedrockRegion
}

// BedrockBaseURL returns an explicit operator override or derives AWS's standard
// Runtime endpoint from BedrockRegion. Callers never need to paste a raw URL for
// the normal first-party path.
func (c Config) BedrockBaseURL() string {
	if configured := c.BaseURL("bedrock", ""); configured != "" {
		return configured
	}
	return fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com", c.BedrockRegion())
}

func (c Config) validateCompat() error {
	for name, upstream := range c.Compat {
		if err := openaicompat.ValidateName(name); err != nil {
			return fmt.Errorf("compat upstream %q: %w", name, err)
		}
		if strings.TrimSpace(upstream.BaseURL) == "" {
			return fmt.Errorf("compat upstream %q: base_url is required", name)
		}
		if err := openaicompat.ValidateBaseURL(upstream.BaseURL); err != nil {
			return fmt.Errorf("compat upstream %q: base_url: %w", name, err)
		}
	}
	return nil
}

// providerEnvKey maps a provider name to the BYOK environment variable that
// holds its API key.
var providerEnvKey = map[string]string{
	"anthropic":         "ANTHROPIC_API_KEY",
	"openai":            "OPENAI_API_KEY",
	"gemini":            "GEMINI_API_KEY",
	"azure_openai":      "AZURE_OPENAI_API_KEY",
	"openai_compatible": "OPENAI_COMPAT_API_KEY",
}

// Credential returns the provider credential resolved from the environment.
// Empty means the caller must fail closed or use an explicit inbound credential.
// Bedrock bearer keys are PAYG credentials even though their wire scheme is
// Authorization: Bearer, so AuthKind carries that provider-specific truth.
func (c Config) Credential(provider string) providers.Credential {
	if provider == "bedrock" {
		if key := env.String("AWS_BEARER_TOKEN_BEDROCK", ""); key != "" {
			return providers.Credential{
				Mode:     "ephemeral_header",
				Key:      key,
				AuthKind: "bedrock_api_key",
				Scheme:   "bearer",
			}
		}
		accessKey := strings.TrimSpace(env.String("AWS_ACCESS_KEY_ID", ""))
		secretKey := strings.TrimSpace(env.String("AWS_SECRET_ACCESS_KEY", ""))
		// A partial IAM pair is never useful and must not fall through as an
		// apparently valid credential. Return empty so Bedrock fails closed before
		// any unsigned upstream request can be sent.
		if accessKey == "" || secretKey == "" {
			return providers.Credential{Mode: "ephemeral_header"}
		}
		key := accessKey + ":" + secretKey
		if sessionToken := strings.TrimSpace(env.String("AWS_SESSION_TOKEN", "")); sessionToken != "" {
			key += ":" + sessionToken
		}
		return providers.Credential{
			Mode:     "ephemeral_header",
			Key:      key,
			AuthKind: "aws_access_keys",
		}
	}
	if key, ok := providerEnvKey[provider]; ok {
		return providers.Credential{
			Mode:            "ephemeral_header",
			Key:             env.String(key, ""),
			AuthFallbackEnv: key,
		}
	}
	return providers.Credential{Mode: "ephemeral_header"}
}

// builtinCompat holds the named OpenAI-compatible upstreams that work with no
// user config. The standalone proxy mounts each one at /compat/<name>/, and
// CompatCredential resolves its key. Thus one table gives a mount and its BYOK
// policy. A caveman.yaml compat entry with the same name replaces the built-in
// entry.
var builtinCompat = map[string]CompatConfig{
	// OpenCode Go uses the OpenAI and Anthropic wire protocols, but its upstream
	// is not api.openai.com. Its key comes from OPENCODE_API_KEY, as in Pi.
	"opencode-go": {BaseURL: "https://opencode.ai/zen/go", APIKeyEnv: "OPENCODE_API_KEY"},
}

// CompatUpstreams returns every named OpenAI-compatible upstream that the proxy
// mounts. The result starts with the built-in table. Then the caveman.yaml
// entries go on top, so a user entry with a built-in name replaces the built-in
// entry. The result is a new map.
func (c Config) CompatUpstreams() map[string]CompatConfig {
	merged := make(map[string]CompatConfig, len(builtinCompat)+len(c.Compat))
	for name, upstream := range builtinCompat {
		merged[name] = upstream
	}
	for name, upstream := range c.Compat {
		merged[name] = upstream
	}
	return merged
}

// CompatCredential returns the key of one named OpenAI-compatible upstream. The
// second result is false if no built-in entry and no configured entry has this
// name. An empty api_key_env means that the upstream needs no Authorization
// header.
func (c Config) CompatCredential(name string) (string, bool) {
	upstream, ok := c.CompatUpstreams()[name]
	if !ok {
		return "", false
	}
	keyEnv := strings.TrimSpace(upstream.APIKeyEnv)
	if keyEnv == "" {
		return "", true
	}
	return env.String(keyEnv, ""), true
}
