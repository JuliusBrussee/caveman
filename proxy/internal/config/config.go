// Package config loads the standalone proxy's caveman.yaml and resolves BYOK
// provider keys from the environment. Secrets never live in the YAML file — only
// the mode, listen address, optimizer flags, and per-provider base URLs do; the
// API keys are read from the environment at request time.
package config

import (
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/openaicompat"
	"github.com/JuliusBrussee/caveman/shared/platform/env"
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
	// BreakpointPlan selects the cache-breakpoint planner. It is DEFAULT OFF: only
	// the explicit value "frontier" turns it on, and "", "off", and any
	// unrecognized value all mean off. The planner adds provider-native cache
	// metadata (Anthropic cache_control, OpenAI prompt_cache_key) to the upstream
	// request only, so it changes no model-visible bytes — but it stays off until
	// the escalation ladder has priced it.
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
	return cfg, nil
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
	// Same normalization discipline: one spelling of off, so the decision point is
	// a single equality against "frontier".
	switch c.BreakpointPlan {
	case "frontier":
	default:
		c.BreakpointPlan = "off"
	}
	if c.Optimizers == nil {
		c.Optimizers = map[string]bool{}
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

// CompatCredential returns the named OpenAI-compatible upstream key, plus
// whether that upstream exists. An empty api_key_env intentionally means no
// Authorization header for that named upstream.
func (c Config) CompatCredential(name string) (string, bool) {
	upstream, ok := c.Compat[name]
	if !ok {
		return "", false
	}
	keyEnv := strings.TrimSpace(upstream.APIKeyEnv)
	if keyEnv == "" {
		return "", true
	}
	return env.String(keyEnv, ""), true
}
