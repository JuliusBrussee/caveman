//go:build !js

package ccr

import (
	"errors"
	"testing"
	"time"
)

func TestRetryOnBusyCapsTotalBackoff(t *testing.T) {
	busy := errors.New("SQLITE_BUSY")
	now := time.Unix(0, 0)
	var slept time.Duration
	calls := 0
	err := retryOnBusy(
		func() error {
			calls++
			return busy
		},
		100*time.Millisecond,
		func() time.Time { return now },
		func(delay time.Duration) {
			slept += delay
			now = now.Add(delay)
		},
	)
	if !errors.Is(err, busy) {
		t.Fatalf("err=%v want SQLITE_BUSY", err)
	}
	if slept != 100*time.Millisecond {
		t.Fatalf("slept=%s want capped 100ms", slept)
	}
	if calls != 3 {
		t.Fatalf("calls=%d want 3 attempts before deadline", calls)
	}
}

func TestRetryOnBusyReturnsNonBusyErrorWithoutSleeping(t *testing.T) {
	want := errors.New("migration failed")
	slept := false
	err := retryOnBusy(
		func() error { return want },
		time.Second,
		time.Now,
		func(time.Duration) { slept = true },
	)
	if !errors.Is(err, want) {
		t.Fatalf("err=%v want %v", err, want)
	}
	if slept {
		t.Fatal("non-busy error must not sleep")
	}
}
