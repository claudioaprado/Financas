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

// PreviewRow is a parsed row plus its dedup status against the account. Warning
// is a non-fatal note (currently: an OFX row with no FITID, imported as new but
// re-importable) — Status stays "new".
type PreviewRow struct {
	ParsedRow
	Status              string // "new" | "duplicate" | "error"
	Warning             string // non-empty when the row imports with a caveat (e.g. no FITID)
	SuggestedCategoryID int64  // auto-categorization suggestion for a new row (0 = none), FR-17
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
	rules, err := s.rules(ctx)
	if err != nil {
		return Result{}, err
	}
	return classify(acct, content, existing, rules), nil
}

// Commit parses content and inserts every new (non-duplicate, non-error) row in
// one transaction (AD-3). cats carries the owner's per-row category choice
// (keyed by ParsedRow.Line; absent ⇒ use the row's suggestion). It returns the
// same Result as Preview.
func (s *Service) Commit(ctx context.Context, accountID int64, content string, cats map[int]int64) (Result, error) {
	acct, err := s.account(ctx, accountID)
	if err != nil {
		return Result{}, err
	}
	existing, err := s.existingHashes(ctx, accountID)
	if err != nil {
		return Result{}, err
	}
	rules, err := s.rules(ctx)
	if err != nil {
		return Result{}, err
	}
	res := classify(acct, content, existing, rules)
	if res.New == 0 {
		return res, nil
	}
	kinds, err := s.categoryKinds(ctx)
	if err != nil {
		return Result{}, err
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
			CategoryID:    resolveCategory(r, cats, kinds),
		}); err != nil {
			return Result{}, fmt.Errorf("importer: insert line %d: %w", r.Line, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("importer: commit: %w", err)
	}
	return res, nil
}

// PreviewOFX parses an OFX statement against the account and labels each row
// new/duplicate/error without writing. Dedup is by (account, FITID) ONLY.
func (s *Service) PreviewOFX(ctx context.Context, accountID int64, content string) (Result, error) {
	acct, err := s.account(ctx, accountID)
	if err != nil {
		return Result{}, err
	}
	existing, err := s.existingFitids(ctx, accountID)
	if err != nil {
		return Result{}, err
	}
	rules, err := s.rules(ctx)
	if err != nil {
		return Result{}, err
	}
	return classifyOFX(acct, content, existing, rules), nil
}

// CommitOFX parses an OFX statement and inserts every new row in one transaction
// (AD-3). Rows already present by FITID are skipped; rows with no FITID always
// insert (fitid NULL) and are intentionally re-importable. cats carries the
// owner's per-row category choice (keyed by ParsedRow.Line).
func (s *Service) CommitOFX(ctx context.Context, accountID int64, content string, cats map[int]int64) (Result, error) {
	acct, err := s.account(ctx, accountID)
	if err != nil {
		return Result{}, err
	}
	existing, err := s.existingFitids(ctx, accountID)
	if err != nil {
		return Result{}, err
	}
	rules, err := s.rules(ctx)
	if err != nil {
		return Result{}, err
	}
	res := classifyOFX(acct, content, existing, rules)
	if res.New == 0 {
		return res, nil
	}
	kinds, err := s.categoryKinds(ctx)
	if err != nil {
		return Result{}, err
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
		if _, err := q.CreateOFXTransaction(ctx, store.CreateOFXTransactionParams{
			Type:          r.Type,
			FromAccountID: from,
			ToAccountID:   to,
			FromAmount:    fromAmt,
			ToAmount:      toAmt,
			OccurredOn:    r.Date,
			Description:   r.Description,
			Fitid:         pgtype.Text{String: r.FITID, Valid: r.FITID != ""},
			CategoryID:    resolveCategory(r, cats, kinds),
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

func (s *Service) existingFitids(ctx context.Context, accountID int64) (map[string]bool, error) {
	rows, err := store.New(s.pool).ListAccountFitids(ctx, pgtype.Int8{Int64: accountID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("importer: list fitids: %w", err)
	}
	set := make(map[string]bool, len(rows))
	for _, f := range rows {
		if f.Valid {
			set[f.String] = true
		}
	}
	return set, nil
}

// classifyOFX parses an OFX statement and labels each row, deduping by FITID ONLY
// (against existing account FITIDs and within the batch). A row with no FITID is
// always "new" with a warning — NEVER content-deduped, because two legitimate
// transactions can share the same date/description/value.
func classifyOFX(acct store.Account, content string, existing map[string]bool, rules []Rule) Result {
	res := Result{AccountName: acct.Name, Currency: acct.Currency}
	seen := make(map[string]bool)
	for _, p := range ParseOFX(content) {
		row := PreviewRow{ParsedRow: p}
		switch {
		case !p.OK:
			row.Status = "error"
			res.Errors++
		case p.FITID == "":
			row.Status = "new"
			row.Warning = "no FITID — re-importing may duplicate"
			row.SuggestedCategoryID = SuggestCategory(p.Description, p.Type, rules)
			res.New++
		case existing[p.FITID] || seen[p.FITID]:
			row.Status = "duplicate"
			res.Duplicate++
		default:
			row.Status = "new"
			row.SuggestedCategoryID = SuggestCategory(p.Description, p.Type, rules)
			res.New++
			seen[p.FITID] = true
		}
		res.Rows = append(res.Rows, row)
	}
	return res
}

// classify parses content and labels each row, deduping against existing hashes
// and within the batch itself.
func classify(acct store.Account, content string, existing map[string]bool, rules []Rule) Result {
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
				row.SuggestedCategoryID = SuggestCategory(p.Description, p.Type, rules)
				res.New++
				seen[h] = true
			}
		}
		res.Rows = append(res.Rows, row)
	}
	return res
}

// rules loads the auto-categorization rules (id order ⇒ first-match-wins) for
// suggestion during preview/commit. FR-17.
func (s *Service) rules(ctx context.Context) ([]Rule, error) {
	rows, err := store.New(s.pool).ListCategoryRules(ctx)
	if err != nil {
		return nil, fmt.Errorf("importer: list rules: %w", err)
	}
	out := make([]Rule, len(rows))
	for i, r := range rows {
		out[i] = Rule{MatchText: r.MatchText, CategoryID: r.CategoryID, Kind: r.CategoryKind}
	}
	return out, nil
}

// categoryKinds returns id→kind ("income"/"expense") for every category, used to
// validate that a chosen category matches the row's type before committing it.
func (s *Service) categoryKinds(ctx context.Context) (map[int64]string, error) {
	rows, err := store.New(s.pool).ListCategories(ctx)
	if err != nil {
		return nil, fmt.Errorf("importer: list categories: %w", err)
	}
	kinds := make(map[int64]string, len(rows))
	for _, c := range rows {
		kinds[c.ID] = c.Kind
	}
	return kinds, nil
}

// resolveCategory picks the category to write for a new row: the owner's explicit
// choice (cats keyed by Line, present even when 0 ⇒ intentionally uncategorized),
// else the row's suggestion. The chosen id must exist and match the row's kind,
// else the row is imported uncategorized (defensive; the UI already prevents a
// mismatch). Honors the category kind rule.
func resolveCategory(r PreviewRow, cats map[int]int64, kinds map[int64]string) pgtype.Int8 {
	id := r.SuggestedCategoryID
	if c, ok := cats[r.Line]; ok {
		id = c
	}
	if id == 0 || kinds[id] != r.Type {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: id, Valid: true}
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
