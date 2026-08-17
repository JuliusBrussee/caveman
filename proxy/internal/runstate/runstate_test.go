package runstate

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPortFromListenAcceptsIPv4AndIPv6AndRejectsInvalidPorts(t *testing.T) {
	for listen, want := range map[string]int{
		"127.0.0.1:8787":  8787,
		"[::1]:443":       443,
		"localhost:1":     1,
		"localhost:65535": 65535,
	} {
		got, err := PortFromListen(listen)
		if err != nil || got != want {
			t.Fatalf("PortFromListen(%q) = %d, %v; want %d", listen, got, err, want)
		}
	}
	for _, listen := range []string{"localhost", "localhost:0", "localhost:65536", "localhost:http", ":0"} {
		if _, err := PortFromListen(listen); err == nil {
			t.Fatalf("invalid listen address %q accepted", listen)
		}
	}
}

func TestWriteAndRemoveMatching(t *testing.T) {
	home := t.TempDir()
	state, err := New("127.0.0.1:18787", "compress", "wrap", "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(home, state); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(Path(home, state.Port))
	if err != nil {
		t.Fatal(err)
	}
	// POSIX permission bits are synthetic on Windows; NTFS ACLs govern there.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %o, want 600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(Path(home, state.Port)))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %o, want 700", dirInfo.Mode().Perm())
	}
	if err := RemoveMatching(home, state.Port, "successor-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Path(home, state.Port)); err != nil {
		t.Fatalf("mismatched token removed successor state: %v", err)
	}
	if err := RemoveMatching(home, state.Port, state.InstanceToken); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Path(home, state.Port)); !os.IsNotExist(err) {
		t.Fatalf("matching state still exists: %v", err)
	}
	if err := RemoveMatching(home, state.Port, state.InstanceToken); err != nil {
		t.Fatalf("missing state removal must be idempotent: %v", err)
	}
}

func TestWriteIsAtomicAndReadableContract(t *testing.T) {
	home := t.TempDir()
	state, err := New("127.0.0.1:18788", "record", "start", "dev")
	if err != nil {
		t.Fatal(err)
	}
	state.RecoveryViaMCP = true
	if err := Write(home, state); err != nil {
		t.Fatal(err)
	}
	got, err := read(home, state.Port)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != state {
		t.Fatalf("read state = %+v, want %+v", got, state)
	}
	raw, err := os.ReadFile(Path(home, state.Port))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		t.Fatalf("state file lacks trailing newline: %q", raw)
	}
	matches, err := filepath.Glob(filepath.Join(home, "run", ".runstate-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary state files leaked: %v, %v", matches, err)
	}
}

func TestValidateChecksIdentityAndBoundPort(t *testing.T) {
	state := State{PID: 42, Listen: "127.0.0.1:8787"}
	ok := validators{
		alive:      func(int) bool { return true },
		executable: func(int) (string, error) { return "/tmp/caveman-proxy", nil },
		bound:      func(string) bool { return true },
	}
	if !validate(state, ok) {
		t.Fatal("valid state rejected")
	}
	wrongExe := ok
	wrongExe.executable = func(int) (string, error) { return "/tmp/node", nil }
	if validate(state, wrongExe) {
		t.Fatal("foreign executable accepted")
	}
	exeError := ok
	exeError.executable = func(int) (string, error) { return "", errors.New("identity lookup failed") }
	if validate(state, exeError) {
		t.Fatal("executable lookup failure accepted")
	}
	dead := ok
	dead.alive = func(int) bool { return false }
	if validate(state, dead) {
		t.Fatal("dead pid accepted")
	}
	unbound := ok
	unbound.bound = func(string) bool { return false }
	if validate(state, unbound) {
		t.Fatal("unbound port accepted")
	}
}

func TestReadRejectsEveryInvalidContractField(t *testing.T) {
	valid := `{"schema":"caveman.proxy.run.v1","pid":1,"port":8787,"listen":"127.0.0.1:8787","mode":"record","owner":"start","instance_token":"token"}`
	tests := map[string]string{
		"malformed JSON": `{`,
		"wrong schema":   strings.Replace(valid, Schema, "future", 1),
		"wrong port":     strings.Replace(valid, `"port":8787`, `"port":8788`, 1),
		"zero pid":       strings.Replace(valid, `"pid":1`, `"pid":0`, 1),
		"missing token":  strings.Replace(valid, `"instance_token":"token"`, `"instance_token":""`, 1),
		"unknown owner":  strings.Replace(valid, `"owner":"start"`, `"owner":"other"`, 1),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.MkdirAll(filepath.Join(home, "run"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(Path(home, 8787), []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := read(home, 8787); err == nil {
				t.Fatal("invalid run-state contract accepted")
			}
			if err := RemoveMatching(home, 8787, "token"); err == nil {
				t.Fatal("RemoveMatching accepted invalid contract")
			}
		})
	}
}

func TestUnknownSchemaFailsClosed(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "run")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(home, 8787), []byte(`{"schema":"future","pid":1,"port":8787}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := ReadValidated(home, 8787)
	if got.Owner != "unknown" || got.Mode != "" {
		t.Fatalf("got %#v, want owner unknown and no mode", got)
	}
}

func TestNewUsesUTCAnd128BitToken(t *testing.T) {
	state, err := New("127.0.0.1:8787", "record", "invalid", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if state.Owner != "start" {
		t.Fatalf("owner = %q", state.Owner)
	}
	if len(state.InstanceToken) != 32 {
		t.Fatalf("token chars = %d, want 32", len(state.InstanceToken))
	}
	if state.StartedAt.Location() != time.UTC {
		t.Fatalf("started_at location = %v", state.StartedAt.Location())
	}
	if _, err := New("invalid", "record", "start", "dev"); err == nil {
		t.Fatal("New accepted invalid listen address")
	}
}

func TestProcessAndPortProbes(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Fatal("current process reported dead")
	}
	if processAlive(1 << 30) {
		t.Fatal("impossible process reported alive")
	}
	executable, err := processExecutable(os.Getpid())
	if err != nil || strings.TrimSpace(executable) == "" {
		t.Fatalf("current executable = %q, %v", executable, err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if !portBound(address) {
		t.Fatalf("live listener %s reported unbound", address)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if portBound(address) {
		t.Fatalf("closed listener %s reported bound", address)
	}
}
