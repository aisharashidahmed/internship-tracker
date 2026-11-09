// Package auth holds the security rules for accounts: how passwords are
// hashed, how session and invite tokens are made, and what counts as a
// valid email or password. It does no I/O; package store saves the results.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

// SessionLength is how long a login lasts after it was last used.
const SessionLength = 30 * 24 * time.Hour

// bcryptCost: each +1 doubles the work for us and for anyone guessing
// passwords from a stolen database. 12 is a common choice today.
const bcryptCost = 12

// MinPasswordLength follows NIST's current advice: require length, not
// symbols. MaxPasswordBytes is bcrypt's limit; longer input would be
// silently cut off, so we refuse it instead.
const (
	MinPasswordLength = 10
	MaxPasswordBytes  = 72
)

var (
	ErrPasswordTooShort = errors.New("Use at least 10 characters.")
	ErrPasswordTooLong  = errors.New("Use at most 72 bytes (about 72 characters).")
	ErrBadEmail         = errors.New("Enter a valid email address.")
)

// CheckPassword says whether a new password is acceptable.
func CheckPassword(pw string) error {
	switch {
	case utf8.RuneCountInString(pw) < MinPasswordLength:
		return ErrPasswordTooShort
	case len(pw) > MaxPasswordBytes:
		return ErrPasswordTooLong
	}
	return nil
}

// HashPassword returns the bcrypt hash to store. It contains a random
// salt, so hashing the same password twice gives different results.
func HashPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	return string(h), err
}

// dummyHash is compared against when an email has no account, so a failed
// login takes the same time either way and doesn't reveal who has one.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("not a real password"), bcryptCost)

// PasswordMatches checks pw against a stored hash. Pass hash "" when the
// account doesn't exist: it still does the slow comparison, then says no.
func PasswordMatches(hash, pw string) bool {
	if hash == "" {
		bcrypt.CompareHashAndPassword(dummyHash, []byte(pw))
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// NormalizeEmail trims spaces and checks the address looks valid. The
// database compares emails lowercased, so case is kept as typed.
func NormalizeEmail(email string) (string, error) {
	email = strings.TrimSpace(email)
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || !strings.Contains(email[strings.LastIndex(email, "@"):], ".") {
		return "", ErrBadEmail
	}
	return email, nil
}

// NewToken returns a random token for a cookie or invite link, and the
// hash to store. 32 random bytes can't be guessed; storing only the hash
// means a leaked database contains no usable tokens.
func NewToken() (token string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

// HashToken is how a token from a cookie or link is looked up.
func HashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
