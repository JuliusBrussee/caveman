package pool

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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
	// A Claude Code request (Messages): OpenAI and the ChatGPT login on
	// Responses (their only wire: GPT tool calls are not served on chat),
	// DeepSeek on its Messages wire.
	got := strings.Join(ids(store.Entries("messages", "claude", 64, nil)), " ")
	want := "openai/gpt-6.1-sol@responses openai/gpt-6-sol@responses openai/gpt-6-astra@responses openai/gpt-6-luna@responses " +
		"chatgpt/gpt-6.1-sol@responses chatgpt/gpt-6-sol@responses chatgpt/gpt-6-astra@responses chatgpt/gpt-6-luna@responses " +
		"deepseek/deepseek-v4-pro@messages deepseek/deepseek-v4-flash@messages zai-coding-plan/glm-5.3@messages"
	if got != want {
		t.Fatalf("messages pool =\n%s\nwant\n%s", got, want)
	}
	// A Codex request (Responses) reaches the ChatGPT login; the coding plan is
	// kept to the agents its terms list.
	got = strings.Join(ids(store.Entries("responses", "aider", 64, nil)), " ")
	if !strings.Contains(got, "chatgpt/gpt-6.1-sol@responses") || !strings.Contains(got, "openai/gpt-6-sol@responses") || strings.Contains(got, "zai-coding-plan") {
		t.Fatalf("responses pool = %s", got)
	}
	// A chat caller (OpenCode, Aider) reaches OpenAI and the ChatGPT login on
	// Responses and DeepSeek on its own chat wire; skip drops the harness's
	// own entries; limit bounds it.
	got = strings.Join(ids(store.Entries("chat", "opencode", 64, func(host, model string) bool { return host == "deepseek" && model == "deepseek-v4-pro" })), " ")
	want = "openai/gpt-6.1-sol@responses openai/gpt-6-sol@responses openai/gpt-6-astra@responses openai/gpt-6-luna@responses " +
		"chatgpt/gpt-6.1-sol@responses chatgpt/gpt-6-sol@responses chatgpt/gpt-6-astra@responses chatgpt/gpt-6-luna@responses " +
		"deepseek/deepseek-v4-flash@chat zai-coding-plan/glm-5.3@chat"
	if got != want {
		t.Fatalf("chat pool =\n%s\nwant\n%s", got, want)
	}
	if got = strings.Join(ids(store.Entries("chat", "opencode", 1, nil)), " "); got != "openai/gpt-6.1-sol@responses" {
		t.Fatalf("chat pool at limit 1 = %s", got)
	}
	// and Claude on Anthropic's Messages wire.
	claude := NewStore(home(t, map[string]string{"anthropic": "sk-ant"}))
	if got = strings.Join(ids(claude.Entries("chat", "opencode", 64, nil)), " "); got != "anthropic/claude-opus-5-5@messages anthropic/claude-sonnet-5-5@messages" {
		t.Fatalf("chat pool with an Anthropic key = %s", got)
	}
	for _, grammar := range []string{"messages", "responses", "chat"} {
		for _, entry := range store.Entries(grammar, "claude", 64, nil) {
			if (entry.Host == "openai" || entry.Host == "chatgpt" || strings.HasPrefix(entry.Model, "gpt-")) && entry.wire == "chat" {
				t.Errorf("%s caller: OpenAI model on the chat wire: %s", grammar, entry.ID)
			}
		}
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
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
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

// A refresh that lands after the login was removed, or after a new sign-in,
// saves nothing.
func TestRefreshNeverResurrectsARemovedOrNewerLogin(t *testing.T) {
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"access_token":"late-access","refresh_token":"late-refresh","expires_in":3600,"scope":"chatgpt.tokens.use.direct"}`)
	}))
	defer issuer.Close()
	for name, meanwhile := range map[string]func(dir string){
		"removed": func(dir string) {
			_ = os.WriteFile(filepath.Join(dir, indexFile), []byte(`{"version":1,"logins":[]}`), 0o600)
		},
		"signed in again": func(dir string) {
			_ = os.WriteFile(filepath.Join(dir, secretDir, "chatgpt"), []byte(`{"access_token":"new","refresh_token":"new-refresh","expires_at":"2099-01-01T00:00:00Z"}`), 0o600)
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := home(t, map[string]string{"chatgpt": chatgptRecord(time.Now().Add(time.Minute), false)})
			store := NewStore(dir)
			store.setKeychain = func(string, string) error { return os.ErrPermission }
			var token chatgptToken
			raw, _ := os.ReadFile(filepath.Join(dir, secretDir, "chatgpt"))
			_ = json.Unmarshal(raw, &token)
			token.Issuer = issuer.URL
			meanwhile(dir)
			store.refreshChatGPT(token)
			index, _ := os.ReadFile(filepath.Join(dir, indexFile))
			secret, _ := os.ReadFile(filepath.Join(dir, secretDir, "chatgpt"))
			if strings.Contains(string(secret), "late-access") || name == "removed" && strings.Contains(string(index), "chatgpt") {
				t.Fatalf("index %s secret %s", index, secret)
			}
		})
	}
}

func TestSwitchingStoresLeavesOneSecret(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the keychain store exists on macOS only")
	}
	t.Setenv("CAVE_NO_KEYCHAIN", "")
	store := NewStore(t.TempDir())
	deleted := ""
	store.deleteKeychain = func(account string) error { deleted = account; return nil }
	store.setKeychain = func(string, string) error { return nil }
	if err := store.Save("openai", "api_key", "sk-1"); err != nil || store.Logins()[0].Store != "keychain" {
		t.Fatalf("keychain save: %v %+v", err, store.Logins())
	}
	store.setKeychain = func(string, string) error { return os.ErrPermission }
	if err := store.Save("openai", "api_key", "sk-2"); err != nil || store.Logins()[0].Store != "file" || deleted != "openai" {
		t.Fatalf("file save: %v %+v deleted=%q", err, store.Logins(), deleted)
	}
	store.setKeychain = func(string, string) error { return nil }
	if err := store.Save("openai", "api_key", "sk-3"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.home, secretDir, "openai")); !os.IsNotExist(err) {
		t.Fatal("the file secret outlived the move to the keychain")
	}
}

func TestIndexLockIsSharedAndStaleLocksBreak(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockIndex(dir)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		release, err := lockIndex(dir)
		if err == nil {
			release()
		}
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("a second writer got the lock while it was held")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(dir, indexFile+".lock")
	_ = os.WriteFile(lock, nil, 0o600)
	_ = os.Chtimes(lock, time.Now().Add(-time.Minute), time.Now().Add(-time.Minute))
	release, err := lockIndex(dir)
	if err != nil {
		t.Fatalf("a stale lock was not broken: %v", err)
	}
	release()
}

func TestCloudOffIsReadFromTheIndex(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if store.CloudOff() {
		t.Fatal("cloud is on by default")
	}
	_ = os.WriteFile(filepath.Join(dir, indexFile), []byte(`{"version":1,"cloud":false,"logins":[]}`), 0o600)
	if !store.CloudOff() {
		t.Fatal("cloud off not read")
	}
}

// A holder only ever removes its own lock: after a stale break handed the
// lock to another writer, the first holder's unlock leaves it alone.
func TestUnlockRemovesOnlyItsOwnLock(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockIndex(dir)
	if err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(dir, indexFile+".lock")
	_ = os.WriteFile(lock, []byte("another-writer"), 0o600) // as if broken and retaken
	unlock()
	if held, _ := os.ReadFile(lock); string(held) != "another-writer" {
		t.Fatalf("the first holder removed another writer's lock: %q", held)
	}
}

func TestMovingToTheFileStoreDropsTheKeychainCopyEvenWithoutKeychainWrites(t *testing.T) {
	t.Setenv("CAVE_NO_KEYCHAIN", "1")
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, indexFile), []byte(`{"version":1,"logins":[{"id":"openai","kind":"api_key","store":"keychain"}]}`), 0o600)
	store := NewStore(dir)
	deleted := ""
	store.deleteKeychain = func(account string) error { deleted = account; return nil }
	if err := store.Save("openai", "api_key", "sk-new"); err != nil {
		t.Fatal(err)
	}
	if deleted != "openai" || store.Logins()[0].Store != "file" {
		t.Fatalf("deleted %q, logins %+v", deleted, store.Logins())
	}
}

// A refresh whose save meets a held lock tries once more, so a single-use
// refresh token is not lost to contention.
func TestRefreshRetriesASaveThatMetTheLock(t *testing.T) {
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"access_token":"rotated","refresh_token":"rotated-refresh","expires_in":3600,"scope":"chatgpt.tokens.use.direct"}`)
	}))
	defer issuer.Close()
	dir := home(t, map[string]string{"chatgpt": chatgptRecord(time.Now().Add(time.Minute), false)})
	store := NewStore(dir)
	store.setKeychain = func(string, string) error { return os.ErrPermission }
	var token chatgptToken
	raw, _ := os.ReadFile(filepath.Join(dir, secretDir, "chatgpt"))
	_ = json.Unmarshal(raw, &token)
	token.Issuer = issuer.URL
	lock := filepath.Join(dir, indexFile+".lock")
	_ = os.WriteFile(lock, []byte("busy"), 0o600)
	go func() { time.Sleep(3500 * time.Millisecond); _ = os.Remove(lock) }()
	store.refreshChatGPT(token)
	if secret, _ := os.ReadFile(filepath.Join(dir, secretDir, "chatgpt")); !strings.Contains(string(secret), "rotated-refresh") {
		t.Fatalf("the rotated token was lost: %s", secret)
	}
}
