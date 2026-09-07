package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/providers/openaicompat"
)

// TestMain isolates the suite from the host's corporate-network variables,
// which Load now reads (#1001).
func TestMain(m *testing.M) {
	for _, name := range []string{"CAVE_UPSTREAM_PROXY", "CAVE_CA_BUNDLE", "NO_PROXY", "no_proxy", "SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "NODE_EXTRA_CA_CERTS"} {
		os.Unsetenv(name)
	}
	os.Exit(m.Run())
}

func TestLoad_MissingFileYieldsRecordDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if cfg.Mode != "record" {
		t.Errorf("mode = %q, want record (safe default)", cfg.Mode)
	}
	if cfg.Listen != DefaultListen {
		t.Errorf("listen = %q, want %q", cfg.Listen, DefaultListen)
	}
}

func TestLoad_RejectsNonLoopbackListen(t *testing.T) {
	for _, listen := range []string{"0.0.0.0:8787", "[::]:8787", ":8787", "192.0.2.1:8787"} {
		t.Run(listen, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "caveman.yaml")
			if err := os.WriteFile(path, []byte("listen: \""+listen+"\"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatalf("Load accepted unauthenticated non-loopback listen %q", listen)
			}
		})
	}
}

func TestLoad_AcceptsLoopbackListen(t *testing.T) {
	for _, listen := range []string{"127.0.0.2:8787", "[::1]:8787", "localhost:8787"} {
		t.Run(listen, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "caveman.yaml")
			if err := os.WriteFile(path, []byte("listen: \""+listen+"\"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load rejected loopback listen %q: %v", listen, err)
			}
			if cfg.Listen != listen {
				t.Fatalf("listen = %q, want %q", cfg.Listen, listen)
			}
		})
	}
}

func TestLoad_UnknownModeFailsClosedToRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caveman.yaml")
	if err := os.WriteFile(path, []byte("mode: yolo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Mode != "record" {
		t.Errorf("unknown mode = %q, want it to fail closed to record", cfg.Mode)
	}
}

func TestLoad_CompressModeAccepted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caveman.yaml")
	if err := os.WriteFile(path, []byte("mode: compress\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Mode != "compress" {
		t.Errorf("mode = %q, want compress (a known S4 mode, not failed closed)", cfg.Mode)
	}
}

// A bare config leaves the subscription off-switch at its permissive default:
// local compression needs no account, so an empty value means
// "allowed" and only an explicit "off" (or an unknown value) closes it.
func TestLoad_SubscriptionCompressDefaultsToAllowed(t *testing.T) {
	t.Setenv("CAVEMAN_SUBSCRIPTION_COMPRESS", "")
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SubscriptionCompress != "" {
		t.Fatalf("subscription_compress = %q, want empty default", cfg.SubscriptionCompress)
	}
}

func TestLoad_SubscriptionCompressLiveZoneAccepted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caveman.yaml")
	if err := os.WriteFile(path, []byte("subscription_compress: live_zone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SubscriptionCompress != "live_zone" {
		t.Fatalf("subscription_compress = %q, want live_zone", cfg.SubscriptionCompress)
	}
}

func TestLoad_SubscriptionCompressEnvOverrideAndUnknownFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caveman.yaml")
	if err := os.WriteFile(path, []byte("subscription_compress: live_zone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAVEMAN_SUBSCRIPTION_COMPRESS", "unknown")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// Empty now means "allowed when entitled", so an unrecognized value must fail
	// closed to the explicit off-switch rather than to the default.
	if cfg.SubscriptionCompress != "off" {
		t.Fatalf("unknown env subscription_compress = %q, want fail-closed off", cfg.SubscriptionCompress)
	}

	t.Setenv("CAVEMAN_SUBSCRIPTION_COMPRESS", "off")
	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SubscriptionCompress != "off" {
		t.Fatalf("env subscription_compress = %q, want off", cfg.SubscriptionCompress)
	}

	t.Setenv("CAVEMAN_SUBSCRIPTION_COMPRESS", "live_zone")
	cfg, err = Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("load with env: %v", err)
	}
	if cfg.SubscriptionCompress != "live_zone" {
		t.Fatalf("env subscription_compress = %q, want live_zone", cfg.SubscriptionCompress)
	}
}

