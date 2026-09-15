package providerorigin

import "testing"

func TestTrustedCanonicalOrigins(t *testing.T) {
	tests := []struct {
		provider string
		url      string
		trusted  bool
	}{
		{"openai", "https://api.openai.com", true},
		{"openai", "https://evil.example", false},
		{"anthropic", "https://api.anthropic.com/v1", true},
		{"gemini", "https://generativelanguage.googleapis.com", true},
		{"azure_openai", "https://resource.openai.azure.com", true},
		{"bedrock", "https://bedrock-runtime.us-east-1.amazonaws.com", true},
		{"vertex", "https://us-central1-aiplatform.googleapis.com", true},
		{"openai_compatible", "https://api.openai.com", false},
		{"openai", "http://api.openai.com", false},
		{"openai", "https://api.openai.com?token=secret", false},
	}
	for _, test := range tests {
		t.Run(test.provider+"/"+test.url, func(t *testing.T) {
			if got := Trusted(test.provider, test.url); got != test.trusted {
				t.Fatalf("Trusted(%q, %q) = %t, want %t", test.provider, test.url, got, test.trusted)
			}
		})
	}
}
