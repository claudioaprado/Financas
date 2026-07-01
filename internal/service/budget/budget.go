// Package budget is the use-case for monthly category budget targets (FR-18,
// Story 8.1): the owner's current monthly target amount per category, expressed
// in the Display Currency. There is at most one target per category — setting a
// target upserts, so "no target" simply means the category is unbudgeted. The
// planned-vs-actual view and carryover are DERIVED on read from the ledger + these
// targets (AD-2/AD-10, Story 8.2) and never stored here. Amounts are decimal
// (NFR-5) and every write goes through one DB transaction (AD-3). Category kind is
// carried as a plain string to keep this package free of a service→service
// dependency (AD-1).
package budget

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioaprado/financas/internal/domain"
	"github.com/claudioaprado/financas/internal/money"
	"github.com/claudioaprado/financas/internal/store"
)

// Errors. The service is the validation authority; DB constraints back it up.
var (
	// ErrNonPositiveAmount rejects a zero or negative target: a budget is a
	// positive planned amount. Removing a budget is Delete, not a zero target.
	ErrNonPositiveAmount = errors.New("budget: amount must be greater than zero")
	// ErrCategoryNotFound is returned when the target's category does not exist
	// (the FK enforces it).
	ErrCategoryNotFound = errors.New("budget: category not found")
	// ErrNotFound is returned when deleting a budget that does not exist.
	ErrNotFound = errors.New("budget: not found")
)

// Budget is one category's monthly target with its category's name and kind
// joined in for display.
type Budget struct {
	CategoryID   int64
	CategoryName string
	Kind         string // "income" | "expense" (the category's kind)
	Amount       decimal.Decimal
}

// Service sets, lists, and deletes category budget targets.
type Service struct {
	pool *pgxpool.Pool
}

// New returns a budget Service backed by the given pool.
func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// Set upserts the monthly target for a category in one transaction (AD-3). The
// amount must be positive; the category must exist (the FK enforces it). Setting
// an existing category's target overwrites it (one target per category).
func (s *Service) Set(ctx context.Context, categoryID int64, amount decimal.Decimal) error {
	if amount.Sign() <= 0 {
		return ErrNonPositiveAmount
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("budget: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := store.New(tx).SetBudget(ctx, store.SetBudgetParams{CategoryID: categoryID, Amount: amount}); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" { // FK violation ⇒ no such category
			return ErrCategoryNotFound
		}
		return fmt.Errorf("budget: upsert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("budget: commit: %w", err)
	}
	return nil
}

// List returns all budget targets, each joined with its category's name and
// kind, ordered by kind then name (stable with the categories list).
func (s *Service) List(ctx context.Context) ([]Budget, error) {
	rows, err := store.New(s.pool).ListBudgets(ctx)
	if err != nil {
		return nil, fmt.Errorf("budget: list: %w", err)
	}
	out := make([]Budget, len(rows))
	for i, r := range rows {
		out[i] = Budget{CategoryID: r.CategoryID, CategoryName: r.CategoryName, Kind: r.CategoryKind, Amount: r.Amount}
	}
	return out, nil
}

// Report derives the planned×actual×remaining budget view with rollover for the
// selected month (AD-2/AD-10) — nothing here is stored. It gathers the authored
// inputs (Display Currency, the per-category targets, and every categorized
// income/expense transaction up to the end of the selected month), resolves each
// transaction's native→Display effective rate on its own date (convert-then-sum,
// AD-12; a missing rate yields a partial total, AD-6), and hands them to the
// canonical domain.Budget function. Reads on the pool (no write ⇒ no tx).
func (s *Service) Report(ctx context.Context, year int, month time.Month) (domain.BudgetReport, error) {
	q := store.New(s.pool)

	code, err := q.GetDisplayCurrency(ctx)
	if err != nil {
		return domain.BudgetReport{}, fmt.Errorf("budget: display currency: %w", err)
	}
	display := money.Currency(code)

	brows, err := q.ListBudgets(ctx)
	if err != nil {
		return domain.BudgetReport{}, fmt.Errorf("budget: list targets: %w", err)
	}
	targets := make([]domain.BudgetTarget, len(brows))
	for i, b := range brows {
		targets[i] = domain.BudgetTarget{CategoryID: b.CategoryID, Name: b.CategoryName, Kind: b.CategoryKind, Amount: b.Amount}
	}

	// Exclusive upper bound: the first day of the month after the selected one, so
	// every day of the selected month (and all prior months) is included.
	upper := time.Date(year, month+1, 1, 0, 0, 0, 0, time.UTC)
	rows, err := q.ListCategorizedForBudget(ctx, upper)
	if err != nil {
		return domain.BudgetReport{}, fmt.Errorf("budget: list categorized: %w", err)
	}

	// Cache resolved rates per (currency, date) — a month of a statement shares
	// dates, so this avoids re-querying the same effective rate repeatedly.
	type rateKey struct {
		cur  money.Currency
		date time.Time
	}
	rateCache := map[rateKey]struct {
		rate decimal.Decimal
		ok   bool
	}{}

	txns := make([]domain.BudgetTxn, 0, len(rows))
	for _, r := range rows {
		cur := money.Currency(r.Currency)
		bt := domain.BudgetTxn{
			CategoryID: r.CategoryID.Int64,
			Year:       r.OccurredOn.Year(),
			Month:      r.OccurredOn.Month(),
			Amount:     money.New(r.Amount, cur),
		}
		if cur == display {
			bt.HasRate = true // same currency ⇒ no conversion; domain uses the amount as-is
		} else {
			key := rateKey{cur: cur, date: r.OccurredOn}
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
					return domain.BudgetReport{}, fmt.Errorf("budget: effective rate: %w", rErr)
				}
				rateCache[key] = cached
			}
			bt.Rate, bt.HasRate = cached.rate, cached.ok
		}
		txns = append(txns, bt)
	}

	return domain.Budget(display, year, month, targets, txns), nil
}

// Delete removes a category's budget target (one transaction). Returns
// ErrNotFound if the category had no target.
func (s *Service) Delete(ctx context.Context, categoryID int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("budget: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	n, err := store.New(tx).DeleteBudget(ctx, categoryID)
	if err != nil {
		return fmt.Errorf("budget: delete: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("budget: commit: %w", err)
	}
	return nil
}
