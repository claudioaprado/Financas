// Package price is the use-case for owner-entered security prices. Prices are
// effective-dated and append-only — corrections are new effective-dated rows; the
// most recent price on or before a date is used for valuation. There is NO
// external/online/automated market-data feed (AD-6). A security with no price on
// or before the query date returns ErrNoPrice (never a fabricated/guessed price).
// Writes go through one transaction per use-case (AD-3). A price has no currency
// of its own — it is implicitly the security's quote currency (AD-5).
package price

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
	// ErrNoPrice means no price exists for the security on or before the
	// requested date — the analog of exchangerate.ErrNoRate. The caller shows the
	// holding without a market value rather than guessing a price (AD-6).
	ErrNoPrice = errors.New("price: no price for that security on or before that date")
	// ErrSecurityNotFound means the price targets a security that does not exist.
	ErrSecurityNotFound = errors.New("price: security not found")
	// ErrNonPositivePrice means the entered price was zero or negative.
	ErrNonPositivePrice = errors.New("price: price must be positive")
)

// Price is one effective-dated price for a security, in the security's quote
// currency. Symbol is resolved for display.
type Price struct {
	ID            int64
	SecurityID    int64
	Symbol        string
	Currency      money.Currency // the security's quote currency (prices are native, AD-5)
	EffectiveDate time.Time
	Price         decimal.Decimal
	CreatedAt     time.Time
}

// PricePoint is the latest price for a security plus its effective date, used for
// valuation and staleness display.
type PricePoint struct {
	Price         decimal.Decimal
	EffectiveDate time.Time
}

// Service reads and appends security prices.
type Service struct {
	pool *pgxpool.Pool
}

// New returns a price Service backed by the given pool.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// Add appends a new effective-dated price after validating that the security
// exists and the price is positive. It writes inside one transaction (AD-3).
func (s *Service) Add(ctx context.Context, securityID int64, effective time.Time, price decimal.Decimal) (Price, error) {
	// Round to the money scale once at the entry boundary (banker's), so storage
	// matches the valuation rounding convention (AD-4/AD-12) and Postgres never
	// silently re-rounds the NUMERIC(19,4) column half-away-from-zero. A sub-scale
	// input that rounds to zero is then rejected with the typed error rather than
	// surfacing a raw CHECK (price > 0) violation.
	price = price.RoundBank(money.MoneyScale)
	if !price.IsPositive() {
		return Price{}, ErrNonPositivePrice
	}

	sec, err := store.New(s.pool).GetSecurity(ctx, securityID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Price{}, ErrSecurityNotFound
	}
	if err != nil {
		return Price{}, fmt.Errorf("price: get security: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Price{}, fmt.Errorf("price: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row, err := store.New(tx).AddPrice(ctx, store.AddPriceParams{
		SecurityID:    securityID,
		EffectiveDate: effective,
		Price:         price,
	})
	if err != nil {
		return Price{}, fmt.Errorf("price: insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Price{}, fmt.Errorf("price: commit: %w", err)
	}
	return Price{
		ID:            row.ID,
		SecurityID:    row.SecurityID,
		Symbol:        sec.Symbol,
		Currency:      money.Currency(sec.QuoteCurrency),
		EffectiveDate: row.EffectiveDate,
		Price:         row.Price,
		CreatedAt:     row.CreatedAt.Time,
	}, nil
}

// PriceAt returns the security's price effective at (<=) date — the latest such
// row. A missing price returns ErrNoPrice; it never guesses (AD-6). "Latest for
// now" is date=today.
func (s *Service) PriceAt(ctx context.Context, securityID int64, date time.Time) (decimal.Decimal, error) {
	p, err := store.New(s.pool).PriceEffectiveAt(ctx, store.PriceEffectiveAtParams{
		SecurityID:    securityID,
		EffectiveDate: date,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return decimal.Decimal{}, ErrNoPrice
	}
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("price: price at: %w", err)
	}
	return p, nil
}

// LatestPrices returns, in one query, the latest price (effective <= asOf) per
// security as a map keyed by security id — used by the holdings valuation read so
// it does not issue one query per holding.
func (s *Service) LatestPrices(ctx context.Context, asOf time.Time) (map[int64]PricePoint, error) {
	rows, err := store.New(s.pool).LatestPrices(ctx, asOf)
	if err != nil {
		return nil, fmt.Errorf("price: latest prices: %w", err)
	}
	out := make(map[int64]PricePoint, len(rows))
	for _, r := range rows {
		out[r.SecurityID] = PricePoint{Price: r.Price, EffectiveDate: r.EffectiveDate}
	}
	return out, nil
}

// List returns all prices, newest effective-date first, with the security symbol.
func (s *Service) List(ctx context.Context) ([]Price, error) {
	rows, err := store.New(s.pool).ListPrices(ctx)
	if err != nil {
		return nil, fmt.Errorf("price: list: %w", err)
	}
	out := make([]Price, len(rows))
	for i, r := range rows {
		out[i] = Price{
			ID:            r.ID,
			SecurityID:    r.SecurityID,
			Symbol:        r.Symbol,
			Currency:      money.Currency(r.QuoteCurrency),
			EffectiveDate: r.EffectiveDate,
			Price:         r.Price,
			CreatedAt:     r.CreatedAt.Time,
		}
	}
	return out, nil
}
