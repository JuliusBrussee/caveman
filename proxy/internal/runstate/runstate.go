// Package runstate owns the out-of-band state channel between a serving
// caveman-proxy process and the CLI. It contains no traffic or tenant data.
package runstate

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const Schema = "caveman.proxy.run.v1"

// InstanceHeader binds a health response to this process's private run-state
// file. Probes never send the expected token to the listener.
const InstanceHeader = "X-Caveman-Instance"

type State struct {
	Schema         string    `json:"schema"`
	PID            int       `json:"pid"`
	Port           int       `json:"port"`
	Listen         string    `json:"listen"`
	Mode           string    `json:"mode"`
	Owner          string    `json:"owner"`
	InstanceToken  string    `json:"instance_token"`
	StartedAt      time.Time `json:"started_at"`
	Version        string    `json:"version"`
	RecoveryViaMCP bool      `json:"recovery_via_mcp"`
	// CompatUpstreams maps each named OpenAI-compatible mount this proxy serves
	// at /compat/<name>/ to its upstream base URL. A client routes a custom
	// provider through a mount only after matching this map, so an absent field
	// (an older proxy) simply means "no custom mount is verifiable".
	CompatUpstreams map[string]string `json:"compat_upstreams,omitempty"`
	// ProviderUpstreams identifies the actual native endpoints of this listener.
	// Missing entries cannot certify a wrapper's original provider endpoint.
	ProviderUpstreams    map[string]string   `json:"provider_upstreams,omitempty"`
	CompatForwardHeaders map[string][]string `json:"compat_forward_headers,omitempty"`
}

type PublicState struct {
	Owner                string              `json:"owner"`
	Mode                 string              `json:"mode,omitempty"`
	InstanceToken        string              `json:"instance_token,omitempty"`
	PID                  int                 `json:"pid,omitempty"`
	Port                 int                 `json:"port,omitempty"`
	StartedAt            time.Time           `json:"started_at,omitempty"`
	Version              string              `json:"version,omitempty"`
	RecoveryViaMCP       bool                `json:"recovery_via_mcp"`
	CompatUpstreams      map[string]string   `json:"compat_upstreams,omitempty"`
	ProviderUpstreams    map[string]string   `json:"provider_upstreams,omitempty"`
	CompatForwardHeaders map[string][]string `json:"compat_forward_headers,omitempty"`
}

func Unknown() PublicState {
	return PublicState{Owner: "unknown"}
}

// RoutingUpstreams publishes only credential-free endpoint identities. Keep an
// unavailable entry as an empty string so it cannot become a same-named default
// route after the credential-bearing URL was suppressed.
func RoutingUpstreams(upstreams map[string]string) map[string]string {
	if upstreams == nil {
		return nil
	}
	out := make(map[string]string, len(upstreams))
	for name, raw := range upstreams {
		out[name] = ""
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			continue
		}
		out[name] = raw
	}
	return out
}

func PortFromListen(listen string) (int, error) {
	_, raw, err := net.SplitHostPort(listen)
	if err != nil {
		return 0, fmt.Errorf("invalid listen address %q: %w", listen, err)
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid listen port %q", raw)
	}
	return port, nil
}

func Path(home string, port int) string {
	return filepath.Join(home, "run", strconv.Itoa(port)+".json")
}

func New(listen, mode, owner, version string) (State, error) {
	port, err := PortFromListen(listen)
	if err != nil {
		return State{}, err
	}
	if owner != "wrap" && owner != "start" {
		owner = "start"
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return State{}, err
	}
	return State{
		Schema:        Schema,
		PID:           os.Getpid(),
		Port:          port,
		Listen:        listen,
		Mode:          mode,
		Owner:         owner,
		InstanceToken: hex.EncodeToString(token[:]),
		StartedAt:     time.Now().UTC(),
		Version:       version,
	}, nil
}

func Write(home string, state State) error {
	state.CompatUpstreams = RoutingUpstreams(state.CompatUpstreams)
	state.ProviderUpstreams = RoutingUpstreams(state.ProviderUpstreams)
	dir := filepath.Join(home, "run")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".runstate-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, Path(home, state.Port))
}

