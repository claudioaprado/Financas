// Package exchangerate is the use-case for owner-entered exchange rates. Rates
// are directional, effective-dated, and append-only; they are NEVER inverted in
// code — a missing direction returns ErrNoRate so the caller can prompt the
// owner (AD-6). Writes go through one transaction per use-case (AD-3).
package exchangerate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/internal/money"
	"github.com/claudioaprado/financas/internal/store"
)

var (
	// ErrNoRate means no rate exists for the exact from->to direction on or
	// before the requested date. The system prompts rather than inverting (AD-6).
	ErrNoRate = errors.New("exchangerate: no rate for that pair on or before that date")
	// ErrUnsupportedCurrency, ErrSameCurrency, ErrNonPositiveRate are input errors.
	ErrUnsupportedCurrency = errors.New("exchangerate: unsupported currency")
	ErrSameCurrency        = errors.New("exchangerate: from and to currency must differ")
	ErrNonPositiveRate     = errors.New("exchangerate: rate must be positive")
)

// Rate is one effective-dated, directional exchange-rate row: 1 unit of From =
// Rate units of To, as of EffectiveDate.
type Rate struct {
	ID            int64
	From          money.Currency
	To            money.Currency
	EffectiveDate time.Time
	Rate          decimal.Decimal
	CreatedAt     time.Time
}

// Service reads and appends exchange rates.
type Service struct {
	pool *pgxpool.Pool
}

// New returns an exchange-rate Service backed by the given pool.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// Add appends a new directional rate after validating the currencies, that they
// differ, and that the rate is positive. It writes inside one transaction (AD-3).
func (s *Service) Add(ctx context.Context, from, to money.Currency, effective time.Time, rate decimal.Decimal) (Rate, error) {
	if !money.IsSupported(from) || !money.IsSupported(to) {
		return Rate{}, ErrUnsupportedCurrency
	}
	if from == to {
		return Rate{}, ErrSameCurrency
	}
	if !rate.IsPositive() {
		return Rate{}, ErrNonPositiveRate
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Rate{}, fmt.Errorf("exchangerate: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row, err := store.New(tx).AddExchangeRate(ctx, store.AddExchangeRateParams{
		FromCurrency:  string(from),
		ToCurrency:    string(to),
		EffectiveDate: effective,
		Rate:          rate,
	})
	if err != nil {
		return Rate{}, fmt.Errorf("exchangerate: insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Rate{}, fmt.Errorf("exchangerate: commit: %w", err)
	}
	return toRate(row), nil
}

// RateAt returns the rate for the exact from->to direction effective at (<=)
// date — the latest such row. It NEVER inverts a to->from rate or guesses
// 1/rate; a missing pair returns ErrNoRate (AD-6). "Latest for now" is date=today.
func (s *Service) RateAt(ctx context.Context, from, to money.Currency, date time.Time) (decimal.Decimal, error) {
	rate, err := store.New(s.pool).RateEffectiveAt(ctx, store.RateEffectiveAtParams{
		FromCurrency:  string(from),
		ToCurrency:    string(to),
		EffectiveDate: date,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return decimal.Decimal{}, ErrNoRate
	}
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("exchangerate: rate at: %w", err)
	}
	return rate, nil
}

// List returns all rates, newest effective-date first.
func (s *Service) List(ctx context.Context) ([]Rate, error) {
	rows, err := store.New(s.pool).ListExchangeRates(ctx)
	if err != nil {
		return nil, fmt.Errorf("exchangerate: list: %w", err)
	}
	out := make([]Rate, len(rows))
	for i, r := range rows {
		out[i] = toRate(r)
	}
	return out, nil
}

func toRate(r store.ExchangeRate) Rate {
	return Rate{
		ID:            r.ID,
		From:          money.Currency(r.FromCurrency),
		To:            money.Currency(r.ToCurrency),
		EffectiveDate: r.EffectiveDate,
		Rate:          r.Rate,
		CreatedAt:     r.CreatedAt.Time,
	}
}
