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
}

type PublicState struct {
	Owner          string    `json:"owner"`
	Mode           string    `json:"mode,omitempty"`
	InstanceToken  string    `json:"instance_token,omitempty"`
	PID            int       `json:"pid,omitempty"`
	Port           int       `json:"port,omitempty"`
	StartedAt      time.Time `json:"started_at,omitempty"`
	Version        string    `json:"version,omitempty"`
	RecoveryViaMCP bool      `json:"recovery_via_mcp"`
}

func Unknown() PublicState {
	return PublicState{Owner: "unknown"}
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
	return state, nil
}

type validators struct {
	alive      func(int) bool
	executable func(int) (string, error)
	bound      func(string) bool
}

func validate(state State, checks validators) bool {
	if !checks.alive(state.PID) {
		return false
	}
	exe, err := checks.executable(state.PID)
	if err != nil || !strings.Contains(strings.ToLower(filepath.Base(exe)), "caveman-proxy") {
		return false
	}
	return checks.bound(state.Listen)
}

func ReadValidated(home string, port int) PublicState {
	state, err := read(home, port)
	if err != nil {
		return Unknown()
	}
	checks := validators{alive: processAlive, executable: processExecutable, bound: portBound}
	if !validate(state, checks) {
		return Unknown()
	}
	return PublicState{
		Owner:          state.Owner,
		Mode:           state.Mode,
		InstanceToken:  state.InstanceToken,
		PID:            state.PID,
		Port:           state.Port,
		StartedAt:      state.StartedAt,
		Version:        state.Version,
		RecoveryViaMCP: state.RecoveryViaMCP,
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
		// Windows validate()'s portBound probe carries the real liveness
		// weight — a dead proxy is not listening.
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

func portBound(listen string) bool {
	conn, err := net.DialTimeout("tcp", listen, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
