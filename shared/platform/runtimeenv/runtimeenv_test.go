package runtimeenv

import "testing"

func TestIsProductionValueFoldsCaseAndWhitespace(t *testing.T) {
	for _, raw := range []string{"prod", " PROD", "prod ", "\tPrOd\n"} {
		if !IsProductionValue(raw) {
			t.Fatalf("IsProductionValue(%q) = false, want true", raw)
		}
	}
	for _, raw := range []string{"", "local", "production", "prod-ish"} {
		if IsProductionValue(raw) {
			t.Fatalf("IsProductionValue(%q) = true, want false", raw)
		}
	}
}
