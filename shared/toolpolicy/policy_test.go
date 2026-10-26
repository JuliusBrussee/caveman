package toolpolicy

import "testing"

func TestParseAndMatch(t *testing.T) {
	p, err := Parse(`["mcp__tokensave__*","tokensave_context"]`)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"mcp__tokensave__tokensave_search": true, "tokensave_context": true, "mcp__other__tokensave_search": false, "Tokensave_context": false, "Read": false} {
		if got := p.Matches(name); got != want {
			t.Fatalf("%s: %v", name, got)
		}
	}
	for _, raw := range []string{`null`, `"x"`, `[1]`, `[""]`, `["*"]`, `["bad**"]`, `["bad?name"]`, `[" spaced"]`, `[] trailing`} {
		if _, err := Parse(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}
