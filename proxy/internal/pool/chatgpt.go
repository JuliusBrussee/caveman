package pool

// Sign in with ChatGPT (EXPERIMENTAL): the runtime's own login on the flow
// OpenAI published for open-source and locally hosted apps on 2026-09-29
// (developers.openai.com/siwc). It never reads ~/.codex/auth.json or any other
// app's credentials, so it never races Codex's own refresh. The token record
// is the "chatgpt" login's secret; the runtime is its only refresher.
//
// NOT VERIFIED LIVE: tested against a fake authorization server only. The
// refresh grant's exact form, the issued client id's field name and whether
// the preview takes top-level function tools (they move to additional_tools,
// translate.Options.ChatGPTLogin) are unconfirmed against the real service.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

const (
	chatgptLogin        = "chatgpt"
	chatgptIssuer       = "https://auth.openai.com"
	chatgptResource     = "https://api.openai.com/v1"
	chatgptScope        = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
	chatgptDirectScope  = "chatgpt.tokens.use.direct"
	chatgptDynamicID    = "dynamic_agent_client"
	chatgptRefreshAhead = 5 * time.Minute
)

// chatgptToken is the stored login. It is never logged or printed.
type chatgptToken struct {
	AccessToken       string    `json:"access_token"`
	RefreshToken      string    `json:"refresh_token"`
	ExpiresAt         time.Time `json:"expires_at"`
	EarliestRefreshAt time.Time `json:"earliest_refresh_at,omitempty"`
	ClientID          string    `json:"client_id"`
	Issuer            string    `json:"issuer"`
	// NeedsLogin: the refresh token was refused; the login leaves the pool
	// until `caveman providers login chatgpt` runs again.
	NeedsLogin bool `json:"needs_login,omitempty"`
}

// LoginOptions: Issuer and Client are for tests; Open opens the browser
// (nil: the OS opener).
type LoginOptions struct {
	Out     io.Writer
	Issuer  string
	Client  *http.Client
	Open    func(string) error
	Timeout time.Duration
}

// LoginChatGPT runs the authorization-code flow with PKCE on a loopback
// callback and stores the token record as the "chatgpt" login.
func (s *Store) LoginChatGPT(ctx context.Context, options LoginOptions) error {
	if options.Issuer == "" {
		options.Issuer = chatgptIssuer
	}
	if options.Client == nil {
		options.Client = s.client
	}
	if options.Timeout == 0 {
		options.Timeout = 5 * time.Minute
	}
	if options.Open == nil {
		options.Open = openBrowser
	}
	if options.Out == nil {
		options.Out = io.Discard
	}
	hostID, err := s.hostID()
	if err != nil {
		return err
	}
	clientID := chatgptDynamicID
	if raw, err := s.Secret(chatgptLogin); err == nil {
		var stored chatgptToken
		if json.Unmarshal([]byte(raw), &stored) == nil && stored.ClientID != "" && stored.Issuer == options.Issuer {
			clientID = stored.ClientID // the id issued at the first login
		}
	}
	verifier, state, nonce := randomToken(), randomToken(), randomToken()
	challenge := sha256.Sum256([]byte(verifier))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", listener.Addr().(*net.TCPAddr).Port)
	codes, failures := make(chan string, 1), make(chan error, 1)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		switch {
		case r.URL.Path != "/callback":
			http.NotFound(w, r)
			return
		case query.Get("state") != state:
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		case query.Get("error") != "":
			select {
			case failures <- fmt.Errorf("authorization refused: %s %s", query.Get("error"), query.Get("error_description")):
			default:
			}
		default:
			select {
			case codes <- query.Get("code"):
			default:
			}
		}
		_, _ = io.WriteString(w, "caveman: you can close this tab.\n")
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	authorize := options.Issuer + "/api/accounts/authorize?" + url.Values{
		"client_id": {clientID}, "agent_name_hint": {"Caveman"}, "ext_agent_host_id": {hostID},
		"response_type": {"code"}, "redirect_uri": {redirect}, "scope": {chatgptScope}, "resource": {chatgptResource},
		"state": {state}, "nonce": {nonce}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
	}.Encode()
	fmt.Fprintln(options.Out, "Sign in with ChatGPT (experimental). Opening:\n  "+authorize)
	if err := options.Open(authorize); err != nil {
		fmt.Fprintln(options.Out, "Open that link in a browser on this machine.")
	}
	wait, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	var code string
	select {
	case code = <-codes:
	case err := <-failures:
		return err
	case <-wait.Done():
		return errors.New("no answer from the browser in time")
	}
	token, err := chatgptTokenRequest(wait, options.Client, options.Issuer, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code},
		"code_verifier": {verifier}, "redirect_uri": {redirect}, "resource": {chatgptResource},
	}, nonce, s.now())
	if err != nil {
		return err
	}
	if token.ClientID == "" {
		token.ClientID = clientID
	}
	token.Issuer = options.Issuer
	raw, _ := json.Marshal(token)
	return s.Save(chatgptLogin, "oauth", string(raw))
}

