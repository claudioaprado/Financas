package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/alexedwards/argon2id"
)

func newOwner(t *testing.T, user, pass string) Owner {
	t.Helper()
	h, err := argon2id.CreateHash(pass, argon2id.DefaultParams)
	if err != nil {
		t.Fatalf("CreateHash: %v", err)
	}
	return Owner{Username: user, PasswordHash: h}
}

func TestAuthenticate(t *testing.T) {
	const user, pass = "owner", "correct horse battery staple"
	a := New(newOwner(t, user, pass))
	ctx := context.Background()

	if err := a.Authenticate(ctx, user, pass); err != nil {
		t.Fatalf("valid credentials rejected: %v", err)
	}
	if err := a.Authenticate(ctx, user, "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password: got %v, want ErrInvalidCredentials", err)
	}
	if err := a.Authenticate(ctx, "intruder", pass); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong username: got %v, want ErrInvalidCredentials", err)
	}
}

func TestAuthenticateMalformedHash(t *testing.T) {
	a := New(Owner{Username: "owner", PasswordHash: "not-a-valid-phc-hash"})
	if err := a.Authenticate(context.Background(), "owner", "whatever"); err == nil {
		t.Fatal("expected error for malformed stored hash")
	}
}