func TestLoad_ParsesProvidersAndOptimizers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caveman.yaml")
	yaml := "mode: active\n" +
		"optimizers:\n  anthropic-cache-breakpoints: true\n" +
		"providers:\n  openai:\n    base_url: https://example.test/v1\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Mode != "active" {
		t.Errorf("mode = %q, want active", cfg.Mode)
	}
	if !cfg.Optimizers["anthropic-cache-breakpoints"] {
		t.Error("expected anthropic-cache-breakpoints optimizer enabled")
	}
	if got := cfg.BaseURL("openai", "default"); got != "https://example.test/v1" {
		t.Errorf("openai base url = %q, want the configured override", got)
	}
	if got := cfg.BaseURL("anthropic", "fallback"); got != "fallback" {
		t.Errorf("unconfigured provider base url = %q, want the fallback", got)
	}
}

func TestLoad_CacheOptimizersDefaultOnWithExplicitOff(t *testing.T) {
	t.Setenv("CAVEMAN_BREAKPOINT_PLAN", "")
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, optimizerID := range []string{"anthropic-cache-breakpoints", "openai-prompt-cache-key", "bedrock-cache-points"} {
		if !cfg.Optimizers[optimizerID] {
			t.Fatalf("default optimizer %q disabled: %#v", optimizerID, cfg.Optimizers)
		}
	}

	path := filepath.Join(t.TempDir(), "caveman.yaml")
	if err := os.WriteFile(path, []byte("optimizers:\n  openai-prompt-cache-key: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("load explicit off: %v", err)
	}
	if cfg.Optimizers["openai-prompt-cache-key"] {
		t.Fatal("explicit OpenAI cache off-switch was overwritten")
	}
	if !cfg.Optimizers["anthropic-cache-breakpoints"] || !cfg.Optimizers["bedrock-cache-points"] {
		t.Fatalf("unconfigured cache defaults lost: %#v", cfg.Optimizers)
	}
}

func TestLoad_ParsesCompatUpstreams(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caveman.yaml")
	yaml := "compat:\n" +
		"  openrouter:\n" +
		"    base_url: https://openrouter.ai/api\n" +
		"    api_key_env: OPENROUTER_API_KEY\n" +
		"  ollama:\n" +
		"    base_url: http://localhost:11434\n" +
		"    api_key_env: \"\"\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.Compat["openrouter"].BaseURL; got != "https://openrouter.ai/api" {
		t.Errorf("openrouter base_url = %q, want configured URL", got)
	}
	if got := cfg.Compat["openrouter"].APIKeyEnv; got != "OPENROUTER_API_KEY" {
		t.Errorf("openrouter api_key_env = %q, want OPENROUTER_API_KEY", got)
	}
	if got := cfg.Compat["ollama"].APIKeyEnv; got != "" {
		t.Errorf("ollama api_key_env = %q, want empty", got)
	}
}

func TestLoad_CompatMalformedErrors(t *testing.T) {
	cases := map[string]string{
		"invalid name": "compat:\n  Groq:\n    base_url: https://api.example.test\n",
		"reserved":     "compat:\n  stub:\n    base_url: https://api.example.test\n",
		"missing url":  "compat:\n  groq:\n    api_key_env: GROQ_API_KEY\n",
		"bad url":      "compat:\n  groq:\n    base_url: ://bad\n",
		"bad shape":    "compat:\n  groq: []\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "caveman.yaml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("Load succeeded, want malformed compat block error")
			}
		})
	}
}

func TestCredential_ReadsBYOKEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test-openai")
	cfg := Config{}
	if got := cfg.Credential("openai"); got.Key != "sk-test-openai" || got.AuthFallbackEnv != "OPENAI_API_KEY" {
		t.Errorf("openai credential = %+v, want key sk-test-openai", got)
	}
}

func TestCredential_BedrockBearerPrecedesIAM(t *testing.T) {
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "bedrock-bearer")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	cfg := Config{}

	got := cfg.Credential("bedrock")
	if got.Key != "bedrock-bearer" || got.Scheme != "bearer" || got.AuthKind != "bedrock_api_key" {
		t.Fatalf("bedrock credential = %+v, want bearer API-key credential", got)
	}
}