// oauthAccess is the access token of an OAuth login's record: an error when
// it needs a new sign-in or has expired; a refresh starts in the background
// when it is about to.
func (s *Store) oauthAccess(id, raw string) (string, error) {
	var token chatgptToken
	if id != chatgptLogin || json.Unmarshal([]byte(raw), &token) != nil || token.AccessToken == "" {
		return "", errors.New("unreadable login")
	}
	now := s.now()
	if token.NeedsLogin {
		return "", errors.New("login needs a new sign-in: caveman providers login chatgpt")
	}
	if token.ExpiresAt.Sub(now) <= chatgptRefreshAhead && token.RefreshToken != "" && !now.Before(token.EarliestRefreshAt) {
		s.mu.Lock()
		busy := s.refreshing[id]
		if !busy {
			if s.refreshing == nil {
				s.refreshing = map[string]bool{}
			}
			s.refreshing[id] = true
		}
		s.mu.Unlock()
		if !busy {
			go s.refreshChatGPT(token)
		}
	}
	if !now.Before(token.ExpiresAt) {
		return "", errors.New("login expired")
	}
	return token.AccessToken, nil
}

// refreshChatGPT trades the refresh token for a new record. A refused refresh
// marks the record needs_login; any other failure keeps it for the next try.
func (s *Store) refreshChatGPT(token chatgptToken) {
	defer func() {
		s.mu.Lock()
		delete(s.refreshing, chatgptLogin)
		s.mu.Unlock()
	}()
	issuer := token.Issuer
	if issuer == "" {
		issuer = chatgptIssuer
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fresh, err := chatgptTokenRequest(ctx, s.client, issuer, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {token.RefreshToken}, "client_id": {token.ClientID}, "resource": {chatgptResource},
	}, "", s.now())
	switch {
	case errors.Is(err, errChatGPTRelogin):
		token.NeedsLogin = true
	case err != nil:
		return
	default:
		if fresh.ClientID == "" {
			fresh.ClientID = token.ClientID
		}
		fresh.Issuer = issuer
		token = fresh
	}
	raw, _ := json.Marshal(token)
	_ = s.Save(chatgptLogin, "oauth", string(raw))
}

type chatgptTokenAnswer struct {
	AccessToken       string `json:"access_token"`
	RefreshToken      string `json:"refresh_token"`
	IDToken           string `json:"id_token"`
	ExpiresIn         int    `json:"expires_in"`
	Scope             string `json:"scope"`
	ClientID          string `json:"client_id"`
	EarliestRefreshAt int64  `json:"earliest_refresh_at"`
	Error             string `json:"error"`
	ErrorDescription  string `json:"error_description"`
}

// errChatGPTRelogin: the refresh token was refused (invalid_grant,
// refresh_token_reused); only a new login fixes it.
var errChatGPTRelogin = errors.New("chatgpt login: refresh refused, run caveman providers login chatgpt again")

