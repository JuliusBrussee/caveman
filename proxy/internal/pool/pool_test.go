package pool

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// home writes a fake credential store: an index and 0600 secret files.
func home(t *testing.T, secrets map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, secretDir), 0o700); err != nil {
		t.Fatal(err)
	}
	var logins []Login
	for _, id := range []string{"anthropic", "openai", "chatgpt", "deepseek", "zai-coding-plan", "openrouter", "unknown-host"} {
		secret, ok := secrets[id]
		if !ok {
			continue
		}
		kind := "api_key"
		if id == "chatgpt" {
			kind = "oauth"
		}
		logins = append(logins, Login{ID: id, Kind: kind, Store: "file"})
		if err := os.WriteFile(filepath.Join(dir, secretDir, id), []byte(secret), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(map[string]any{"version": 1, "logins": logins})
	if err := os.WriteFile(filepath.Join(dir, indexFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func ids(entries []Entry) []string {
	var out []string
	for _, entry := range entries {
		out = append(out, entry.ID+"@"+entry.wire)
	}
	return out
}

func chatgptRecord(expires time.Time, needsLogin bool) string {
	raw, _ := json.Marshal(chatgptToken{AccessToken: "chatgpt-access", RefreshToken: "chatgpt-refresh", ExpiresAt: expires, ClientID: "c1", NeedsLogin: needsLogin})
	return string(raw)
}

func TestEntriesFollowWhatThePersonSetUpAndWhatTheRuntimeCanTranslate(t *testing.T) {
	store := NewStore(home(t, map[string]string{
		"openai": "sk-openai", "chatgpt": chatgptRecord(time.Now().Add(time.Hour), false), "deepseek": "sk-ds",
		"zai-coding-plan": "zai", "unknown-host": "x",
	}))
	// A Claude Code request (Messages): OpenAI on its chat wire, DeepSeek on its
	// Messages wire, no ChatGPT login (Responses only, which Messages cannot reach yet).
	got := strings.Join(ids(store.Entries("messages", "claude", 64, nil)), " ")
	want := "openai/gpt-6.1-sol@chat openai/gpt-6-sol@chat openai/gpt-6-astra@chat openai/gpt-6-luna@chat deepseek/deepseek-v4-pro@messages deepseek/deepseek-v4-flash@messages zai-coding-plan/glm-5.3@messages"
	if got != want {
		t.Fatalf("messages pool =\n%s\nwant\n%s", got, want)
	}
	// A Codex request (Responses) reaches the ChatGPT login; the coding plan is
	// kept to the agents its terms list.
	got = strings.Join(ids(store.Entries("responses", "aider", 64, nil)), " ")
	if !strings.Contains(got, "chatgpt/gpt-6.1-sol@responses") || !strings.Contains(got, "openai/gpt-6-sol@responses") || strings.Contains(got, "zai-coding-plan") {
		t.Fatalf("responses pool = %s", got)
	}
	// A chat caller reaches chat wires only; skip drops the harness's own entries; limit bounds it.
	got = strings.Join(ids(store.Entries("chat", "opencode", 3, func(host, model string) bool { return host == "openai" && model == "gpt-6.1-sol" })), " ")
	if got != "openai/gpt-6-sol@chat openai/gpt-6-astra@chat openai/gpt-6-luna@chat" {
		t.Fatalf("chat pool = %s", got)
	}
	if entries := NewStore(t.TempDir()).Entries("messages", "claude", 64, nil); len(entries) != 0 {
		t.Fatalf("no logins, pool = %v", ids(entries))
	}
}

func TestTargetCarriesTheLoginsCredentialAndTheHostsWire(t *testing.T) {
	store := NewStore(home(t, map[string]string{"openrouter": "sk-or", "deepseek": "sk-ds", "anthropic": "sk-ant"}))
	entries := store.Entries("messages", "claude", 64, nil)
	find := func(id string) Entry {
		for _, entry := range entries {
			if entry.ID == id {
				return entry
			}
		}
		t.Fatalf("no entry %s in %v", id, ids(entries))
		return Entry{}
	}
	target, err := store.Target(find("openrouter/kimi-k3"))
	if err != nil {
		t.Fatal(err)
	}
	if target.URL != "https://openrouter.ai/api/v1/chat/completions" || target.Header.Get("authorization") != "Bearer sk-or" ||
		target.Translate.Model != "moonshotai/kimi-k3" || target.Translate.Route != "openrouter/kimi-k3" || target.Translate.Dialect != "openrouter" ||
		target.Header.Get("X-Title") != "caveman" || target.Affinity != "x-session-id" || target.Model != "kimi-k3" || target.Via != "local" {
		t.Fatalf("openrouter target = %+v", target)
	}
	target, _ = store.Target(find("anthropic/claude-sonnet-5-5"))
	if target.URL != "https://api.anthropic.com/v1/messages" || target.Header.Get("x-api-key") != "sk-ant" || target.Header.Get("authorization") != "" || target.Wire != "messages" {
		t.Fatalf("anthropic target = %+v", target)
	}
	target, _ = store.Target(find("deepseek/deepseek-v4-flash"))
	if target.Translate.Route != "deepseek" || !target.Translate.Replay {
		t.Fatalf("deepseek target = %+v", target)
	}
	if err := os.Remove(filepath.Join(store.home, secretDir, "deepseek")); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(store.home).Target(find("deepseek/deepseek-v4-flash")); err == nil {
		t.Fatal("a missing secret must not make a target")
	}
}

func TestChatGPTLoginExpiryAndRefresh(t *testing.T) {
	var form string
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		form = string(raw)
		_, _ = io.WriteString(w, `{"access_token":"fresh-access","refresh_token":"fresh-refresh","expires_in":3600,"scope":"openid chatgpt.tokens.use.direct"}`)
	}))
	defer issuer.Close()
	now := time.Now()
	dir := home(t, map[string]string{"chatgpt": chatgptRecord(now.Add(time.Minute), false)})
	store := NewStore(dir)
	store.setKeychain = func(string, string) error { return os.ErrPermission } // the file store, on any OS
	// Point the stored record at the fake issuer.
	var record chatgptToken
	_ = json.Unmarshal([]byte(chatgptRecord(now.Add(time.Minute), false)), &record)
	record.Issuer = issuer.URL
	raw, _ := json.Marshal(record)
	_ = os.WriteFile(filepath.Join(dir, secretDir, "chatgpt"), raw, 0o600)

	entries := store.Entries("responses", "codex", 64, nil)
	target, err := store.Target(entries[0])
	if err != nil || target.Header.Get("authorization") != "Bearer chatgpt-access" || !target.Translate.ChatGPTLogin {
		t.Fatalf("target = %+v, %v", target, err)
	}
	// About to expire: a background refresh saved a new record.
	deadline := time.Now().Add(5 * time.Second)
	for {
		secret, _ := os.ReadFile(filepath.Join(dir, secretDir, "chatgpt"))
		if strings.Contains(string(secret), "fresh-access") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no refresh; secret = %s", secret)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(form, "grant_type=refresh_token") || !strings.Contains(form, "refresh_token=chatgpt-refresh") {
		t.Fatalf("refresh form = %s", form)
	}
	for name, record := range map[string]string{"expired": chatgptRecord(now.Add(-time.Minute), false), "needs login": chatgptRecord(now.Add(time.Hour), true)} {
		dir := home(t, map[string]string{"chatgpt": record})
		store := NewStore(dir)
		store.client = &http.Client{Transport: offline{}} // a refresh never leaves the test
		store.setKeychain = func(string, string) error { return os.ErrPermission }
		if _, err := store.Target(store.Entries("responses", "codex", 64, nil)[0]); err == nil {
			t.Errorf("%s: an unusable login made a target", name)
		}
	}
}

func TestSaveWritesTheIndexAndAFileSecret(t *testing.T) {
	store := NewStore(t.TempDir())
	store.setKeychain = func(string, string) error { return os.ErrPermission }
	t.Setenv("CAVE_NO_KEYCHAIN", "1")
	if err := store.Save("chatgpt", "oauth", "secret-1"); err != nil {
		t.Fatal(err)
	}
	if logins := store.Logins(); len(logins) != 1 || logins[0].ID != "chatgpt" || logins[0].Store != "file" {
		t.Fatalf("logins = %+v", logins)
	}
	info, err := os.Stat(filepath.Join(store.home, secretDir, "chatgpt"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("secret file = %v, %v", info, err)
	}
	index, _ := os.ReadFile(filepath.Join(store.home, indexFile))
	if strings.Contains(string(index), "secret-1") {
		t.Fatal("the index holds a secret")
	}
	if secret, _ := store.Secret("chatgpt"); secret != "secret-1" {
		t.Fatalf("secret = %q", secret)
	}
}

func TestRegistryOAuthRowsCarryTerms(t *testing.T) {
	for _, provider := range Providers() {
		if provider.Kind != "api_key" && provider.Kind != "oauth" {
			t.Errorf("%s: kind %q", provider.ID, provider.Kind)
		}
		if provider.Kind == "oauth" && provider.Terms == "" {
			t.Errorf("%s: an OAuth login without a terms note", provider.ID)
		}
		if strings.Contains(strings.ToLower(provider.ID), "google") || strings.Contains(provider.BaseURL, "accounts.google") {
			t.Errorf("%s: no Google login may be added", provider.ID)
		}
		if len(provider.Models) == 0 || len(provider.Wires) == 0 {
			t.Errorf("%s: no models or wires", provider.ID)
		}
	}
}

type offline struct{}

func (offline) RoundTrip(*http.Request) (*http.Response, error) { return nil, os.ErrDeadlineExceeded }
