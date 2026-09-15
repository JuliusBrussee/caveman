package env

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/JuliusBrussee/caveman/shared/platform/kms"
	"github.com/JuliusBrussee/caveman/shared/platform/runtimeenv"
)

func String(name, fallback string) string {
	if v, ok := os.LookupEnv(name); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

func Bool(name string, fallback bool) bool {
	v, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(v) == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return fallback
	}
	return parsed
}

func Int(name string, fallback int) int {
	v, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(v) == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return fallback
	}
	return parsed
}

func Required(name string) (string, error) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return v, nil
}

// IsProduction is the single definition of "this process runs in production".
// Every refusal gate must agree on it: a CAVE_ENV of " prod" or "PROD" that one
// check treated as production and another as local would arm half the production
// refusals and silently skip the rest. Whitespace and case are therefore folded
// in — the direction that turns MORE deployments on, never fewer.
func IsProduction() bool {
	return runtimeenv.IsProduction()
}

func RefuseProductionDefaults() error {
	if !IsProduction() {
		return nil
	}
	if err := validateProductionTextSecrets([]string{
		"CAVE_KEY_HASH_PEPPER",
		"CAVE_JWT_SIGNING_KEY",
		"CAVE_BOOTSTRAP_TOKEN",
	}); err != nil {
		return err
	}
	if err := kms.ValidateProduction(); err != nil {
		return fmt.Errorf("production KMS configuration: %w", err)
	}
	return validateProductionPublicURL()
}

// RefuseGatewayProductionDefaults validates only material the public data plane
// consumes. Control-plane JWT/bootstrap secrets must never be injected into the
// gateway merely to satisfy a shared configuration check.
func RefuseGatewayProductionDefaults() error {
	if !IsProduction() {
		return nil
	}
	if err := validateProductionTextSecrets([]string{"CAVE_KEY_HASH_PEPPER"}); err != nil {
		return err
	}
	if Bool("CAVE_REPLAY_ENABLED", false) {
		if err := validateProductionTextSecrets([]string{"CAVE_ROUTER_REPLAY_TOKEN"}); err != nil {
			return err
		}
	}
	return validateProductionPublicURL()
}

func validateProductionTextSecrets(names []string) error {
	for _, name := range names {
		v := strings.TrimSpace(os.Getenv(name))
		if v == "" || v == "generated" || strings.Contains(strings.ToLower(v), "changeme") {
			return fmt.Errorf("production refuses default or empty %s", name)
		}
		if len(v) < 32 {
			return fmt.Errorf("production requires %s to contain at least 32 characters", name)
		}
		if distinctBytes([]byte(v)) < 8 {
			return fmt.Errorf("production refuses low-diversity %s", name)
		}
	}
	return nil
}

func validateProductionPublicURL() error {
	publicURL := strings.TrimSpace(os.Getenv("CAVE_PUBLIC_URL"))
	u, err := url.Parse(publicURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("production requires CAVE_PUBLIC_URL to be an HTTPS origin without credentials, path, query, or fragment")
	}
	return nil
}

func distinctBytes(value []byte) int {
	seen := map[byte]struct{}{}
	for _, b := range value {
		seen[b] = struct{}{}
	}
	return len(seen)
}
