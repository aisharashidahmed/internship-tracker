package auth

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestPasswordHashing(t *testing.T) {
	h1, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := HashPassword("correct horse battery")
	if h1 == h2 {
		t.Error("same password hashed twice gave the same hash; the salt is missing")
	}
	if strings.Contains(h1, "correct horse") {
		t.Error("the hash contains the password")
	}
	if !PasswordMatches(h1, "correct horse battery") {
		t.Error("the right password was rejected")
	}
	if PasswordMatches(h1, "correct horse batterY") {
		t.Error("a wrong password was accepted")
	}
	if PasswordMatches("", "anything at all") {
		t.Error("a missing account must never match")
	}
}

func TestCheckPassword(t *testing.T) {
	tests := map[string]error{
		"short":                         ErrPasswordTooShort,
		"exactly10!":                    nil,
		"a long passphrase with spaces": nil,
		"ééééééééé":                     ErrPasswordTooShort, // 9 characters, even though 18 bytes
		strings.Repeat("a", 73):         ErrPasswordTooLong,  // bcrypt would silently ignore the 73rd byte
	}
	for pw, want := range tests {
		if got := CheckPassword(pw); got != want {
			t.Errorf("CheckPassword(%q) = %v, want %v", pw, got, want)
		}
	}
}

func TestNormalizeEmail(t *testing.T) {
	good := map[string]string{
		"aisha@college.edu":     "aisha@college.edu",
		"  Aisha@College.edu  ": "Aisha@College.edu", // trimmed; case kept, the DB compares lowercased
	}
	for in, want := range good {
		if got, err := NormalizeEmail(in); err != nil || got != want {
			t.Errorf("NormalizeEmail(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "aisha", "aisha@", "aisha@localhost", "Aisha <aisha@x.com>", "a b@x.com"} {
		if _, err := NormalizeEmail(bad); err == nil {
			t.Errorf("NormalizeEmail(%q) accepted a bad address", bad)
		}
	}
}

func TestNewToken(t *testing.T) {
	a, ha, _ := NewToken()
	b, _, _ := NewToken()
	if a == b {
		t.Error("two tokens were equal")
	}
	if len(a) < 40 {
		t.Errorf("token %q is too short to be unguessable", a)
	}
	if !bytes.Equal(ha, HashToken(a)) {
		t.Error("the returned hash doesn't match HashToken(token)")
	}
}

func TestLimiter(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	l := NewLimiter(3, 15*time.Minute)
	l.now = func() time.Time { return now }

	for range 3 {
		if l.Blocked("aisha@x.com") {
			t.Fatal("blocked before using up the attempts")
		}
		l.Fail("aisha@x.com")
	}
	if !l.Blocked("aisha@x.com") {
		t.Error("not blocked after 3 failures")
	}
	if l.Blocked("someone@else.com") {
		t.Error("one key's failures blocked another key")
	}

	now = now.Add(16 * time.Minute) // the window has passed
	if l.Blocked("aisha@x.com") {
		t.Error("still blocked after the window")
	}

	l.Fail("aisha@x.com")
	l.Reset("aisha@x.com")
	if len(l.failures) != 0 {
		t.Error("Reset kept failures")
	}
}
