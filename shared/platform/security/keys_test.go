package security

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestValidatePasswordBounds(t *testing.T) {
	if err := ValidatePassword(strings.Repeat("x", MinPasswordBytes)); err != nil {
		t.Fatalf("minimum password rejected: %v", err)
	}
	if err := ValidatePassword(strings.Repeat("x", MinPasswordBytes-1)); err == nil {
		t.Fatal("short password accepted")
	}
	if err := ValidatePassword(strings.Repeat("x", MaxPasswordBytes+1)); err == nil {
		t.Fatal("oversized password accepted")
	}
	if err := ValidatePassword(strings.Repeat("x", MaxPasswordBytes)); err != nil {
		t.Fatalf("maximum password rejected: %v", err)
	}
}

func TestCheckPasswordMalformedHashStillFails(t *testing.T) {
	malformed := []string{
		"",
		"bcrypt$not-argon",
		"argon2id$v=19$m=65536,t=3,p=4$not-base64$also-not-base64",
		"argon2id$v=19$m=65536,t=3,p=4$" + base64.RawStdEncoding.EncodeToString([]byte("salt")) + "$",
		"argon2id$v=19$m=65536,t=3,p=4$" + base64.RawStdEncoding.EncodeToString([]byte("salt")) + "$" +
			base64.RawStdEncoding.EncodeToString(make([]byte, 65)),
	}
	for _, encoded := range malformed {
		if CheckPassword(encoded, "attacker-password") {
			t.Fatalf("dummy password check accepted malformed hash %q", encoded)
		}
	}
}

func TestPasswordHashRoundTripAndFreshSalt(t *testing.T) {
	const password = "correct horse battery staple"
	first, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	second, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword second: %v", err)
	}
	if first == second {
		t.Fatal("password hashing reused salt")
	}
	if !strings.HasPrefix(first, "argon2id$v=19$m=65536,t=3,p=4$") {
		t.Fatalf("unexpected password hash contract: %q", first)
	}
	if !CheckPassword(first, password) {
		t.Fatal("correct password rejected")
	}
	if CheckPassword(first, "wrong password") {
		t.Fatal("wrong password accepted")
	}
}

func TestKeyAndDomainHashesAreDeterministicAndSeparated(t *testing.T) {
	full := "cave_live_abcdefghijkl_secret"
	first := HashProjectKey("pepper-a", full)
	if len(first) != 64 || first != HashProjectKey("pepper-a", full) {
		t.Fatalf("project-key hash = %q", first)
	}
	if first == HashProjectKey("pepper-b", full) || first == HashProjectKey("pepper-a", full+"x") {
		t.Fatal("project-key hash ignored pepper or key")
	}

	reset := HMACSHA256("pepper-a", "password-reset:v1\x00token")
	otherDomain := HMACSHA256("pepper-a", "token")
	if len(reset) != 64 || reset == otherDomain {
		t.Fatalf("domain HMAC reset=%q other=%q", reset, otherDomain)
	}
}

func TestParseProjectKeyAcceptsUnderscoreBearingPrefixes(t *testing.T) {
	// base64url prefixes may contain '_' — a real minted key with one at
	// position 5 must parse (the split-on-underscore regression rejected it).
	cases := map[string]struct {
		prefix string
		ok     bool
	}{
		"cave_live_BFmItFU3VME__0VJiqEnvvhpepEE43_ihxJEJd12Pcbw": {"BFmItFU3VME_", true},
		"cave_live_S5Cr_ILmQxxK_H6mbriFJ2cDb_9tCT9IkG5LQmNDRg_0": {"S5Cr_ILmQxxK", true},
		"cave_live_kIGDsmB453GF_tail-without-underscores":        {"kIGDsmB453GF", true},
		"Bearer cave_live_kIGDsmB453GF_secretsecret":             {"kIGDsmB453GF", true},
		"cave_live_short_x":       {"", false},
		"cave_live_twelvechars12": {"", false}, // no separator/secret after the prefix
		"sk-not-a-cave-key":       {"", false},
		"cave_live_kIGDsmB453GF_": {"", false}, // empty secret
	}
	for full, want := range cases {
		prefix, ok := ParseProjectKey(full)
		if ok != want.ok || prefix != want.prefix {
			t.Fatalf("ParseProjectKey(%q) = (%q, %v), want (%q, %v)", full, prefix, ok, want.prefix, want.ok)
		}
	}
}

func TestGeneratedProjectKeysAlwaysParse(t *testing.T) {
	// The generator draws from base64url (includes '_' and '-'); every key it
	// can mint must round-trip through ParseProjectKey.
	for i := 0; i < 2000; i++ {
		full, prefix, err := GenerateProjectKey()
		if err != nil {
			t.Fatalf("GenerateProjectKey: %v", err)
		}
		got, ok := ParseProjectKey(full)
		if !ok || got != prefix {
			t.Fatalf("minted key failed to parse: full=%q prefix=%q got=%q ok=%v", full, prefix, got, ok)
		}
	}
}
