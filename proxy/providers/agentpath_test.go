package providers

import (
	"strings"
	"testing"
)

func TestSplitAgentPath(t *testing.T) {
	longSlug := "a" + strings.Repeat("b", 64)
	cases := []struct {
		name     string
		path     string
		wantSlug string
		wantRest string
		wantOK   bool
	}{
		{
			name:     "valid prefixed openai route",
			path:     "/w/checkout-bot/openai/v1/chat/completions",
			wantSlug: "checkout-bot",
			wantRest: "/openai/v1/chat/completions",
			wantOK:   true,
		},
		{
			name:     "dots underscores dashes ok",
			path:     "/w/app.v2_worker-prod/v1/messages",
			wantSlug: "app.v2_worker-prod",
			wantRest: "/v1/messages",
			wantOK:   true,
		},
		{
			name:     "strip once",
			path:     "/w/openai/openai/v1/chat/completions",
			wantSlug: "openai",
			wantRest: "/openai/v1/chat/completions",
			wantOK:   true,
		},
		{name: "uppercase reject", path: "/w/Bad/v1/messages"},
		{name: "too long reject", path: "/w/" + longSlug + "/v1/messages"},
		{name: "leading dash reject", path: "/w/-bad/v1/messages"},
		{name: "empty rest reject", path: "/w/app"},
		{name: "slash rest reject", path: "/w/app/"},
		{name: "empty slug reject", path: "/w//v1/messages"},
		{name: "non w path passthrough", path: "/openai/v1/chat/completions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotSlug, gotRest, gotOK := SplitAgentPath(tc.path)
			if gotOK != tc.wantOK || gotSlug != tc.wantSlug || gotRest != tc.wantRest {
				t.Fatalf("SplitAgentPath(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.path, gotSlug, gotRest, gotOK, tc.wantSlug, tc.wantRest, tc.wantOK)
			}
		})
	}
}
