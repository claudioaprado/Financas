package importer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/internal/store"
)

// Errors.
var (
	ErrAccountNotFound        = errors.New("importer: account not found")
	ErrUnsupportedAccountType = errors.New("importer: import requires a cash or credit account")
)

// PreviewRow is a parsed row plus its dedup status against the account.
type PreviewRow struct {
	ParsedRow
	Status string // "new" | "duplicate" | "error"
}

// Result summarizes a preview or commit.
type Result struct {
	AccountName string
	Currency    string
	Rows        []PreviewRow
	New         int
	Duplicate   int
	Errors      int
}

// Service previews and commits file imports.
type Service struct {
	pool *pgxpool.Pool
}

// New returns an importer Service backed by the given pool.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// Preview parses content against the account and labels each row new/duplicate/
// error without writing anything.
func (s *Service) Preview(ctx context.Context, accountID int64, content string) (Result, error) {
	acct, err := s.account(ctx, accountID)
	if err != nil {
		return Result{}, err
	}
	existing, err := s.existingHashes(ctx, accountID)
	if err != nil {
		return Result{}, err
	}
	return classify(acct, content, existing), nil
}

// Commit parses content and inserts every new (non-duplicate, non-error) row in
// one transaction (AD-3). It returns the same Result as Preview.
func (s *Service) Commit(ctx context.Context, accountID int64, content string) (Result, error) {
	acct, err := s.account(ctx, accountID)
	if err != nil {
		return Result{}, err
	}
	existing, err := s.existingHashes(ctx, accountID)
	if err != nil {
		return Result{}, err
	}
	res := classify(acct, content, existing)
	if res.New == 0 {
		return res, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("importer: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := store.New(tx)
	for _, r := range res.Rows {
		if r.Status != "new" {
			continue
		}
		from, to, fromAmt, toAmt := legs(accountID, r.Type, r.Amount)
		if _, err := q.CreateImportedTransaction(ctx, store.CreateImportedTransactionParams{
			Type:          r.Type,
			FromAccountID: from,
			ToAccountID:   to,
			FromAmount:    fromAmt,
			ToAmount:      toAmt,
			OccurredOn:    r.Date,
			Description:   r.Description,
			ImportHash:    pgtype.Text{String: rowHash(acct.ID, r.ParsedRow), Valid: true},
		}); err != nil {
			return Result{}, fmt.Errorf("importer: insert line %d: %w", r.Line, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("importer: commit: %w", err)
	}
	return res, nil
}

func (s *Service) account(ctx context.Context, accountID int64) (store.Account, error) {
	acct, err := store.New(s.pool).GetAccount(ctx, accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Account{}, ErrAccountNotFound
	}
	if err != nil {
		return store.Account{}, fmt.Errorf("importer: get account: %w", err)
	}
	if acct.Type != "cash" && acct.Type != "credit" {
		return store.Account{}, ErrUnsupportedAccountType
	}
	return acct, nil
}

func (s *Service) existingHashes(ctx context.Context, accountID int64) (map[string]bool, error) {
	rows, err := store.New(s.pool).ListAccountImportHashes(ctx, pgtype.Int8{Int64: accountID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("importer: list hashes: %w", err)
	}
	set := make(map[string]bool, len(rows))
	for _, h := range rows {
		if h.Valid {
			set[h.String] = true
		}
	}
	return set, nil
}

// classify parses content and labels each row, deduping against existing hashes
// and within the batch itself.
func classify(acct store.Account, content string, existing map[string]bool) Result {
	res := Result{AccountName: acct.Name, Currency: acct.Currency}
	seen := make(map[string]bool)
	for _, p := range Parse(content) {
		row := PreviewRow{ParsedRow: p}
		switch {
		case !p.OK:
			row.Status = "error"
			res.Errors++
		default:
			h := rowHash(acct.ID, p)
			if existing[h] || seen[h] {
				row.Status = "duplicate"
				res.Duplicate++
			} else {
				row.Status = "new"
				res.New++
				seen[h] = true
			}
		}
		res.Rows = append(res.Rows, row)
	}
	return res
}

// rowHash is the stored per-row natural key over the dedup tuple
// (account_id, date, description, signed value).
func rowHash(accountID int64, p ParsedRow) string {
	signed := p.Amount
	if p.Type == typeExpense {
		signed = p.Amount.Neg()
	}
	key := fmt.Sprintf("%d\x00%s\x00%s\x00%s", accountID, p.Date.Format("2006-01-02"), p.Description, signed.String())
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// legs maps an imported income/expense to the one-row from/to shape (AD-9):
// income credits (to-side), expense debits (from-side).
func legs(accountID int64, typ string, amount decimal.Decimal) (from, to pgtype.Int8, fromAmt, toAmt decimal.Decimal) {
	id := pgtype.Int8{Int64: accountID, Valid: true}
	if typ == typeIncome {
		return pgtype.Int8{}, id, decimal.Zero, amount
	}
	return id, pgtype.Int8{}, amount, decimal.Zero
}
