// Package auth verifies the single owner's credentials. It decides credential
// validity only — it performs no HTTP, cookie, or session work (AD-1, AD-7).
// The owner's username and argon2id hash are supplied from configuration, so
// the data model carries no owner table or column.
package auth

import (
	"context"
	"crypto/subtle"
	"errors"

	"github.com/alexedwards/argon2id"
)

// ErrInvalidCredentials is returned for any authentication failure, without
// distinguishing a wrong username from a wrong password.
var ErrInvalidCredentials = errors.New("auth: invalid credentials")

// Owner is the single configured owner credential: a username and a PHC-format
// argon2id password hash.
type Owner struct {
	Username     string
	PasswordHash string
}

// Authenticator verifies submitted credentials against the configured owner.
type Authenticator struct {
	owner Owner
}

// New returns an Authenticator for the given owner credential.
func New(owner Owner) *Authenticator {
	return &Authenticator{owner: owner}
}

// Authenticate returns nil when username and password match the configured
// owner, and ErrInvalidCredentials otherwise. It always runs the argon2id hash
// comparison and uses a constant-time username comparison, so a wrong username
// and a wrong password take the same work — closing a user-enumeration timing
// side channel. A malformed stored hash yields a non-nil (non-sentinel) error.
func (a *Authenticator) Authenticate(_ context.Context, username, password string) error {
	match, err := argon2id.ComparePasswordAndHash(password, a.owner.PasswordHash)
	if err != nil {
		return err
	}
	userOK := subtle.ConstantTimeCompare([]byte(username), []byte(a.owner.Username)) == 1
	if !userOK || !match {
		return ErrInvalidCredentials
	}
	return nil
}
