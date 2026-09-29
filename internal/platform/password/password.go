// Package password generates and hashes one-time-visible credential
// passwords (staff/worker accounts auto-provisioned on washing point
// creation, internal/user). Kept separate from internal/auth — user
// imports this package, and auth imports user, so a leaf package avoids
// that import cycle.
package password

import (
	"crypto/rand"
	"math/big"

	"golang.org/x/crypto/bcrypt"
)

// charset excludes visually ambiguous characters (0/O, 1/l/I) since a
// generated password is meant to be read off a screen and copied or typed
// by hand at least once.
const charset = "23456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

const length = 16

// GenerateRandom returns a cryptographically random plaintext password.
func GenerateRandom() (string, error) {
	b := make([]byte, length)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			return "", err
		}
		b[i] = charset[n.Int64()]
	}
	return string(b), nil
}

// Hash bcrypt-hashes password for storage.
func Hash(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}
