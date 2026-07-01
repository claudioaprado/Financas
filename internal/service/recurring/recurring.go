// Package recurring is the use-case for recurring transaction templates (FR-20,
// Epic 9): the owner defines a repeating income/expense/transfer once, the app
// surfaces DUE occurrences (next-due ≤ today), and the owner posts each with one
// click — no background scheduler. The template is the only authored state; its
// `next_due` cursor is advanced when the owner posts or skips. Whether a template
// is due is DERIVED on read (AD-2/AD-10) via domain.IsDue. Posting MATERIALIZES a
// real ledger row (a transfer is one two-account row, AD-9) and advances the
// cursor — all in one DB transaction (AD-3). Amounts are decimal (NFR-5).
//
// This package writes ledger rows directly through the store (not service/
// transaction) to keep the post-and-advance atomic in a single transaction and to
// avoid a service→service dependency (AD-1); the ledger's one-row from/to shape is
// replicated here deliberately.
package recurring

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/internal/domain"
	"github.com/claudioaprado/financas/internal/money"
	"github.com/claudioaprado/financas/internal/store"
)

// Transaction types a template can materialize (the ledger's type strings).
const (
	Income   = "income"
	Expense  = "expense"
	Transfer = "transfer"
)

// Validation errors. The service is the validation authority; DB constraints back
// it up.
var (
	ErrInvalidType         = errors.New("recurring: type must be income, expense, or transfer")
	ErrNonPositiveAmount   = errors.New("recurring: amount must be greater than zero")
	ErrInvalidCadence      = errors.New("recurring: cadence must be weeks, months, or years")
	ErrNonPositiveInterval = errors.New("recurring: interval must be a positive whole number")
	ErrMissingStartDate    = errors.New("recurring: start date is required")
	ErrInvalidDateRange    = errors.New("recurring: end date must not precede start date")
	ErrAccountNotFound     = errors.New("recurring: account not found")
	// ErrUnsupportedAccountType mirrors the ledger rule: income/expense require a
	// cash or credit account (investment cash flow is a trade, not a recurrence).
	ErrUnsupportedAccountType = errors.New("recurring: income/expense require a cash or credit account")
	ErrSameAccount            = errors.New("recurring: transfer source and destination must differ")
	// ErrToAmountRequired is returned when a cross-currency transfer omits the
	// destination amount (the rate is not stored — both legs are authored, AD-9).
	ErrToAmountRequired = errors.New("recurring: cross-currency transfer needs a destination amount")
	// ErrSameCurrencyAmountMismatch rejects a same-currency transfer whose legs
	// differ (they must be equal, AD-9).
	ErrSameCurrencyAmountMismatch = errors.New("recurring: same-currency transfer must have equal amounts")
	ErrCategoryNotFound           = errors.New("recurring: category not found")
	ErrCategoryKindMismatch       = errors.New("recurring: category kind must match the transaction type")
	ErrCategoryOnTransfer         = errors.New("recurring: transfers are never categorized")
	ErrNotFound                   = errors.New("recurring: not found")
	// ErrNotDue is returned when posting/skipping an occurrence that is not
	// currently due (already advanced, not yet arrived, or past the end date) —
	// which makes a duplicate one-click post a harmless no-op.
	ErrNotDue = errors.New("recurring: occurrence is not due")
)

// Input is the authored template as entered by the owner. For income/expense
// AccountID is the affected account; for a transfer FromAccountID and ToAccountID
// are the two legs. Amount is the (from-leg) magnitude; ToAmount is only the
// destination leg of a cross-currency transfer (0 ⇒ same currency, mirrored).
type Input struct {
	Type          string
	AccountID     int64 // income/expense account
	FromAccountID int64 // transfer source
	ToAccountID   int64 // transfer destination
	Amount        decimal.Decimal
	ToAmount      decimal.Decimal
	CategoryID    int64 // 0 = none
	Cadence       string
	IntervalN     int
	StartDate     time.Time
	EndDate       *time.Time // nil = open-ended
	Description   string
}

// Recurring is one template formatted for display, with names resolved and the
// derived Due flag. Amount is in its primary account's currency; for a
// cross-currency transfer ToAmount carries the destination leg.
type Recurring struct {
	ID            int64
	Type          string
	FromAccountID int64
	FromAccount   string
	ToAccountID   int64
	ToAccount     string
	Amount        money.Money
	ToAmount      money.Money
	CrossCurrency bool
	CategoryID    int64
	CategoryName  string
	Cadence       string
	IntervalN     int
	StartDate     time.Time
	EndDate       *time.Time
	NextDue       time.Time
	Description   string
	Due           bool
}

