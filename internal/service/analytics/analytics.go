// Package analytics is the read-only use-case for the spending & cash-flow view
// (FR-19, Story 8.3): expense-by-category and monthly income-vs-expense over a
// trailing window, in the Display Currency. Everything is DERIVED on read from the
// ledger (AD-2/AD-10) by the canonical domain.Analyze function — nothing is
// stored. Conversion is convert-then-sum at each transaction's effective rate
// (AD-12); a missing rate yields a partial total (AD-6). Reads go on the pool (no
// write ⇒ no transaction); currency + rates are read straight from the store, so
// there is no service→service dependency (AD-1).
package analytics

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioaprado/financas/internal/domain"
	"github.com/claudioaprado/financas/internal/money"
	"github.com/claudioaprado/financas/internal/store"
)

// Service derives the spending & cash-flow analytics.
type Service struct {
	pool *pgxpool.Pool
	// now returns the current time; overridable in tests for a stable window.
	now func() time.Time
}

// New returns an analytics Service backed by the given pool.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, now: func() time.Time { return time.Now().UTC() }}
}

// Report derives the analytics for the trailing `months` months ending in the
// current month, in the Display Currency. It gathers every income/expense ledger
// entry in the window, resolves each entry's native→Display effective rate on its
// own date (cached per currency+date), and hands them to domain.Analyze. `months`
// is clamped to at least 1.
func (s *Service) Report(ctx context.Context, months int) (domain.Analytics, error) {
	if months < 1 {
		months = 1
	}
	q := store.New(s.pool)

	code, err := q.GetDisplayCurrency(ctx)
	if err != nil {
		return domain.Analytics{}, fmt.Errorf("analytics: display currency: %w", err)
	}
	display := money.Currency(code)

	now := s.now()
	anchorYear, anchorMonth := now.Year(), now.Month()
	// Window: [first day of the start month, first day of the month after anchor).
	lower := time.Date(anchorYear, anchorMonth-time.Month(months-1), 1, 0, 0, 0, 0, time.UTC)
	upper := time.Date(anchorYear, anchorMonth+1, 1, 0, 0, 0, 0, time.UTC)

	rows, err := q.ListLedgerForAnalytics(ctx, store.ListLedgerForAnalyticsParams{
		OccurredOn:   lower,
		OccurredOn_2: upper,
	})
	if err != nil {
		return domain.Analytics{}, fmt.Errorf("analytics: list ledger: %w", err)
	}

	// Key the cache by a normalized civil date string, not a time.Time — time.Time
	// map keys compare the wall clock + monotonic + location and are a known
	// footgun; the calendar date is what the effective rate actually depends on.
	type rateKey struct {
		cur  money.Currency
		date string
	}
	rateCache := map[rateKey]struct {
		rate decimal.Decimal
		ok   bool
	}{}

	entries := make([]domain.LedgerEntry, 0, len(rows))
	for _, r := range rows {
		cur := money.Currency(r.Currency)
		e := domain.LedgerEntry{
			Type:     r.Type,
			Year:     r.OccurredOn.Year(),
			Month:    r.OccurredOn.Month(),
			Category: r.CategoryName.String, // "" when NULL (uncategorized)
			Amount:   money.New(r.Amount, cur),
		}
		if cur == display {
			e.HasRate = true // same currency ⇒ no conversion
		} else {
			key := rateKey{cur: cur, date: r.OccurredOn.Format("2006-01-02")}
			cached, seen := rateCache[key]
			if !seen {
				rate, rErr := q.RateEffectiveAt(ctx, store.RateEffectiveAtParams{
					FromCurrency:  string(cur),
					ToCurrency:    string(display),
					EffectiveDate: r.OccurredOn,
				})
				cached.ok = rErr == nil
				cached.rate = rate
				if rErr != nil && !errors.Is(rErr, pgx.ErrNoRows) {
					return domain.Analytics{}, fmt.Errorf("analytics: effective rate: %w", rErr)
				}
				rateCache[key] = cached
			}
			e.Rate, e.HasRate = cached.rate, cached.ok
		}
		entries = append(entries, e)
	}

	return domain.Analyze(display, anchorYear, anchorMonth, months, entries), nil
}