// chatgptTokenRequest posts one grant. A login (nonce set) checks the
// id_token's claims: it came straight from the token endpoint over TLS, which
// OIDC Core 3.1.3.7 accepts in place of the signature check.
func chatgptTokenRequest(ctx context.Context, client *http.Client, issuer string, form url.Values, nonce string, now time.Time) (chatgptToken, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, issuer+"/api/accounts/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return chatgptToken{}, err
	}
	request.Header.Set("content-type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		return chatgptToken{}, err
	}
	defer response.Body.Close()
	var answer chatgptTokenAnswer
	_ = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&answer)
	if answer.Error == "invalid_grant" || answer.Error == "refresh_token_reused" || strings.Contains(answer.ErrorDescription, "refresh_token_reused") {
		return chatgptToken{}, errChatGPTRelogin
	}
	if response.StatusCode != http.StatusOK || answer.AccessToken == "" {
		return chatgptToken{}, fmt.Errorf("chatgpt token endpoint: %s %s", response.Status, answer.Error)
	}
	if !slices.Contains(strings.Fields(answer.Scope), chatgptDirectScope) {
		return chatgptToken{}, fmt.Errorf("chatgpt login: the grant lacks %s (scope %q)", chatgptDirectScope, answer.Scope)
	}
	if nonce != "" {
		if err := checkIDToken(answer.IDToken, issuer, answer.ClientID, form.Get("client_id"), nonce, now); err != nil {
			return chatgptToken{}, err
		}
	}
	token := chatgptToken{AccessToken: answer.AccessToken, RefreshToken: answer.RefreshToken, ClientID: answer.ClientID,
		ExpiresAt: now.Add(time.Duration(max(answer.ExpiresIn, 60)) * time.Second)}
	if answer.EarliestRefreshAt > 0 {
		token.EarliestRefreshAt = time.Unix(answer.EarliestRefreshAt, 0)
	}
	if token.RefreshToken == "" {
		token.RefreshToken = form.Get("refresh_token") // a refresh may keep the old one
	}
	return token, nil
}

// checkIDToken checks the id_token's issuer, audience (the issued or the
// requested client id), nonce and expiry.
func checkIDToken(raw, issuer, issuedID, requestedID, nonce string, now time.Time) error {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return errors.New("chatgpt login: no id_token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Issuer   string          `json:"iss"`
		Audience json.RawMessage `json:"aud"`
		Nonce    string          `json:"nonce"`
		Expires  int64           `json:"exp"`
	}
	if err != nil || json.Unmarshal(payload, &claims) != nil {
		return errors.New("chatgpt login: unreadable id_token")
	}
	var audiences []string
	if json.Unmarshal(claims.Audience, &audiences) != nil {
		var one string
		_ = json.Unmarshal(claims.Audience, &one)
		audiences = []string{one}
	}
	switch {
	case strings.TrimSuffix(claims.Issuer, "/") != strings.TrimSuffix(issuer, "/"):
		return fmt.Errorf("chatgpt login: id_token issuer %q", claims.Issuer)
	case !(issuedID != "" && slices.Contains(audiences, issuedID)) && !slices.Contains(audiences, requestedID):
		return errors.New("chatgpt login: id_token is for another client")
	case claims.Nonce != nonce:
		return errors.New("chatgpt login: id_token nonce mismatch")
	case claims.Expires > 0 && now.Unix() > claims.Expires:
		return errors.New("chatgpt login: id_token expired")
	}
	return nil
}

// hostID is ext_agent_host_id: a random urn:uuid made once per install (not
// a secret).
func (s *Store) hostID() (string, error) {
	path := filepath.Join(s.home, "chatgpt-host-id")
	if raw, err := os.ReadFile(path); err == nil && strings.HasPrefix(strings.TrimSpace(string(raw)), "urn:uuid:") {
		return strings.TrimSpace(string(raw)), nil
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	id[6], id[8] = id[6]&0x0f|0x40, id[8]&0x3f|0x80 // RFC 9562 version 4
	h := hex.EncodeToString(id)
	value := "urn:uuid:" + h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
	if err := os.MkdirAll(s.home, 0o700); err != nil {
		return "", err
	}
	return value, writeAtomic(path, []byte(value+"\n"), 0o600)
}

func randomToken() string {
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func openBrowser(target string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	return exec.Command(name, target).Start()
}
