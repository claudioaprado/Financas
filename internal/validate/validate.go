// Package validate holds the shared free-text guards used by the authoring
// services (accounts, categories, securities). Each service remains the
// validation authority (AD-3) and owns its own emptiness/domain rules; this
// package centralizes the cross-cutting concerns that were previously missing
// on every free-text field — a sane maximum length and a control-character
// reject — so an unbounded or malformed name can't reach an unbounded TEXT
// column. The DB length CHECKs (migration 00017) are the backstop.
//
// Inputs are expected already trimmed (callers TrimSpace and reject empty).
package validate

import (
	"errors"
	"unicode"
	"unicode/utf8"
)

// Shared limits. Generous for real use, bounded against abuse and accidental
// paste of a whole document into a name field.
const (
	// MaxNameLen is the maximum number of runes in a display name (account,
	// category, or security name).
	MaxNameLen = 200
	// MaxSymbolLen is the maximum number of runes in a security symbol/ticker.
	MaxSymbolLen = 32
)

// Shared validation sentinels. The http layer maps these to pt-BR messages
// (knownErrMsg); they are returned verbatim by the services.
var (
	// ErrNameTooLong means a display name exceeds MaxNameLen runes.
	ErrNameTooLong = errors.New("validate: name is too long")
	// ErrNameBadChars means a display name contains a control character.
	ErrNameBadChars = errors.New("validate: name contains invalid characters")
	// ErrSymbolTooLong means a symbol exceeds MaxSymbolLen runes.
	ErrSymbolTooLong = errors.New("validate: symbol is too long")
	// ErrSymbolBadChars means a symbol contains whitespace (interior included)
	// or a control character.
	ErrSymbolBadChars = errors.New("validate: symbol contains spaces or invalid characters")
)

// Name validates an already-trimmed, non-empty display name. Interior spaces
// are allowed — real names have them ("Conta Corrente", "PETR4 - Petrobras") —
// but the name is capped at MaxNameLen runes and may not contain control
// characters (rejects a pasted newline/tab or stray control byte). Emptiness is
// the caller's own concern and is not re-checked here.
func Name(s string) error {
	if utf8.RuneCountInString(s) > MaxNameLen {
		return ErrNameTooLong
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return ErrNameBadChars
		}
	}
	return nil
}

// Symbol validates an already-trimmed, non-empty ticker. It caps the length at
// MaxSymbolLen runes and rejects ANY whitespace (interior included, so "PE TR 4"
// is refused) and control characters. Punctuation common in tickers (".", "-")
// is intentionally allowed (e.g. "BRK.B"). Case normalization is the caller's
// responsibility (security.Create upper-cases first).
func Symbol(s string) error {
	if utf8.RuneCountInString(s) > MaxSymbolLen {
		return ErrSymbolTooLong
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return ErrSymbolBadChars
		}
	}
	return nil
}
