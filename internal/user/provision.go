package user

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"q-wash-api/internal/platform/password"
)

// Credential is a one-time-visible username/password pair — the plaintext
// Password is never persisted or logged; only its bcrypt hash is stored,
// and only the response that creates or resets it ever carries the
// plaintext.
type Credential struct {
	Username string
	Password string
}

// PointAccounts is the pair of accounts every washing point gets: a staff
// login (cabinet access) and a worker login (worker app access), both
// scoped to that point via WashingPointID.
type PointAccounts struct {
	Staff  Credential
	Worker Credential
}

var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// slugify turns a washing point name into a lowercase, hyphenated,
// ASCII-only base for a username. A name with no ASCII letters/digits at
// all (e.g. purely Cyrillic) falls back to "point" so a base always
// exists.
func slugify(name string) string {
	s := slugNonAlnum.ReplaceAllString(strings.ToLower(name), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "point"
	}
	return s
}

// uniqueUsername tries base, then base-2, base-3, ... against tx (the
// caller's transaction, so it sees uncommitted rows from earlier in the
// same call) until one doesn't already exist.
func uniqueUsername(tx *gorm.DB, base string) (string, error) {
	for i := 1; ; i++ {
		candidate := base
		if i > 1 {
			candidate = fmt.Sprintf("%s-%d", base, i)
		}
		var count int64
		if err := tx.Model(&User{}).Where("username = ?", candidate).Count(&count).Error; err != nil {
			return "", err
		}
		if count == 0 {
			return candidate, nil
		}
	}
}

func newCredential(tx *gorm.DB, washingPointID uuid.UUID, role Role, usernameBase string) (Credential, error) {
	username, err := uniqueUsername(tx, usernameBase)
	if err != nil {
		return Credential{}, err
	}
	plaintext, err := password.GenerateRandom()
	if err != nil {
		return Credential{}, err
	}
	hash, err := password.Hash(plaintext)
	if err != nil {
		return Credential{}, err
	}
	u := &User{
		Role:           role,
		WashingPointID: &washingPointID,
		Username:       &username,
		PasswordHash:   &hash,
	}
	if err := tx.Create(u).Error; err != nil {
		return Credential{}, err
	}
	return Credential{Username: username, Password: plaintext}, nil
}

// ProvisionPointAccounts creates a staff and a worker login for a newly
// created washing point, inside the caller's transaction (tx) so they
// either both land with the point row or not at all. pointName seeds the
// username (slugified; worker gets a "-worker" suffix); colliding with an
// existing username anywhere in the system gets a numeric suffix.
func ProvisionPointAccounts(ctx context.Context, tx *gorm.DB, washingPointID uuid.UUID, pointName string) (PointAccounts, error) {
	tx = tx.WithContext(ctx)
	base := slugify(pointName)

	staff, err := newCredential(tx, washingPointID, RoleStaff, base)
	if err != nil {
		return PointAccounts{}, err
	}
	worker, err := newCredential(tx, washingPointID, RoleWorker, base+"-worker")
	if err != nil {
		return PointAccounts{}, err
	}
	return PointAccounts{Staff: staff, Worker: worker}, nil
}

// ResetPassword regenerates a single account's password (staff/worker
// login reset, internal/admin) and returns the new plaintext once.
func ResetPassword(ctx context.Context, repo *Repository, u *User) (string, error) {
	plaintext, err := password.GenerateRandom()
	if err != nil {
		return "", err
	}
	hash, err := password.Hash(plaintext)
	if err != nil {
		return "", err
	}
	if err := repo.SetCredentials(ctx, u.ID, *u.Username, hash); err != nil {
		return "", err
	}
	return plaintext, nil
}
