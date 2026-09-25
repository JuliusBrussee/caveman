package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// proxy.log used to rotate only at startup, so a long-lived proxy writing a
// middleware audit line per request grew it without bound.
func TestProxyLogRotatesWhileServing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.log")
	log := openProxyLog(path, 100)
	if log == nil {
		t.Fatal("proxy log did not open")
	}
	defer log.Close()
	line := strings.Repeat("x", 39) + "\n"
	for i := 0; i < 6; i++ {
		if n, err := log.Write([]byte(line)); err != nil || n != len(line) {
			t.Fatal(n, err)
		}
	}
	current, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := os.Stat(path + ".1")
	if err != nil {
		t.Fatal("no rotated generation:", err)
	}
	if current.Size() > 100 || previous.Size() > 100 || current.Size()+previous.Size() != 80+80 {
		t.Fatalf("rotation sizes current=%d previous=%d", current.Size(), previous.Size())
	}
}
