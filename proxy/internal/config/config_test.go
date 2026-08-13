package config

import (
	"os"
	"path/filepath"
	"testing"
)

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

// The breakpoint planner is DEFAULT OFF for the same reason: it ships behind the
// escalation ladder, so only the explicit value "frontier" turns it on and every
// other spelling normalizes to off.
func TestLoad_BreakpointPlanDefaultsOffAndFailsClosed(t *testing.T) {
	t.Setenv("CAVEMAN_BREAKPOINT_PLAN", "")
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.BreakpointPlan != "off" {
		t.Fatalf("bare config breakpoint_plan = %q, want off", cfg.BreakpointPlan)
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
