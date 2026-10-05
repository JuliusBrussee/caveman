package openai

import (
	"net/http"
	"testing"
)

func TestMatchRoute_ModelDiscovery(t *testing.T) {
	adapter := New("https://upstream.test")
	tests := []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodGet, "/v1/models", true},
		{http.MethodGet, "/v1/models/gpt-5.5", true},
		{http.MethodGet, "/openai/v1/models", true},
		{http.MethodGet, "/openai/v1/models/gpt-5.5", true},
		{http.MethodPost, "/v1/models", false},
		{http.MethodGet, "/v1/models/", false},
		{http.MethodGet, "/v1/models/gpt-5.5/metadata", false},
		{http.MethodGet, "/v1/models-extra", false},
		{http.MethodGet, "/v1/chat/completions", false},
		{http.MethodPost, "/v1/chat/completions", true},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			if got := adapter.MatchRoute(tt.method, tt.path); got != tt.want {
				t.Fatalf("MatchRoute(%q, %q) = %v, want %v", tt.method, tt.path, got, tt.want)
			}
		})
	}
}
