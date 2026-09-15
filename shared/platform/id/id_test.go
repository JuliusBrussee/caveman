package id

import (
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNewUUIDv7FormatVersionVariantAndTimestamp(t *testing.T) {
	before := time.Now().UnixMilli()
	value := NewUUIDv7()
	after := time.Now().UnixMilli()

	parts := strings.Split(value, "-")
	wantLengths := []int{8, 4, 4, 4, 12}
	if len(parts) != len(wantLengths) {
		t.Fatalf("UUID %q has %d parts, want 5", value, len(parts))
	}
	for i, want := range wantLengths {
		if len(parts[i]) != want {
			t.Fatalf("UUID part %d length = %d, want %d", i, len(parts[i]), want)
		}
		if _, err := hex.DecodeString(parts[i]); err != nil {
			t.Fatalf("UUID part %d is not hex: %v", i, err)
		}
	}
	if parts[2][0] != '7' {
		t.Fatalf("UUID version nibble = %q, want 7", parts[2][0])
	}
	if !strings.ContainsRune("89ab", rune(parts[3][0])) {
		t.Fatalf("UUID variant nibble = %q, want RFC 4122 variant", parts[3][0])
	}
	ms, err := strconv.ParseInt(parts[0]+parts[1], 16, 64)
	if err != nil {
		t.Fatal(err)
	}
	if ms < before || ms > after {
		t.Fatalf("UUID timestamp = %d, want within [%d, %d]", ms, before, after)
	}
}

func TestNewUUIDv7IsUnique(t *testing.T) {
	const count = 1000
	seen := make(map[string]struct{}, count)
	for range count {
		value := NewUUIDv7()
		if _, exists := seen[value]; exists {
			t.Fatalf("duplicate UUID: %s", value)
		}
		seen[value] = struct{}{}
	}
}

func TestTraceAndSpanIDs(t *testing.T) {
	tests := []struct {
		name   string
		length int
		newID  func() string
	}{
		{name: "span", length: 16, newID: NewSpanID},
		{name: "trace", length: 32, newID: NewTraceID},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first := test.newID()
			second := test.newID()
			if len(first) != test.length {
				t.Fatalf("length = %d, want %d", len(first), test.length)
			}
			if _, err := hex.DecodeString(first); err != nil {
				t.Fatalf("ID is not hex: %v", err)
			}
			if first == second {
				t.Fatalf("two generated IDs matched: %s", first)
			}
		})
	}
}