// Service creates, lists, edits, deletes, and posts recurring templates.
type Service struct {
	pool *pgxpool.Pool
}

// New returns a recurring Service backed by the given pool.
func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// Create validates and stores a template with its schedule cursor primed to the
// start date (one transaction, AD-3).
func (s *Service) Create(ctx context.Context, in Input) (int64, error) {
	p, err := s.resolve(ctx, in)
	if err != nil {
		return 0, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("recurring: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row, err := store.New(tx).CreateRecurring(ctx, store.CreateRecurringParams{
		Type:          p.typ,
		FromAccountID: p.from,
		ToAccountID:   p.to,
		Amount:        p.amount,
		ToAmount:      p.toAmount,
		CategoryID:    p.category,
		Cadence:       p.cadence,
		IntervalN:     int32(p.interval),
		StartDate:     p.start,
		EndDate:       dateParam(p.end),
		NextDue:       p.start, // first occurrence is the start date
		Description:   in.Description,
	})
	if err != nil {
		return 0, fmt.Errorf("recurring: insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("recurring: commit: %w", err)
	}
	return row.ID, nil
}

// Edit updates a template's fields (one transaction, AD-3). The schedule cursor
// is preserved so already-posted occurrences are not re-posted, but is pulled
// forward to the new start date if the start moved past it.
func (s *Service) Edit(ctx context.Context, id int64, in Input) error {
	p, err := s.resolve(ctx, in)
	if err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("recurring: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := store.New(tx)
	cur, err := q.GetRecurring(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("recurring: get: %w", err)
	}

	nextDue := cur.NextDue
	if nextDue.Before(p.start) {
		nextDue = p.start
	}

	n, err := q.UpdateRecurring(ctx, store.UpdateRecurringParams{
		ID:            id,
		Type:          p.typ,
		FromAccountID: p.from,
		ToAccountID:   p.to,
		Amount:        p.amount,
		ToAmount:      p.toAmount,
		CategoryID:    p.category,
		Cadence:       p.cadence,
		IntervalN:     int32(p.interval),
		StartDate:     p.start,
		EndDate:       dateParam(p.end),
		NextDue:       nextDue,
		Description:   in.Description,
	})
	if err != nil {
		return fmt.Errorf("recurring: update: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("recurring: commit: %w", err)
	}
	return nil
}

// Delete removes a template (one transaction). Materialized transactions it
// already posted are untouched. Returns ErrNotFound when nothing was deleted.
func (s *Service) Delete(ctx context.Context, id int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("recurring: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	n, err := store.New(tx).DeleteRecurring(ctx, id)
	if err != nil {
		return fmt.Errorf("recurring: delete: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("recurring: commit: %w", err)
	}
	return nil
}

// List returns every template, newest-due first, with the Due flag derived
// against today (AD-2/AD-10).
func (s *Service) List(ctx context.Context) ([]Recurring, error) {
	rows, err := store.New(s.pool).ListRecurring(ctx)
	if err != nil {
		return nil, fmt.Errorf("recurring: list: %w", err)
	}
	today := time.Now()
	out := make([]Recurring, len(rows))
	for i, r := range rows {
		out[i] = toRecurring(r, today)
	}
	return out, nil
}

// Due returns the templates whose next occurrence is due as of today (next_due ≤
// today, within the end date) — the remind list + dashboard nudge source.
func (s *Service) Due(ctx context.Context) ([]Recurring, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	due := make([]Recurring, 0, len(all))
	for _, r := range all {
		if r.Due {
			due = append(due, r)
		}
	}
	return due, nil
}

// Post materializes the template's current due occurrence as a real ledger row
// and advances the cursor, in one transaction (AD-3). The row is FOR UPDATE-locked
// so a concurrent or double-click post serializes; posting is idempotent — an
// `occurrence` that no longer matches the cursor, or a cursor that is not due,
// is a harmless no-op (ErrNotDue) rather than a second row for the same occurrence.
func (s *Service) Post(ctx context.Context, id int64, occurrence time.Time) error {
	return s.advance(ctx, id, occurrence, true)
}

// Skip advances the cursor past the current due occurrence WITHOUT materializing
// a transaction (one transaction, AD-3). Same locking/idempotency as Post.
func (s *Service) Skip(ctx context.Context, id int64, occurrence time.Time) error {
	return s.advance(ctx, id, occurrence, false)
}

// advance is the shared post/skip core: lock the row, confirm the occurrence is
// the current due one, optionally materialize it, then move the cursor forward.
func (s *Service) advance(ctx context.Context, id int64, occurrence time.Time, materialize bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("recurring: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := store.New(tx)
	r, err := q.GetRecurringForUpdate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("recurring: get for update: %w", err)
	}

	end := datePtr(r.EndDate)
	// Idempotency: the occurrence the owner acted on must still be the cursor, and
	// that cursor must be due. Either failing means it was already handled.
	if !sameDay(occurrence, r.NextDue) || !domain.IsDue(r.NextDue, end, time.Now()) {
		return ErrNotDue
	}

	if materialize {
		if _, err := q.CreateTransaction(ctx, s.materialize(r)); err != nil {
			return fmt.Errorf("recurring: materialize: %w", err)
		}
	}

	next := domain.AdvanceDate(r.NextDue, r.StartDate, domain.Cadence(r.Cadence), int(r.IntervalN))
	if _, err := q.UpdateRecurringNextDue(ctx, store.UpdateRecurringNextDueParams{ID: id, NextDue: next}); err != nil {
		return fmt.Errorf("recurring: advance cursor: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("recurring: commit: %w", err)
	}
	return nil
}

// materialize builds the ledger row for a template's current occurrence, dated at
// the occurrence (next_due). The one-row shape mirrors the ledger (AD-9): income
// credits to_account (magnitude in the to-leg), expense debits from_account (in
// the from-leg), and a transfer populates both (to_amount is the dest leg).
func (s *Service) materialize(r store.Recurring) store.CreateTransactionParams {
	fromAmt, toAmt := decimal.Zero, decimal.Zero
	switch r.Type {
	case Income:
		toAmt = r.Amount
	case Expense:
		fromAmt = r.Amount
	case Transfer:
		fromAmt, toAmt = r.Amount, r.ToAmount
	}
	return store.CreateTransactionParams{
		Type:          r.Type,
		FromAccountID: r.FromAccountID,
		ToAccountID:   r.ToAccountID,
		FromAmount:    fromAmt,
		ToAmount:      toAmt,
		OccurredOn:    r.NextDue,
		Description:   r.Description,
		CategoryID:    r.CategoryID,
		SecurityID:    pgtype.Int8{},
		Quantity:      decimal.Zero,
		Price:         decimal.Zero,
		Fees:          decimal.Zero,
	}
}

// resolved holds the validated, storage-ready fields shared by Create and Edit.
type resolved struct {
	typ      string
	from, to pgtype.Int8
	amount   decimal.Decimal
	toAmount decimal.Decimal
	category pgtype.Int8
	cadence  string
	interval int
	start    time.Time
	end      *time.Time
}

// resolve validates an Input and computes the ledger legs, destination amount,
// and category. It is the single validation authority for Create and Edit.
func (s *Service) resolve(ctx context.Context, in Input) (resolved, error) {
	if in.Type != Income && in.Type != Expense && in.Type != Transfer {
		return resolved{}, ErrInvalidType
	}
	if !in.Amount.IsPositive() {
		return resolved{}, ErrNonPositiveAmount
	}
	if !domain.Cadence(in.Cadence).IsValid() {
		return resolved{}, ErrInvalidCadence
	}
	if in.IntervalN < 1 {
		return resolved{}, ErrNonPositiveInterval
	}
	if in.StartDate.IsZero() {
		return resolved{}, ErrMissingStartDate
	}
	if in.EndDate != nil && in.EndDate.Before(in.StartDate) {
		return resolved{}, ErrInvalidDateRange
	}

	out := resolved{
		typ:      in.Type,
		amount:   in.Amount,
		cadence:  in.Cadence,
		interval: in.IntervalN,
		start:    dayOnly(in.StartDate),
	}
	if in.EndDate != nil {
		e := dayOnly(*in.EndDate)
		out.end = &e
	}

	q := store.New(s.pool)
	switch in.Type {
	case Income, Expense:
		acct, err := s.requireCashOrCredit(ctx, q, in.AccountID)
		if err != nil {
			return resolved{}, err
		}
		if in.Type == Income {
			out.to = idParam(acct.ID)
		} else {
			out.from = idParam(acct.ID)
		}
		cat, err := s.resolveCategory(ctx, q, in.CategoryID, in.Type)
		if err != nil {
			return resolved{}, err
		}
		out.category = cat
	case Transfer:
		if in.CategoryID != 0 {
			return resolved{}, ErrCategoryOnTransfer
		}
		if in.FromAccountID == in.ToAccountID {
			return resolved{}, ErrSameAccount
		}
		from, err := requireAccount(ctx, q, in.FromAccountID)
		if err != nil {
			return resolved{}, err
		}
		to, err := requireAccount(ctx, q, in.ToAccountID)
		if err != nil {
			return resolved{}, err
		}
		out.from, out.to = idParam(from.ID), idParam(to.ID)
		// Resolve the destination leg by currency (AD-9), mirroring Transfer.
		if from.Currency == to.Currency {
			if in.ToAmount.IsPositive() && !in.ToAmount.Equal(in.Amount) {
				return resolved{}, ErrSameCurrencyAmountMismatch
			}
			out.toAmount = in.Amount
		} else {
			if !in.ToAmount.IsPositive() {
				return resolved{}, ErrToAmountRequired
			}
			out.toAmount = in.ToAmount
		}
	}
	return out, nil
}

// requireCashOrCredit loads an account and enforces the income/expense account
// type rule.
func (s *Service) requireCashOrCredit(ctx context.Context, q *store.Queries, id int64) (store.Account, error) {
	acct, err := requireAccount(ctx, q, id)
	if err != nil {
		return store.Account{}, err
	}
	if acct.Type != "cash" && acct.Type != "credit" {
		return store.Account{}, ErrUnsupportedAccountType
	}
	return acct, nil
}

// requireAccount loads an account or returns ErrAccountNotFound.
func requireAccount(ctx context.Context, q *store.Queries, id int64) (store.Account, error) {
	acct, err := q.GetAccount(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Account{}, ErrAccountNotFound
	}
	if err != nil {
		return store.Account{}, fmt.Errorf("recurring: get account: %w", err)
	}
	return acct, nil
}

// resolveCategory validates an optional category (0 = none): it must exist and
// its kind must match the transaction type.
func (s *Service) resolveCategory(ctx context.Context, q *store.Queries, categoryID int64, typ string) (pgtype.Int8, error) {
	if categoryID == 0 {
		return pgtype.Int8{}, nil
	}
	cat, err := q.GetCategory(ctx, categoryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return pgtype.Int8{}, ErrCategoryNotFound
	}
	if err != nil {
		return pgtype.Int8{}, fmt.Errorf("recurring: get category: %w", err)
	}
	if cat.Kind != typ {
		return pgtype.Int8{}, ErrCategoryKindMismatch
	}
	return idParam(categoryID), nil
}

// toRecurring maps a joined store row to the display shape, deriving Due against
// `today` (AD-10). The primary Amount currency follows the money leg: income →
// destination, expense → source, transfer → source (with ToAmount the dest leg).
func toRecurring(r store.ListRecurringRow, today time.Time) Recurring {
	end := datePtr(r.EndDate)
	out := Recurring{
		ID:            r.ID,
		Type:          r.Type,
		FromAccountID: nullID(r.FromAccountID),
		FromAccount:   r.FromAccountName.String,
		ToAccountID:   nullID(r.ToAccountID),
		ToAccount:     r.ToAccountName.String,
		CategoryID:    nullID(r.CategoryID),
		CategoryName:  r.CategoryName.String,
		Cadence:       r.Cadence,
		IntervalN:     int(r.IntervalN),
		StartDate:     r.StartDate,
		EndDate:       end,
		NextDue:       r.NextDue,
		Description:   r.Description,
		Due:           domain.IsDue(r.NextDue, end, today),
	}
	switch r.Type {
	case Income:
		out.Amount = money.New(r.Amount, money.Currency(r.ToCurrency.String))
	case Expense:
		out.Amount = money.New(r.Amount, money.Currency(r.FromCurrency.String))
	case Transfer:
		out.Amount = money.New(r.Amount, money.Currency(r.FromCurrency.String))
		if r.FromCurrency.String != r.ToCurrency.String {
			out.CrossCurrency = true
			out.ToAmount = money.New(r.ToAmount, money.Currency(r.ToCurrency.String))
		}
	}
	return out
}

// dayOnly strips any time-of-day, keeping the calendar date in UTC.
func dayOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// sameDay reports whether two times fall on the same calendar day.
func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// idParam wraps a non-zero id as a valid pgtype.Int8.
func idParam(id int64) pgtype.Int8 { return pgtype.Int8{Int64: id, Valid: true} }

// nullID unwraps a nullable id to int64 (0 when NULL).
func nullID(v pgtype.Int8) int64 {
	if v.Valid {
		return v.Int64
	}
	return 0
}

// dateParam wraps an optional calendar date as a pgtype.Date (NULL when nil).
func dateParam(t *time.Time) pgtype.Date {
	if t == nil {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: *t, Valid: true}
}

// datePtr unwraps a nullable pgtype.Date to *time.Time (nil when NULL).
func datePtr(d pgtype.Date) *time.Time {
	if !d.Valid {
		return nil
	}
	t := d.Time
	return &t
}
