package env_test

import (
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/shared/platform/env"
)

func TestPrimitiveEnvironmentReaders(t *testing.T) {
	t.Setenv("CAVE_TEST_STRING", " explicit value ")
	if got := env.String("CAVE_TEST_STRING", "fallback"); got != " explicit value " {
		t.Fatalf("String explicit = %q", got)
	}
	t.Setenv("CAVE_TEST_STRING", "   ")
	if got := env.String("CAVE_TEST_STRING", "fallback"); got != "fallback" {
		t.Fatalf("String blank = %q", got)
	}

	for value, want := range map[string]bool{
		"true":   true,
		"FALSE":  false,
		" true ": true,
	} {
		t.Setenv("CAVE_TEST_BOOL", value)
		if got := env.Bool("CAVE_TEST_BOOL", !want); got != want {
			t.Errorf("Bool(%q) = %v, want %v", value, got, want)
		}
	}
	t.Setenv("CAVE_TEST_BOOL", "invalid")
	if got := env.Bool("CAVE_TEST_BOOL", true); !got {
		t.Fatal("invalid Bool did not use fallback")
	}
	t.Setenv("CAVE_TEST_BOOL", "")
	if got := env.Bool("CAVE_TEST_BOOL", true); !got {
		t.Fatal("empty Bool did not use fallback")
	}

	for value, want := range map[string]int{
		"42":   42,
		" -7 ": -7,
	} {
		t.Setenv("CAVE_TEST_INT", value)
		if got := env.Int("CAVE_TEST_INT", 99); got != want {
			t.Errorf("Int(%q) = %d, want %d", value, got, want)
		}
	}
	t.Setenv("CAVE_TEST_INT", "invalid")
	if got := env.Int("CAVE_TEST_INT", 99); got != 99 {
		t.Fatalf("invalid Int = %d", got)
	}

	t.Setenv("CAVE_TEST_REQUIRED", " secret ")
	if got, err := env.Required("CAVE_TEST_REQUIRED"); err != nil || got != "secret" {
		t.Fatalf("Required explicit = %q, %v", got, err)
	}
	t.Setenv("CAVE_TEST_REQUIRED", " ")
	if _, err := env.Required("CAVE_TEST_REQUIRED"); err == nil {
		t.Fatal("blank Required value accepted")
	}
}

func setValidProductionEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CAVE_ENV", "prod")
	t.Setenv("CAVE_KEY_HASH_PEPPER", "pepper-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	t.Setenv("CAVE_JWT_SIGNING_KEY", "jwt-key-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	t.Setenv("CAVE_BOOTSTRAP_TOKEN", "bootstrap-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	t.Setenv("CAVE_PUBLIC_URL", "https://app.caveman.example")
	t.Setenv("CAVE_KMS_PROVIDER", "scaleway")
	t.Setenv("CAVE_KMS_REGION", "fr-par")
	t.Setenv("CAVE_KMS_AUTH_TOKEN", "kms-token-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	t.Setenv("SCW_SECRET_KEY", "")
	t.Setenv("KMS_SECRETS_KEY_ARN", "00000000-0000-0000-0000-000000000001")
}

func TestRefuseProductionDefaultsAcceptsStrongConfiguration(t *testing.T) {
	setValidProductionEnv(t)
	if err := env.RefuseProductionDefaults(); err != nil {
		t.Fatalf("valid production configuration rejected: %v", err)
	}
}

func TestRefuseProductionDefaultsRejectsWeakSecretsAndOrigins(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{name: "short pepper", key: "CAVE_KEY_HASH_PEPPER", value: "short"},
		{name: "short JWT key", key: "CAVE_JWT_SIGNING_KEY", value: "short"},
		{name: "short bootstrap token", key: "CAVE_BOOTSTRAP_TOKEN", value: "short"},
		{name: "low diversity JWT key", key: "CAVE_JWT_SIGNING_KEY", value: strings.Repeat("x", 48)},
		{name: "missing KMS provider", key: "CAVE_KMS_PROVIDER", value: ""},
		{name: "invalid KMS region", key: "CAVE_KMS_REGION", value: "../metadata"},
		{name: "missing KMS token", key: "CAVE_KMS_AUTH_TOKEN", value: ""},
		{name: "empty public origin", key: "CAVE_PUBLIC_URL", value: ""},
		{name: "non TLS public origin", key: "CAVE_PUBLIC_URL", value: "http://app.caveman.example"},
		{name: "public origin with credentials", key: "CAVE_PUBLIC_URL", value: "https://user:pass@app.caveman.example"},
		{name: "public origin with path", key: "CAVE_PUBLIC_URL", value: "https://app.caveman.example/dashboard"},
		{name: "missing KMS key", key: "KMS_SECRETS_KEY_ARN", value: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setValidProductionEnv(t)
			t.Setenv(tc.key, tc.value)
			if err := env.RefuseProductionDefaults(); err == nil {
				t.Fatal("unsafe production configuration accepted")
			}
		})
	}
}

func TestRefuseProductionDefaultsDoesNotConstrainLocalMode(t *testing.T) {
	t.Setenv("CAVE_ENV", "local")
	t.Setenv("CAVE_JWT_SIGNING_KEY", "dev")
	if err := env.RefuseProductionDefaults(); err != nil {
		t.Fatalf("local mode should remain developer-friendly: %v", err)
	}
}

func TestRefuseGatewayProductionDefaultsDoesNotRequireControlPlaneSecrets(t *testing.T) {
	t.Setenv("CAVE_ENV", "prod")
	t.Setenv("CAVE_KEY_HASH_PEPPER", "gateway-pepper-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	t.Setenv("CAVE_JWT_SIGNING_KEY", "")
	t.Setenv("CAVE_BOOTSTRAP_TOKEN", "")
	t.Setenv("CAVE_PUBLIC_URL", "https://app.caveman.example")
	if err := env.RefuseGatewayProductionDefaults(); err != nil {
		t.Fatalf("gateway validation required unused control-plane secrets: %v", err)
	}
}

func TestRefuseGatewayProductionDefaultsRequiresReplayTokenOnlyWhenEnabled(t *testing.T) {
	t.Setenv("CAVE_ENV", "prod")
	t.Setenv("CAVE_KEY_HASH_PEPPER", "gateway-pepper-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	t.Setenv("CAVE_PUBLIC_URL", "https://app.caveman.example")
	t.Setenv("CAVE_REPLAY_ENABLED", "true")
	t.Setenv("CAVE_ROUTER_REPLAY_TOKEN", "")
	if err := env.RefuseGatewayProductionDefaults(); err == nil || !strings.Contains(err.Error(), "CAVE_ROUTER_REPLAY_TOKEN") {
		t.Fatalf("missing replay token error = %v", err)
	}
	t.Setenv("CAVE_ROUTER_REPLAY_TOKEN", "router-replay-token-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	if err := env.RefuseGatewayProductionDefaults(); err != nil {
		t.Fatalf("configured replay token: %v", err)
	}
}
