// Package settings is the use-case for application settings — currently the
// owner's Display Currency. It mutates state only through one DB transaction
// per write (AD-3) and treats the Display Currency as a presentation choice
// that never alters stored amounts (AD-5).
package settings

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioaprado/financas/internal/money"
	"github.com/claudioaprado/financas/internal/store"
)

// ErrUnsupportedCurrency is returned when a Display Currency outside the
// supported set is requested.
var ErrUnsupportedCurrency = errors.New("settings: unsupported display currency")

// Service reads and updates application settings.
type Service struct {
	pool *pgxpool.Pool
}

// New returns a settings Service backed by the given pool.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// DisplayCurrency returns the owner's chosen Display Currency.
func (s *Service) DisplayCurrency(ctx context.Context) (money.Currency, error) {
	code, err := store.New(s.pool).GetDisplayCurrency(ctx)
	if err != nil {
		return "", fmt.Errorf("settings: get display currency: %w", err)
	}
	return money.Currency(code), nil
}

// SetDisplayCurrency persists the Display Currency. It rejects unsupported
// currencies and writes inside a single transaction (AD-3). It changes only the
// settings row — no stored amount is touched (AD-5).
func (s *Service) SetDisplayCurrency(ctx context.Context, c money.Currency) error {
	if !money.IsSupported(c) {
		return ErrUnsupportedCurrency
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("settings: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := store.New(tx).SetDisplayCurrency(ctx, string(c)); err != nil {
		return fmt.Errorf("settings: set display currency: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("settings: commit: %w", err)
	}
	return nil
}

// ListCurrencies returns the available currencies (ISO-4217 codes).
func (s *Service) ListCurrencies(ctx context.Context) ([]money.Currency, error) {
	rows, err := store.New(s.pool).ListCurrencies(ctx)
	if err != nil {
		return nil, fmt.Errorf("settings: list currencies: %w", err)
	}
	out := make([]money.Currency, len(rows))
	for i, r := range rows {
		out[i] = money.Currency(r.Code)
	}
	return out, nil
}