func read(home string, port int) (State, error) {
	raw, err := os.ReadFile(Path(home, port))
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return State{}, err
	}
	if state.Schema != Schema || state.Port != port || state.PID < 1 ||
		state.InstanceToken == "" || (state.Owner != "wrap" && state.Owner != "start") {
		return State{}, errors.New("invalid run-state contract")
	}
	listenPort, err := PortFromListen(state.Listen)
	if err != nil || listenPort != state.Port {
		return State{}, errors.New("run-state listen address does not match its port")
	}
	return state, nil
}

type validators struct {
	alive      func(int) bool
	executable func(int) (string, error)
	instance   func(string, string) bool
}

func validate(state State, checks validators) bool {
	if !checks.alive(state.PID) {
		return false
	}
	exe, err := checks.executable(state.PID)
	if err != nil || !strings.Contains(strings.ToLower(filepath.Base(exe)), "caveman-proxy") {
		return false
	}
	return checks.instance(state.Listen, state.InstanceToken)
}

func ReadValidated(home string, port int) PublicState {
	state, err := read(home, port)
	if err != nil {
		return Unknown()
	}
	checks := validators{alive: processAlive, executable: processExecutable, instance: instanceMatches}
	if !validate(state, checks) {
		return Unknown()
	}
	return PublicState{
		Owner:                state.Owner,
		Mode:                 state.Mode,
		InstanceToken:        state.InstanceToken,
		PID:                  state.PID,
		Port:                 state.Port,
		StartedAt:            state.StartedAt,
		Version:              state.Version,
		RecoveryViaMCP:       state.RecoveryViaMCP,
		CompatUpstreams:      RoutingUpstreams(state.CompatUpstreams),
		ProviderUpstreams:    RoutingUpstreams(state.ProviderUpstreams),
		CompatForwardHeaders: state.CompatForwardHeaders,
	}
}

func RemoveMatching(home string, port int, token string) error {
	state, err := read(home, port)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if state.InstanceToken != token {
		return nil
	}
	err = os.Remove(Path(home, port))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	defer func() { _ = process.Release() }()
	if runtime.GOOS == "windows" {
		// Signal(0) is not implemented on Windows. FindProcess opens a real
		// process handle there and fails for exited processes, so a
		// successful open is the liveness signal. Caveat: a terminated
		// process whose handle another process still holds also opens, so on
		// Windows validate()'s instance probe carries the real liveness
		// weight — a dead proxy cannot return its generation's token.
		return true
	}
	return process.Signal(syscall.Signal(0)) == nil
}

func processExecutable(pid int) (string, error) {
	if runtime.GOOS == "linux" {
		return os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
	}
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
		return strings.TrimSpace(string(out)), err
	}
	if runtime.GOOS == "windows" {
		return processExecutableWindows(pid)
	}
	return "", fmt.Errorf("executable identity unsupported on %s", runtime.GOOS)
}

func instanceMatches(listen, token string) bool {
	if token == "" {
		return false
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	// A wildcard bind is reached through loopback, never a corporate proxy or
	// an unspecified remote destination.
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	// The listen address comes out of a file. config.validateListen allows a
	// non-loopback bind only behind CAVEMAN_AUTH_TOKEN; this probe deliberately
	// holds a stricter rule and only ever dials loopback, rather than issuing a
	// request to whatever remote host that file happens to name. A local CLI
	// next to an explicitly non-loopback listener therefore runs direct.
	if addr, err := netip.ParseAddr(host); err != nil || !addr.IsLoopback() {
		return false
	}
	target := url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: "/health/live"}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            (&net.Dialer{Timeout: 250 * time.Millisecond}).DialContext,
		DisableKeepAlives:      true,
		MaxResponseHeaderBytes: 16 << 10,
		ResponseHeaderTimeout:  500 * time.Millisecond,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   750 * time.Millisecond,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Get(target.String())
	if err != nil {
		return false
	}
	defer response.Body.Close()
	values := response.Header.Values(InstanceHeader)
	return response.StatusCode == http.StatusOK && len(values) == 1 && values[0] == token
}
