// Package pool is the local runtime's provider logins and the routing pool
// built from them: the API keys and subscription logins a person added with
// `caveman providers add|login`, times the catalog models each host serves.
// The route stage sends that pool with its ask (contracts route-ask-v1
// `pool`); when Cloud picks one of its entries, Target says where the request
// goes and on which credential. Nothing here decides a model.
//
// Logins live in $CAVEMAN_HOME/provider-logins.json (ids and where each secret
// is stored, never a secret) and their secrets in the macOS keychain (service
// caveman-provider, account = provider id) or a 0600 file under
// $CAVEMAN_HOME/provider-logins/, the same split the CLI uses for its own
// Cloud login.
package pool

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
	"github.com/JuliusBrussee/caveman/proxy/internal/securehome"
	"github.com/JuliusBrussee/caveman/proxy/internal/translate"
)

//go:embed providers.json
var registryJSON []byte

// Provider is one host a login can reach.
type Provider struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Kind           string            `json:"kind"` // api_key | oauth
	Env            []string          `json:"env"`
	BaseURL        string            `json:"base_url"`
	Wires          map[string]Wire   `json:"wires"`
	Models         []Model           `json:"models"`
	Aggregator     bool              `json:"aggregator"` // serves several vendors' models
	MaxTokensField string            `json:"max_tokens_field"`
	DropParams     []string          `json:"drop_params"`
	Replay         bool              `json:"replay"`
	Headers        map[string]string `json:"headers"`
	ForwardHeaders []string          `json:"forward_headers"`
	Affinity       string            `json:"affinity"`
	Agents         []string          `json:"agents"` // when set, only these agents' requests may use it
	ChatGPTLogin   bool              `json:"chatgpt_login"`
	Billing        string            `json:"billing"`
	Terms          string            `json:"terms"`
}

// Wire is one grammar a host speaks: its path and how it takes a credential.
type Wire struct {
	Path   string `json:"path"`
	Auth   string `json:"auth"`   // x-api-key | bearer
	Effort string `json:"effort"` // chat effort dialect
}

// Model is a catalog model name, the host's own id for it ("" = the same) and
// the one wire it is served on ("" = any the host speaks).
// ponytail: static per-host model lists; read each host's /models (or a
// Cloud-published list) when they drift.
type Model struct {
	Model string `json:"model"`
	ID    string `json:"id"`
	Wire  string `json:"wire"`
}

var (
	registryOnce sync.Once
	registry     []Provider
)

// Providers is the embedded registry.
func Providers() []Provider {
	registryOnce.Do(func() {
		var doc struct {
			Providers []Provider `json:"providers"`
		}
		if err := json.Unmarshal(registryJSON, &doc); err != nil {
			panic("pool: providers.json: " + err.Error())
		}
		registry = doc.Providers
	})
	return registry
}

// Find is the registry row for id.
func Find(id string) (Provider, bool) {
	for _, provider := range Providers() {
		if provider.ID == id {
			return provider, true
		}
	}
	return Provider{}, false
}