func TestCredential_BedrockIAMRequiresCompletePair(t *testing.T) {
	for _, tc := range []struct {
		name      string
		accessKey string
		secretKey string
		session   string
		wantKey   string
		wantKind  string
	}{
		{name: "long lived", accessKey: "AKIAEXAMPLE", secretKey: "secret", wantKey: "AKIAEXAMPLE:secret", wantKind: "aws_access_keys"},
		{name: "temporary", accessKey: "ASIAEXAMPLE", secretKey: "secret", session: "session-token", wantKey: "ASIAEXAMPLE:secret:session-token", wantKind: "aws_access_keys"},
		{name: "access only fails closed", accessKey: "AKIAEXAMPLE"},
		{name: "secret only fails closed", secretKey: "secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "")
			t.Setenv("AWS_ACCESS_KEY_ID", tc.accessKey)
			t.Setenv("AWS_SECRET_ACCESS_KEY", tc.secretKey)
			t.Setenv("AWS_SESSION_TOKEN", tc.session)

			got := (Config{}).Credential("bedrock")
			if got.Key != tc.wantKey || got.AuthKind != tc.wantKind {
				t.Fatalf("bedrock IAM credential = %+v, want key %q kind %q", got, tc.wantKey, tc.wantKind)
			}
		})
	}
}

func TestBedrockRegionPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured string
		cave       string
		aws        string
		awsDefault string
		want       string
	}{
		{name: "configured", configured: "eu-west-1", cave: "us-west-2", aws: "us-east-2", awsDefault: "ap-southeast-1", want: "eu-west-1"},
		{name: "caveman env", cave: "us-west-2", aws: "us-east-2", awsDefault: "ap-southeast-1", want: "us-west-2"},
		{name: "aws env", aws: "us-east-2", awsDefault: "ap-southeast-1", want: "us-east-2"},
		{name: "aws default env", awsDefault: "ap-southeast-1", want: "ap-southeast-1"},
		{name: "documented default", want: DefaultBedrockRegion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CAVE_BEDROCK_REGION", tc.cave)
			t.Setenv("AWS_REGION", tc.aws)
			t.Setenv("AWS_DEFAULT_REGION", tc.awsDefault)
			cfg := Config{Providers: map[string]ProviderConfig{
				"bedrock": {Region: tc.configured},
			}}
			if got := cfg.BedrockRegion(); got != tc.want {
				t.Fatalf("BedrockRegion() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCompatCredential_UsesPerNameEnvAndEmptyMeansNoAuth(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-openrouter")
	cfg := Config{Compat: map[string]CompatConfig{
		"openrouter": {BaseURL: "https://openrouter.ai/api", APIKeyEnv: "OPENROUTER_API_KEY"},
		"ollama":     {BaseURL: "http://localhost:11434", APIKeyEnv: ""},
	}}
	if got, ok := cfg.CompatCredential("openrouter"); !ok || got != "sk-openrouter" {
		t.Errorf("openrouter credential = (%q,%v), want (sk-openrouter,true)", got, ok)
	}
	if got, ok := cfg.CompatCredential("ollama"); !ok || got != "" {
		t.Errorf("ollama credential = (%q,%v), want empty credential with ok=true", got, ok)
	}
	if got, ok := cfg.CompatCredential("missing"); ok || got != "" {
		t.Errorf("missing credential = (%q,%v), want (\"\",false)", got, ok)
	}
}

// A built-in compat mount has its own BYOK policy with no user config. Thus a
// keyless request never uses the OPENAI_COMPAT_API_KEY secret.
func TestCompatCredential_BuiltinMountReadsItsOwnEnv(t *testing.T) {
	t.Setenv("OPENCODE_API_KEY", "sk-opencode")
	t.Setenv("OPENAI_COMPAT_API_KEY", "sk-legacy")
	if got, ok := (Config{}).CompatCredential("opencode-go"); !ok || got != "sk-opencode" {
		t.Errorf("opencode-go credential = (%q,%v), want (sk-opencode,true)", got, ok)
	}
}

func TestCompatUpstreams_UserEntryWins(t *testing.T) {
	builtin := Config{}.CompatUpstreams()
	if got := builtin["opencode-go"].BaseURL; got != "https://opencode.ai/zen/go" {
		t.Fatalf("built-in opencode-go base_url = %q, want the OpenCode Go upstream", got)
	}
	user := CompatConfig{BaseURL: "https://opencode.example.test", APIKeyEnv: "OPENCODE_ZEN_API_KEY"}
	cfg := Config{Compat: map[string]CompatConfig{
		"opencode-go": user,
		"openrouter":  {BaseURL: "https://openrouter.ai/api", APIKeyEnv: "OPENROUTER_API_KEY"},
	}}
	merged := cfg.CompatUpstreams()
	if got := merged["opencode-go"]; got != user {
		t.Errorf("opencode-go upstream = %+v, want the user entry %+v", got, user)
	}
	if _, ok := merged["openrouter"]; !ok {
		t.Errorf("user-only upstream openrouter missing from %v", merged)
	}
	if len(merged) != 2 {
		t.Errorf("merged upstreams = %v, want exactly the built-in and user names", merged)
	}
	if _, ok := cfg.Compat["opencode-go"]; !ok || len(cfg.Compat) != 2 {
		t.Errorf("CompatUpstreams must not mutate cfg.Compat: %v", cfg.Compat)
	}
}

// buildAdapters panics on a compat entry that fails validation. Only config.Load
// validates the user entries. This test validates the built-in entries.
func TestBuiltinCompat_EntriesPassValidation(t *testing.T) {
	for name, upstream := range builtinCompat {
		if err := openaicompat.ValidateName(name); err != nil {
			t.Errorf("built-in compat %q: %v", name, err)
		}
		if err := openaicompat.ValidateBaseURL(upstream.BaseURL); err != nil {
			t.Errorf("built-in compat %q base_url: %v", name, err)
		}
		if strings.TrimSpace(upstream.APIKeyEnv) == "" {
			t.Errorf("built-in compat %q has no api_key_env", name)
		}
	}
}

// The tool-schema strip is DEFAULT OFF: it changes model-visible bytes, so only
// the explicit value "annotations" turns it on and every other spelling —
// including a bare config and an unrecognized value — normalizes to off.
func TestLoad_ToolSchemaStripDefaultsOffAndFailsClosed(t *testing.T) {
	t.Setenv("CAVEMAN_TOOLSCHEMA_STRIP", "")
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ToolSchemaStrip != "off" {
		t.Fatalf("bare config toolschema_strip = %q, want off", cfg.ToolSchemaStrip)
	}

	path := filepath.Join(t.TempDir(), "caveman.yaml")
	if err := os.WriteFile(path, []byte("toolschema_strip: aggressive\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ToolSchemaStrip != "off" {
		t.Fatalf("unknown toolschema_strip = %q, want fail-closed off", cfg.ToolSchemaStrip)
	}

	if err := os.WriteFile(path, []byte("toolschema_strip: annotations\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ToolSchemaStrip != "annotations" {
		t.Fatalf("toolschema_strip = %q, want annotations", cfg.ToolSchemaStrip)
	}

	t.Setenv("CAVEMAN_TOOLSCHEMA_STRIP", "off")
	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ToolSchemaStrip != "off" {
		t.Fatalf("env override toolschema_strip = %q, want off", cfg.ToolSchemaStrip)
	}
}

// Cache planning defaults on in optimization modes. Explicit off and unknown
// values remain fail-closed off-switches; record mode never runs planner.
func TestLoad_BreakpointPlanDefaultsFrontierAndFailsClosed(t *testing.T) {
	t.Setenv("CAVEMAN_BREAKPOINT_PLAN", "")
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.BreakpointPlan != "frontier" {
		t.Fatalf("bare config breakpoint_plan = %q, want frontier", cfg.BreakpointPlan)
	}

	path := filepath.Join(t.TempDir(), "caveman.yaml")
	for _, unknown := range []string{"on", "true", "Frontier", "aggressive"} {
		if err := os.WriteFile(path, []byte("breakpoint_plan: "+unknown+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err = Load(path)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.BreakpointPlan != "off" {
			t.Fatalf("breakpoint_plan %q = %q, want fail-closed off", unknown, cfg.BreakpointPlan)
		}
	}

	if err := os.WriteFile(path, []byte("breakpoint_plan: frontier\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.BreakpointPlan != "frontier" {
		t.Fatalf("breakpoint_plan = %q, want frontier", cfg.BreakpointPlan)
	}

	t.Setenv("CAVEMAN_BREAKPOINT_PLAN", "off")
	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.BreakpointPlan != "off" {
		t.Fatalf("env override breakpoint_plan = %q, want off", cfg.BreakpointPlan)
	}
}

func TestLoad_UpstreamProxy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caveman.yaml")
	if err := os.WriteFile(path, []byte("upstream_proxy: http://proxy.corp.example:3128\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	u, err := cfg.UpstreamProxyFunc()(&http.Request{URL: &url.URL{Scheme: "https", Host: "api.openai.com"}})
	if err != nil || u == nil || u.Host != "proxy.corp.example:3128" {
		t.Fatalf("proxy selector = %v, %v; want the yaml proxy", u, err)
	}

	t.Setenv("CAVE_UPSTREAM_PROXY", "off")
	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("Load with env override: %v", err)
	}
	if cfg.UpstreamProxy != "off" || cfg.UpstreamProxyFunc() != nil {
		t.Fatalf("env override not applied: %q", cfg.UpstreamProxy)
	}
	t.Setenv("CAVE_UPSTREAM_PROXY", "OFF")
	if cfg, err := Load(path); err != nil || cfg.UpstreamProxyFunc() != nil {
		t.Fatalf("keywords are case-insensitive: %q err=%v", cfg.UpstreamProxy, err)
	}

	// A pinned proxy keeps env-mode's direct exemptions: loopback (an allowlisted
	// local model server) and NO_PROXY are never handed to the corporate proxy.
	t.Setenv("CAVE_UPSTREAM_PROXY", "http://proxy.corp.example:3128")
	t.Setenv("NO_PROXY", "internal.example")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for host, wantDirect := range map[string]bool{"api.openai.com": false, "127.0.0.1:11434": true, "localhost:11434": true, "models.internal.example": true} {
		u, err := cfg.UpstreamProxyFunc()(&http.Request{URL: &url.URL{Scheme: "http", Host: host}})
		if err != nil || (u == nil) != wantDirect {
			t.Fatalf("%s: proxy=%v err=%v, want direct=%v", host, u, err, wantDirect)
		}
	}

	// Default: honour the environment like every other tool on the host (#1001).
	t.Setenv("CAVE_UPSTREAM_PROXY", "")
	if cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml")); err != nil || cfg.UpstreamProxyFunc() == nil {
		t.Fatalf("default must be the environment selector: cfg=%q err=%v", cfg.UpstreamProxy, err)
	}

	for _, bad := range []string{"ftp://proxy:21", "proxy.corp.example:3128", "not a url"} {
		t.Setenv("CAVE_UPSTREAM_PROXY", bad)
		if _, err := Load(path); err == nil {
			t.Fatalf("upstream_proxy %q must be rejected", bad)
		}
	}
}

func TestLoad_CABundle(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "corp-root.pem")
	if err := os.WriteFile(good, selfSignedPEM(t), 0o600); err != nil {
		t.Fatal(err)
	}
	garbage := filepath.Join(dir, "garbage.pem")
	if err := os.WriteFile(garbage, []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	absent := filepath.Join(dir, "absent.pem")
	yaml := filepath.Join(dir, "caveman.yaml")
	for _, name := range append([]string{"CAVE_CA_BUNDLE"}, inheritedCABundleEnv...) {
		t.Setenv(name, "")
	}

	if cfg, err := Load(yaml); err != nil || cfg.RootCAs() != nil {
		t.Fatalf("no bundle configured: roots=%v err=%v, want nil (Go default verification)", cfg.RootCAs(), err)
	}

	if err := os.WriteFile(yaml, []byte("ca_bundle: "+good+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(yaml)
	if err != nil || cfg.RootCAs() == nil {
		t.Fatalf("ca_bundle: roots=%v err=%v", cfg.RootCAs(), err)
	}

	for _, bad := range []string{absent, garbage} {
		t.Setenv("CAVE_CA_BUNDLE", bad)
		if _, err := Load(yaml); err == nil {
			t.Fatalf("ca_bundle %s must fail Load closed", bad)
		}
	}
	t.Setenv("CAVE_CA_BUNDLE", "")

	// Inherited toolchain variables: a missing, corrupt, or directory-valued file
	// is skipped and reported (never a startup failure, never partially trusted);
	// a good one is trusted additively.
	for _, bad := range []string{absent, garbage, dir} {
		t.Setenv("NODE_EXTRA_CA_CERTS", bad)
		cfg, err = Load(yaml)
		if err != nil || len(cfg.SkippedCABundles) != 1 || cfg.RootCAs() == nil {
			t.Fatalf("NODE_EXTRA_CA_CERTS=%s: skipped=%v roots=%v err=%v", bad, cfg.SkippedCABundles, cfg.RootCAs(), err)
		}
	}
	t.Setenv("NODE_EXTRA_CA_CERTS", "")
	t.Setenv("REQUESTS_CA_BUNDLE", good)
	if cfg, err := Load(filepath.Join(dir, "absent.yaml")); err != nil || cfg.RootCAs() == nil {
		t.Fatalf("REQUESTS_CA_BUNDLE alone: roots=%v err=%v", cfg.RootCAs(), err)
	}
}

func selfSignedPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "corp-root"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
