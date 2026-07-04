// Package auth handles identity: password hashing (here), JWT issue/verify
// (jwt.go), and register/login orchestration (service.go).
package auth

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// HashPassword returns a bcrypt hash of the plaintext. bcrypt generates a random
// salt and applies a tunable cost (work factor), then packs BOTH into the
// returned string — so we store just this one value in the password column.
// DefaultCost (10) is a sane security/latency balance; raise it as hardware improves.
func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("failed to hash password: %w", err)
	}
	return string(bytes), nil
}

// CheckPassword reports whether plaintext `password` matches the stored bcrypt
// `hashedPassword`. bcrypt re-derives the salt and cost from the hash itself and
// compares. Returns a plain bool — the caller doesn't need to know why it failed.
func CheckPassword(hashedPassword, password string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(password))
	return err == nil
}
