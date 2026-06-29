// Package account is the use-case for the owner's accounts (FR-1). An Account is
// a cash, credit, or investment holder in a single base currency. Accounts are
// created, renamed, and archived (never deleted — archiving preserves history).
//
// Balances and Net Worth are NOT stored here: they are derived from the
// transaction ledger on read (AD-2) by domain functions in later epics. This
// package owns only the authored Account state. The account's Type is the
// contract those derivations key off (cash/investment carry a cash balance;
// credit tracks a balance owed). Writes go through one transaction per use-case
// (AD-3); the base currency is stored natively and never converted (AD-5).
package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioaprado/financas/internal/money"
	"github.com/claudioaprado/financas/internal/store"
)

// AccountType is the kind of account, which fixes its balance semantics.
type AccountType string

const (
	// Cash is a cash/bank account; its balance is an asset.
	Cash AccountType = "cash"
	// Credit is a credit account; it tracks a balance owed (a liability).
	Credit AccountType = "credit"
	// Investment is a brokerage account; it carries a cash balance plus holdings.
	Investment AccountType = "investment"
)

// IsValid reports whether t is one of the supported account types.
func (t AccountType) IsValid() bool {
	switch t {
	case Cash, Credit, Investment:
		return true
	default:
		return false
	}
}

// Input errors. The service is the validation authority; DB CHECK/FK constraints
// are the backstop.
var (
	ErrEmptyName           = errors.New("account: name must not be empty")
	ErrInvalidType         = errors.New("account: type must be cash, credit, or investment")
	ErrUnsupportedCurrency = errors.New("account: unsupported currency")
	// ErrNotFound means no account matched the given id (e.g. on rename/archive).
	ErrNotFound = errors.New("account: not found")
)

// Account is one of the owner's accounts. Balances are derived elsewhere (AD-2)
// and are deliberately absent here.
type Account struct {
	ID        int64
	Name      string
	Type      AccountType
	Currency  money.Currency
	Archived  bool
	CreatedAt time.Time
}

// Service creates, lists, renames, and archives accounts.
type Service struct {
	pool *pgxpool.Pool
}

// New returns an account Service backed by the given pool.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// Create validates and appends a new account, returning the stored row. It
// writes inside one transaction (AD-3).
func (s *Service) Create(ctx context.Context, name string, typ AccountType, currency money.Currency) (Account, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Account{}, ErrEmptyName
	}
	if !typ.IsValid() {
		return Account{}, ErrInvalidType
	}
	if !money.IsSupported(currency) {
		return Account{}, ErrUnsupportedCurrency
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Account{}, fmt.Errorf("account: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row, err := store.New(tx).CreateAccount(ctx, store.CreateAccountParams{
		Name:     name,
		Type:     string(typ),
		Currency: string(currency),
	})
	if err != nil {
		return Account{}, fmt.Errorf("account: insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Account{}, fmt.Errorf("account: commit: %w", err)
	}
	return toAccount(row), nil
}

// Rename changes an account's name (preserving its identity and history). A
// missing id returns ErrNotFound. It writes inside one transaction (AD-3).
func (s *Service) Rename(ctx context.Context, id int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrEmptyName
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("account: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	n, err := store.New(tx).RenameAccount(ctx, store.RenameAccountParams{ID: id, Name: name})
	if err != nil {
		return fmt.Errorf("account: rename: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("account: commit: %w", err)
	}
	return nil
}

// SetArchived archives or unarchives an account. Archiving preserves history but
// excludes the account from default views and current Net Worth. A missing id
// returns ErrNotFound. It writes inside one transaction (AD-3).
func (s *Service) SetArchived(ctx context.Context, id int64, archived bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("account: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	n, err := store.New(tx).SetAccountArchived(ctx, store.SetAccountArchivedParams{ID: id, Archived: archived})
	if err != nil {
		return fmt.Errorf("account: set archived: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("account: commit: %w", err)
	}
	return nil
}

// List returns accounts ordered by name. With includeArchived false (the default
// view) archived accounts are excluded; with true, all accounts are returned.
func (s *Service) List(ctx context.Context, includeArchived bool) ([]Account, error) {
	var rows []store.Account
	var err error
	if includeArchived {
		rows, err = store.New(s.pool).ListAllAccounts(ctx)
	} else {
		rows, err = store.New(s.pool).ListActiveAccounts(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("account: list: %w", err)
	}
	out := make([]Account, len(rows))
	for i, r := range rows {
		out[i] = toAccount(r)
	}
	return out, nil
}

func toAccount(r store.Account) Account {
	return Account{
		ID:        r.ID,
		Name:      r.Name,
		Type:      AccountType(r.Type),
		Currency:  money.Currency(r.Currency),
		Archived:  r.Archived,
		CreatedAt: r.CreatedAt.Time,
	}
}