// Login is one credential the person added; never the secret itself.
type Login struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`  // api_key | oauth
	Store   string `json:"store"` // keychain | file
	AddedAt string `json:"added_at,omitempty"`
}

const (
	indexFile       = "provider-logins.json"
	secretDir       = "provider-logins"
	keychainService = "caveman-provider"
	maxPool         = 64
)

// Store reads the logins and their secrets, re-reading the index only when it
// changed (one stat per use) and each secret once per index change.
type Store struct {
	home string
	// keychain and setKeychain reach the macOS keychain; tests replace them.
	keychain       func(account string) (string, error)
	setKeychain    func(account, secret string) error
	deleteKeychain func(account string) error
	now            func() time.Time
	client         *http.Client // token refreshes

	mu         sync.Mutex
	stamp      string
	logins     []Login
	secrets    map[string]string
	refreshing map[string]bool
	cloudOff   bool
}

// NewStore reads the logins under home ($CAVEMAN_HOME).
func NewStore(home string) *Store {
	return &Store{home: home, keychain: macKeychainGet, setKeychain: macKeychainSet, deleteKeychain: macKeychainDelete, now: time.Now,
		client: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// Logins are the logins of providers this runtime knows, in the order added.
func (s *Store) Logins() []Login {
	path := filepath.Join(s.home, indexFile)
	stamp := ""
	if info, err := os.Stat(path); err == nil {
		stamp = fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if stamp == s.stamp && s.secrets != nil {
		return s.logins
	}
	s.stamp, s.logins, s.secrets, s.cloudOff = stamp, nil, map[string]string{}, false
	raw, err := os.ReadFile(path)
	var doc struct {
		Logins []Login `json:"logins"`
		Cloud  *bool   `json:"cloud"`
	}
	if err != nil || json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	s.cloudOff = doc.Cloud != nil && !*doc.Cloud
	for _, login := range doc.Logins {
		if provider, ok := Find(login.ID); ok && provider.Kind == login.Kind && !slices.ContainsFunc(s.logins, func(l Login) bool { return l.ID == login.ID }) {
			s.logins = append(s.logins, login)
		}
	}
	return s.logins
}

// Secret is a login's stored secret, read once per index change.
func (s *Store) Secret(id string) (string, error) {
	var login *Login
	for _, l := range s.Logins() {
		if l.ID == id {
			login = &l
			break
		}
	}
	if login == nil {
		return "", errors.New("no such login")
	}
	s.mu.Lock()
	secret, cached := s.secrets[id]
	s.mu.Unlock()
	if cached {
		return secret, nil
	}
	secret, err := s.readSecret(*login)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	if s.secrets != nil {
		s.secrets[id] = secret
	}
	s.mu.Unlock()
	return secret, nil
}

// readSecret reads a login's secret from its store, uncached.
func (s *Store) readSecret(login Login) (string, error) {
	var secret string
	var err error
	switch login.Store {
	case "keychain":
		secret, err = s.keychain(login.ID)
	default:
		var raw []byte
		raw, err = os.ReadFile(filepath.Join(s.home, secretDir, login.ID))
		secret = string(raw)
	}
	secret = strings.TrimSpace(secret)
	if err != nil || secret == "" {
		return "", errors.New("login secret unreadable")
	}
	return secret, nil
}

// Save stores a login's secret and lists it in the index (the keychain on
// macOS unless CAVE_NO_KEYCHAIN is set, else a 0600 file).
func (s *Store) Save(id, kind, secret string) error {
	return s.saveIf(id, kind, secret, nil)
}

// saveIf is Save under the index lock. When keep is set, the save happens
// only while the login is still listed and keep accepts its current secret
// (a background refresh must not resurrect a removed login or overwrite a
// newer sign-in).
func (s *Store) saveIf(id, kind, secret string, keep func(current string) bool) error {
	if err := os.MkdirAll(s.home, 0o700); err != nil {
		return err
	}
	// Best effort: a volume without ACLs (FAT) cannot be made private, and
	// refusing there would only break sign-in.
	_ = securehome.Restrict(s.home)
	unlock, err := lockIndex(s.home)
	if err != nil {
		return err
	}
	defer unlock()
	path := filepath.Join(s.home, indexFile)
	var doc map[string]any
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &doc)
	}
	if doc == nil {
		doc = map[string]any{"version": 1}
	}
	logins, _ := doc["logins"].([]any)
	var previous map[string]any
	kept := []any{}
	for _, item := range logins {
		if entry, ok := item.(map[string]any); ok && entry["id"] == id {
			previous = entry
			continue
		}
		kept = append(kept, item)
	}
	if keep != nil {
		if previous == nil {
			return errors.New("login removed")
		}
		store, _ := previous["store"].(string)
		current, err := s.readSecret(Login{ID: id, Store: store})
		if err != nil || !keep(current) {
			return errors.New("login changed")
		}
	}
	store := "file"
	if runtime.GOOS == "darwin" && os.Getenv("CAVE_NO_KEYCHAIN") == "" && s.setKeychain(id, secret) == nil {
		store = "keychain"
		_ = os.Remove(filepath.Join(s.home, secretDir, id)) // a secret never lives in both stores
	} else {
		dir := filepath.Join(s.home, secretDir)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := writeAtomic(filepath.Join(dir, id), []byte(secret), 0o600); err != nil {
			return err
		}
		if previous != nil && previous["store"] == "keychain" && s.deleteKeychain != nil {
			_ = s.deleteKeychain(id)
		}
	}
	added := s.now().UTC().Format(time.RFC3339)
	if at, ok := previous["added_at"].(string); ok && at != "" {
		added = at
	}
	doc["logins"] = append(kept, map[string]any{"id": id, "kind": kind, "store": store, "added_at": added})
	raw, _ := json.MarshalIndent(doc, "", "  ")
	if err := writeAtomic(path, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	s.mu.Lock()
	s.stamp = "" // re-read on next use
	s.mu.Unlock()
	return nil
}

// CloudOff reports `caveman providers cloud off`: an answer sending a
// request through Caveman Cloud runs the asked model instead.
func (s *Store) CloudOff() bool {
	s.Logins()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cloudOff
}

// lockIndex takes $CAVEMAN_HOME/provider-logins.json.lock, the lock the CLI
// takes too, for one read-modify-write of the index. The lock file holds a
// token of its own: unlock removes it only while it still holds that token.
// A lock older than 10 s is a crashed writer's: it is renamed aside first, so
// of two waiters only one breaks it, and a fresh lock that slipped in between
// is put back.
func lockIndex(home string) (func(), error) {
	path := filepath.Join(home, indexFile+".lock")
	token := randomToken()
	deadline := time.Now().Add(3 * time.Second)
	for {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, werr := file.WriteString(token)
			file.Close()
			if werr != nil {
				_ = os.Remove(path)
				return nil, werr
			}
			return func() {
				if held, err := os.ReadFile(path); err == nil && string(held) == token {
					_ = os.Remove(path)
				}
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > lockStale {
			aside := path + ".break." + token
			if os.Rename(path, aside) == nil {
				if info, err := os.Stat(aside); err == nil && time.Since(info.ModTime()) <= lockStale {
					_ = os.Link(aside, path) // a fresh lock: back where it was, unless another took the place
				}
				_ = os.Remove(aside)
			}
			continue
		}
		if time.Now().After(deadline) {
			return nil, errIndexLocked
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// lockStale is when a lock counts as a crashed writer's.
const lockStale = 10 * time.Second

var errIndexLocked = errors.New("provider-logins.json is locked")

// Entry is one pool entry: a catalog model on one login (contracts
// route-ask-v1 pool[]). The unexported fields say how to reach it.
type Entry struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	Host  string `json:"host"`
	Via   string `json:"via"`

	wire   string
	wireID string
}

// wirePreference is the order a caller's grammar tries a host's wires in:
// its own first, then the ones a translator serves it from.
var wirePreference = map[string][]string{
	translate.Messages:  {translate.Messages, translate.Chat, translate.Responses},
	translate.Responses: {translate.Responses, translate.Messages, translate.Chat},
	translate.Chat:      {translate.Chat, translate.Messages, translate.Responses},
}

// Entries is the pool the logins reach for a caller speaking grammar, for
// agent (the x-cave-agent slug): every login's models on a wire this runtime
// can translate to, skipping what skip reports (the harness's own entries).
// At most limit entries.
func (s *Store) Entries(grammar, agent string, limit int, skip func(host, model string) bool) []Entry {
	var out []Entry
	for _, login := range s.Logins() {
		provider, _ := Find(login.ID)
		if len(provider.Agents) > 0 && !slices.Contains(provider.Agents, agent) {
			continue
		}
		for _, model := range provider.Models {
			if len(out) >= min(limit, maxPool) {
				return out
			}
			if skip != nil && skip(provider.ID, model.Model) {
				continue
			}
			wire := pickWire(provider, model, grammar)
			if wire == "" {
				continue
			}
			id := model.ID
			if id == "" {
				id = model.Model
			}
			out = append(out, Entry{ID: provider.ID + "/" + model.Model, Model: model.Model, Host: provider.ID, Via: "local", wire: wire, wireID: id})
		}
	}
	return out
}

func pickWire(provider Provider, model Model, grammar string) string {
	for _, wire := range wirePreference[grammar] {
		if _, speaks := provider.Wires[wire]; speaks && (model.Wire == "" || model.Wire == wire) && translate.Supported(grammar, wire) {
			return wire
		}
	}
	return ""
}

// Target is where entry's requests go: the URL, the credential and the
// translator options for its host. An OAuth login that is expired or needs a
// new sign-in is an error; one about to expire is refreshed in the
// background and used while it still holds.
func (s *Store) Target(entry Entry) (gateway.RouteTarget, error) {
	provider, ok := Find(entry.Host)
	wire, speaks := provider.Wires[entry.wire]
	if !ok || !speaks {
		return gateway.RouteTarget{}, errors.New("unknown pool entry")
	}
	secret, err := s.Secret(provider.ID)
	if err != nil {
		return gateway.RouteTarget{}, err
	}
	if provider.Kind == "oauth" {
		if secret, err = s.oauthAccess(provider.ID, secret); err != nil {
			return gateway.RouteTarget{}, err
		}
	}
	header := http.Header{}
	switch wire.Auth {
	case "x-api-key":
		header.Set("x-api-key", secret)
	default:
		header.Set("authorization", "Bearer "+secret)
	}
	for name, value := range provider.Headers {
		header.Set(name, value)
	}
	route := provider.ID
	if provider.Aggregator {
		route += "/" + entry.Model
	}
	dialect := wire.Effort
	if entry.wire == translate.Chat && dialect == "" {
		dialect = "openai_chat"
	}
	return gateway.RouteTarget{
		PoolID: entry.ID, Via: "local", Host: provider.ID, Model: entry.Model,
		Wire: entry.wire, URL: strings.TrimSuffix(provider.BaseURL, "/") + wire.Path, Header: header,
		Forward: provider.ForwardHeaders, Affinity: provider.Affinity,
		Translate: translate.Options{
			Model: entry.wireID, Dialect: dialect, Route: route, Replay: provider.Replay,
			MaxTokensField: provider.MaxTokensField, DropParams: provider.DropParams, ChatGPTLogin: provider.ChatGPTLogin,
		},
	}, nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func macKeychainGet(account string) (string, error) {
	if runtime.GOOS != "darwin" || os.Getenv("CAVE_NO_KEYCHAIN") != "" {
		return "", errors.New("no keychain")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "security", "find-generic-password", "-s", keychainService, "-a", account, "-w").Output()
	return string(out), err
}

// macKeychainDelete removes a login's keychain copy. It runs even under
// CAVE_NO_KEYCHAIN (which only stops new keychain writes): an older copy the
// index names must not outlive a move to the file store. Deleting never
// prompts.
func macKeychainDelete(account string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "security", "delete-generic-password", "-s", keychainService, "-a", account).Run()
}

// macKeychainSet goes through `security -i` so the secret travels on stdin,
// never argv (which any local process can read from the process table).
func macKeychainSet(account, secret string) error {
	if runtime.GOOS != "darwin" || strings.ContainsFunc(account+secret, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return errors.New("no keychain") // `security -i` reads one line: no control characters
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "security", "-i")
	command.Stdin = strings.NewReader(fmt.Sprintf("add-generic-password -U -s %s -a %s -w %s\n", keychainService, securityQuote(account), securityQuote(secret)))
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return err
	}
	// `security -i` exits 0 even when the command inside it failed.
	if stored, err := macKeychainGet(account); err != nil || strings.TrimSpace(stored) != secret {
		return errors.New("keychain write did not land")
	}
	return nil
}

// securityQuote quotes one argument for `security -i`'s line parser.
func securityQuote(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}
