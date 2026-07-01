// Package backup is the use-case for exporting the owner's AUTHORED data to a
// single, self-describing, re-importable file (Story 6.1, FR-15). It reads only
// authored state — accounts, categories, securities, exchange rates, prices,
// transactions, the display-currency setting, and the Phase-2 authored tables
// (budgets, auto-categorization rules, recurring templates, tags + their links)
// — never derived figures (holdings, balances, valuation, net worth, budget
// carryover, recurrence "due" state), which are recomputed on read (AD-2). It
// assembles a versioned, JSON-ready value; the http layer serializes and streams
// it (AD-1). Decimal amounts are carried as strings, never floats (AD-4/NFR-5).
// All tables are read inside one transaction so the snapshot is internally
// consistent (NFR-2). New table groups are ADDITIVE and optional on restore
// (absent in an older file ⇒ that table simply restores empty), so ExportVersion
// stays 1 and pre-Phase-2 backups still restore.
package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/internal/money"
	"github.com/claudioaprado/financas/internal/store"
)

// Export schema identity. ExportVersion is the contract restore (6.2) validates:
// bump it whenever the export shape changes so an older restore refuses a newer
// file rather than misreading it.
const (
	ExportSchema  = "financas.export"
	ExportVersion = 1
)

// dateLayout is the calendar-date format for occurred_on / effective_date.
const dateLayout = "2006-01-02"

// Export is the full authored-data snapshot, JSON-ready and versioned. It is the
// canonical contract consumed by restore (6.2). Decimal fields live in the DTOs
// as strings (NFR-5); slices are always non-nil so an empty instance emits [].
type Export struct {
	Schema          string            `json:"schema"`           // == ExportSchema
	Version         int               `json:"version"`          // == ExportVersion
	ExportedAt      string            `json:"exported_at"`      // RFC3339 UTC, informational (ignored on restore)
	DisplayCurrency string            `json:"display_currency"` // app_settings singleton
	Accounts        []AccountDTO      `json:"accounts"`
	Categories      []CategoryDTO     `json:"categories"`
	Securities      []SecurityDTO     `json:"securities"`
	ExchangeRates   []ExchangeRateDTO `json:"exchange_rates"`
	Prices          []PriceDTO        `json:"prices"`
	Transactions    []TransactionDTO  `json:"transactions"`
	// Phase-2 authored tables (Epics 8-10). Absent in pre-Phase-2 exports ⇒ empty
	// on restore, which correctly leaves those tables empty.
	Budgets         []BudgetDTO         `json:"budgets"`
	CategoryRules   []CategoryRuleDTO   `json:"category_rules"`
	Recurring       []RecurringDTO      `json:"recurring"`
	Tags            []TagDTO            `json:"tags"`
	TransactionTags []TransactionTagDTO `json:"transaction_tags"`
}

// AccountDTO mirrors the account table at full fidelity (PK + created_at).
type AccountDTO struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Currency  string `json:"currency"`
	Archived  bool   `json:"archived"`
	CreatedAt string `json:"created_at,omitempty"`
}

// CategoryDTO mirrors the category table.
type CategoryDTO struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	CreatedAt string `json:"created_at,omitempty"`
}

// SecurityDTO mirrors the security table.
type SecurityDTO struct {
	ID            int64  `json:"id"`
	Symbol        string `json:"symbol"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	QuoteCurrency string `json:"quote_currency"`
	CreatedAt     string `json:"created_at,omitempty"`
}

// ExchangeRateDTO mirrors the exchange_rate table. Rate is a decimal string.
type ExchangeRateDTO struct {
	ID            int64  `json:"id"`
	FromCurrency  string `json:"from_currency"`
	ToCurrency    string `json:"to_currency"`
	EffectiveDate string `json:"effective_date"` // YYYY-MM-DD
	Rate          string `json:"rate"`           // decimal string (NFR-5)
	CreatedAt     string `json:"created_at,omitempty"`
}

// PriceDTO mirrors the price table. Price is a decimal string.
type PriceDTO struct {
	ID            int64  `json:"id"`
	SecurityID    int64  `json:"security_id"`
	EffectiveDate string `json:"effective_date"` // YYYY-MM-DD
	Price         string `json:"price"`          // decimal string (NFR-5)
	CreatedAt     string `json:"created_at,omitempty"`
}

// TransactionDTO mirrors the transaction ledger at full fidelity. Nullable FKs
// and import_hash are pointers (JSON null/absent ⇄ SQL NULL); the four money/
// quantity columns are decimal strings (NFR-5).
type TransactionDTO struct {
	ID            int64   `json:"id"`
	Type          string  `json:"type"`
	FromAccountID *int64  `json:"from_account_id,omitempty"`
	ToAccountID   *int64  `json:"to_account_id,omitempty"`
	FromAmount    string  `json:"from_amount"`
	ToAmount      string  `json:"to_amount"`
	OccurredOn    string  `json:"occurred_on"` // YYYY-MM-DD
	Description   string  `json:"description"`
	CreatedAt     string  `json:"created_at,omitempty"`
	CategoryID    *int64  `json:"category_id,omitempty"`
	ImportHash    *string `json:"import_hash,omitempty"`
	SecurityID    *int64  `json:"security_id,omitempty"`
	Quantity      string  `json:"quantity"`
	Price         string  `json:"price"`
	Fees          string  `json:"fees"`
	Fitid         *string `json:"fitid,omitempty"` // OFX dedup key; absent in pre-7.1 exports ⇒ NULL
	Note          string  `json:"note,omitempty"`  // free-text annotation (Story 10.2); absent in pre-10.2 exports ⇒ ""
}

// BudgetDTO mirrors the budget table (Story 8.1). Amount is a decimal string.
type BudgetDTO struct {
	ID         int64  `json:"id"`
	CategoryID int64  `json:"category_id"`
	Amount     string `json:"amount"`
	CreatedAt  string `json:"created_at,omitempty"`
}

// CategoryRuleDTO mirrors the category_rule table (Story 7.2).
type CategoryRuleDTO struct {
	ID         int64  `json:"id"`
	MatchText  string `json:"match_text"`
	CategoryID int64  `json:"category_id"`
	CreatedAt  string `json:"created_at,omitempty"`
}

// RecurringDTO mirrors the recurring template table (Epic 9). Nullable FKs and the
// end date are pointers; amounts are decimal strings; dates are YYYY-MM-DD.
type RecurringDTO struct {
	ID            int64   `json:"id"`
	Type          string  `json:"type"`
	FromAccountID *int64  `json:"from_account_id,omitempty"`
	ToAccountID   *int64  `json:"to_account_id,omitempty"`
	Amount        string  `json:"amount"`
	ToAmount      string  `json:"to_amount"`
	CategoryID    *int64  `json:"category_id,omitempty"`
	Cadence       string  `json:"cadence"`
	IntervalN     int32   `json:"interval_n"`
	StartDate     string  `json:"start_date"` // YYYY-MM-DD
	EndDate       *string `json:"end_date,omitempty"`
	NextDue       string  `json:"next_due"` // YYYY-MM-DD
	Description   string  `json:"description"`
	CreatedAt     string  `json:"created_at,omitempty"`
}

// TagDTO mirrors the tag table (Story 10.2).
type TagDTO struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at,omitempty"`
}

// TransactionTagDTO mirrors the transaction_tag join (Story 10.2).
type TransactionTagDTO struct {
	TransactionID int64 `json:"transaction_id"`
	TagID         int64 `json:"tag_id"`
}

// Service assembles the authored-data export.
type Service struct {
	pool *pgxpool.Pool
}

// New returns a backup Service backed by the given pool.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// Export reads every authored table inside one read transaction (a consistent
// point-in-time snapshot, NFR-2/AD-3) and returns a versioned, JSON-ready
// Export. It performs no derivation — only authored rows are read (AD-2).
func (s *Service) Export(ctx context.Context) (Export, error) {
	// A repeatable-read, read-only transaction gives every read in this method one
	// consistent snapshot (NFR-2/AD-3) — under the default READ COMMITTED each
	// statement would snapshot independently, so a concurrent write between reads
	// could tear the export (e.g. a transaction row referencing an account absent
	// from the file).
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Export{}, fmt.Errorf("backup: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := store.New(tx)

	display, err := q.GetDisplayCurrency(ctx)
	if err != nil {
		return Export{}, fmt.Errorf("backup: display currency: %w", err)
	}

	exp := Export{
		Schema:          ExportSchema,
		Version:         ExportVersion,
		ExportedAt:      time.Now().UTC().Format(time.RFC3339),
		DisplayCurrency: display,
		Accounts:        []AccountDTO{},
		Categories:      []CategoryDTO{},
		Securities:      []SecurityDTO{},
		ExchangeRates:   []ExchangeRateDTO{},
		Prices:          []PriceDTO{},
		Transactions:    []TransactionDTO{},
		Budgets:         []BudgetDTO{},
		CategoryRules:   []CategoryRuleDTO{},
		Recurring:       []RecurringDTO{},
		Tags:            []TagDTO{},
		TransactionTags: []TransactionTagDTO{},
	}

	accounts, err := q.ExportAccounts(ctx)
	if err != nil {
		return Export{}, fmt.Errorf("backup: accounts: %w", err)
	}
	for _, a := range accounts {
		exp.Accounts = append(exp.Accounts, AccountDTO{
			ID:        a.ID,
			Name:      a.Name,
			Type:      a.Type,
			Currency:  a.Currency,
			Archived:  a.Archived,
			CreatedAt: timestamp(a.CreatedAt),
		})
	}

	categories, err := q.ExportCategories(ctx)
	if err != nil {
		return Export{}, fmt.Errorf("backup: categories: %w", err)
	}
	for _, c := range categories {
		exp.Categories = append(exp.Categories, CategoryDTO{
			ID:        c.ID,
			Name:      c.Name,
			Kind:      c.Kind,
			CreatedAt: timestamp(c.CreatedAt),
		})
	}

	securities, err := q.ExportSecurities(ctx)
	if err != nil {
		return Export{}, fmt.Errorf("backup: securities: %w", err)
	}
	for _, sec := range securities {
		exp.Securities = append(exp.Securities, SecurityDTO{
			ID:            sec.ID,
			Symbol:        sec.Symbol,
			Name:          sec.Name,
			Type:          sec.Type,
			QuoteCurrency: sec.QuoteCurrency,
			CreatedAt:     timestamp(sec.CreatedAt),
		})
	}

	rates, err := q.ExportExchangeRates(ctx)
	if err != nil {
		return Export{}, fmt.Errorf("backup: exchange rates: %w", err)
	}
	for _, r := range rates {
		exp.ExchangeRates = append(exp.ExchangeRates, ExchangeRateDTO{
			ID:            r.ID,
			FromCurrency:  r.FromCurrency,
			ToCurrency:    r.ToCurrency,
			EffectiveDate: r.EffectiveDate.Format(dateLayout),
			Rate:          r.Rate.String(),
			CreatedAt:     timestamp(r.CreatedAt),
		})
	}

	prices, err := q.ExportPrices(ctx)
	if err != nil {
		return Export{}, fmt.Errorf("backup: prices: %w", err)
	}
	for _, p := range prices {
		exp.Prices = append(exp.Prices, PriceDTO{
			ID:            p.ID,
			SecurityID:    p.SecurityID,
			EffectiveDate: p.EffectiveDate.Format(dateLayout),
			Price:         p.Price.String(),
			CreatedAt:     timestamp(p.CreatedAt),
		})
	}

	transactions, err := q.ExportTransactions(ctx)
	if err != nil {
		return Export{}, fmt.Errorf("backup: transactions: %w", err)
	}
	for _, t := range transactions {
		exp.Transactions = append(exp.Transactions, TransactionDTO{
			ID:            t.ID,
			Type:          t.Type,
			FromAccountID: intPtr(t.FromAccountID),
			ToAccountID:   intPtr(t.ToAccountID),
			FromAmount:    t.FromAmount.String(),
			ToAmount:      t.ToAmount.String(),
			OccurredOn:    t.OccurredOn.Format(dateLayout),
			Description:   t.Description,
			CreatedAt:     timestamp(t.CreatedAt),
			CategoryID:    intPtr(t.CategoryID),
			ImportHash:    textPtr(t.ImportHash),
			SecurityID:    intPtr(t.SecurityID),
			Quantity:      t.Quantity.String(),
			Price:         t.Price.String(),
			Fees:          t.Fees.String(),
			Fitid:         textPtr(t.Fitid),
			Note:          t.Note,
		})
	}

	budgets, err := q.ExportBudgets(ctx)
	if err != nil {
		return Export{}, fmt.Errorf("backup: budgets: %w", err)
	}
	for _, b := range budgets {
		exp.Budgets = append(exp.Budgets, BudgetDTO{
			ID: b.ID, CategoryID: b.CategoryID, Amount: b.Amount.String(), CreatedAt: timestamp(b.CreatedAt),
		})
	}

	rules, err := q.ExportCategoryRules(ctx)
	if err != nil {
		return Export{}, fmt.Errorf("backup: category rules: %w", err)
	}
	for _, r := range rules {
		exp.CategoryRules = append(exp.CategoryRules, CategoryRuleDTO{
			ID: r.ID, MatchText: r.MatchText, CategoryID: r.CategoryID, CreatedAt: timestamp(r.CreatedAt),
		})
	}

	recurring, err := q.ExportRecurring(ctx)
	if err != nil {
		return Export{}, fmt.Errorf("backup: recurring: %w", err)
	}
	for _, r := range recurring {
		exp.Recurring = append(exp.Recurring, RecurringDTO{
			ID: r.ID, Type: r.Type,
			FromAccountID: intPtr(r.FromAccountID), ToAccountID: intPtr(r.ToAccountID),
			Amount: r.Amount.String(), ToAmount: r.ToAmount.String(),
			CategoryID: intPtr(r.CategoryID), Cadence: r.Cadence, IntervalN: r.IntervalN,
			StartDate: r.StartDate.Format(dateLayout), EndDate: datePtr(r.EndDate),
			NextDue: r.NextDue.Format(dateLayout), Description: r.Description, CreatedAt: timestamp(r.CreatedAt),
		})
	}

	tags, err := q.ExportTags(ctx)
	if err != nil {
		return Export{}, fmt.Errorf("backup: tags: %w", err)
	}
	for _, t := range tags {
		exp.Tags = append(exp.Tags, TagDTO{ID: t.ID, Name: t.Name, CreatedAt: timestamp(t.CreatedAt)})
	}

	txnTags, err := q.ExportTransactionTags(ctx)
	if err != nil {
		return Export{}, fmt.Errorf("backup: transaction tags: %w", err)
	}
	for _, tt := range txnTags {
		exp.TransactionTags = append(exp.TransactionTags, TransactionTagDTO{TransactionID: tt.TransactionID, TagID: tt.TagID})
	}

	if err := tx.Commit(ctx); err != nil {
		return Export{}, fmt.Errorf("backup: commit: %w", err)
	}
	return exp, nil
}

// datePtr renders a nullable pgtype.Date as YYYY-MM-DD, or nil when NULL.
func datePtr(d pgtype.Date) *string {
	if !d.Valid {
		return nil
	}
	s := d.Time.Format(dateLayout)
	return &s
}

// timestamp renders a nullable created_at as RFC3339Nano UTC, or "" when NULL.
func timestamp(ts pgtype.Timestamptz) string {
	if !ts.Valid {
		return ""
	}
	return ts.Time.UTC().Format(time.RFC3339Nano)
}

// intPtr maps a nullable BIGINT to *int64 (nil when SQL NULL).
func intPtr(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

// textPtr maps a nullable TEXT to *string (nil when SQL NULL).
func textPtr(v pgtype.Text) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

// Restore sentinel errors. The http layer maps these to a precise 400 message
// without inspecting the file itself (AD-1).
var (
	// ErrMalformed marks a file that isn't valid JSON, isn't a Financas export,
	// has an unparseable field, or fails a database constraint on insert.
	ErrMalformed = errors.New("backup: file is not a valid Financas export")
	// ErrUnsupportedSchema marks a JSON document whose schema tag isn't ours.
	ErrUnsupportedSchema = errors.New("backup: unrecognized export schema")
	// ErrUnsupportedVersion marks an export from an incompatible version.
	ErrUnsupportedVersion = errors.New("backup: unsupported export version")
)

// RestoreSummary reports how many authored rows were restored, per table.
type RestoreSummary struct {
	Accounts        int
	Categories      int
	Securities      int
	ExchangeRates   int
	Prices          int
	Transactions    int
	Budgets         int
	CategoryRules   int
	Recurring       int
	Tags            int
	TransactionTags int
}

// Restore rebuilds the instance from a 6.1 export (FR-15). It is a replace-all
// recovery inside ONE transaction (AD-3): it deletes every authored row and
// re-inserts the file's rows preserving primary keys and created_at (identity
// insert), then resets the identity sequences and the display-currency setting.
// Only AUTHORED data is written — balances, holdings, valuation and net worth
// recompute on read and reproduce the source instance (NFR-2/AD-2).
//
// It is atomic: a bad file (unparseable, wrong schema/version, or a referential
// violation caught by the database) rolls everything back and leaves the
// instance unchanged. All field parsing happens BEFORE the transaction begins,
// so a malformed amount/date rejects without touching the database.
func (s *Service) Restore(ctx context.Context, raw []byte) (RestoreSummary, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return RestoreSummary{}, fmt.Errorf("%w: the file is empty", ErrMalformed)
	}
	var exp Export
	if err := json.Unmarshal(raw, &exp); err != nil {
		return RestoreSummary{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if exp.Schema != ExportSchema {
		return RestoreSummary{}, fmt.Errorf("%w: %q", ErrUnsupportedSchema, exp.Schema)
	}
	if exp.Version != ExportVersion {
		return RestoreSummary{}, fmt.Errorf("%w: file is version %d, this app reads version %d", ErrUnsupportedVersion, exp.Version, ExportVersion)
	}
	if !money.IsSupported(money.Currency(exp.DisplayCurrency)) {
		return RestoreSummary{}, fmt.Errorf("%w: unsupported display currency %q", ErrMalformed, exp.DisplayCurrency)
	}

	// Build every row's params up front so a parse error rejects the whole file
	// before any database mutation (NFR-5: decimals via decimal.NewFromString,
	// never a float).
	accounts := make([]store.RestoreInsertAccountParams, 0, len(exp.Accounts))
	for _, a := range exp.Accounts {
		ca, err := toTimestamp(a.CreatedAt)
		if err != nil {
			return RestoreSummary{}, err
		}
		accounts = append(accounts, store.RestoreInsertAccountParams{
			ID: a.ID, Name: a.Name, Type: a.Type, Currency: a.Currency, Archived: a.Archived, CreatedAt: ca,
		})
	}
	categories := make([]store.RestoreInsertCategoryParams, 0, len(exp.Categories))
	for _, c := range exp.Categories {
		ca, err := toTimestamp(c.CreatedAt)
		if err != nil {
			return RestoreSummary{}, err
		}
		categories = append(categories, store.RestoreInsertCategoryParams{
			ID: c.ID, Name: c.Name, Kind: c.Kind, CreatedAt: ca,
		})
	}
	securities := make([]store.RestoreInsertSecurityParams, 0, len(exp.Securities))
	for _, sec := range exp.Securities {
		ca, err := toTimestamp(sec.CreatedAt)
		if err != nil {
			return RestoreSummary{}, err
		}
		securities = append(securities, store.RestoreInsertSecurityParams{
			ID: sec.ID, Symbol: sec.Symbol, Name: sec.Name, Type: sec.Type, QuoteCurrency: sec.QuoteCurrency, CreatedAt: ca,
		})
	}
	rates := make([]store.RestoreInsertExchangeRateParams, 0, len(exp.ExchangeRates))
	for _, r := range exp.ExchangeRates {
		rate, err := parseDecimal("exchange_rate.rate", r.Rate)
		if err != nil {
			return RestoreSummary{}, err
		}
		eff, err := parseDay("exchange_rate.effective_date", r.EffectiveDate)
		if err != nil {
			return RestoreSummary{}, err
		}
		ca, err := toTimestamp(r.CreatedAt)
		if err != nil {
			return RestoreSummary{}, err
		}
		rates = append(rates, store.RestoreInsertExchangeRateParams{
			ID: r.ID, FromCurrency: r.FromCurrency, ToCurrency: r.ToCurrency, EffectiveDate: eff, Rate: rate, CreatedAt: ca,
		})
	}
	prices := make([]store.RestoreInsertPriceParams, 0, len(exp.Prices))
	for _, p := range exp.Prices {
		pr, err := parseDecimal("price.price", p.Price)
		if err != nil {
			return RestoreSummary{}, err
		}
		eff, err := parseDay("price.effective_date", p.EffectiveDate)
		if err != nil {
			return RestoreSummary{}, err
		}
		ca, err := toTimestamp(p.CreatedAt)
		if err != nil {
			return RestoreSummary{}, err
		}
		prices = append(prices, store.RestoreInsertPriceParams{
			ID: p.ID, SecurityID: p.SecurityID, EffectiveDate: eff, Price: pr, CreatedAt: ca,
		})
	}
	transactions := make([]store.RestoreInsertTransactionParams, 0, len(exp.Transactions))
	for _, t := range exp.Transactions {
		fromAmt, err := parseDecimal("transaction.from_amount", t.FromAmount)
		if err != nil {
			return RestoreSummary{}, err
		}
		toAmt, err := parseDecimal("transaction.to_amount", t.ToAmount)
		if err != nil {
			return RestoreSummary{}, err
		}
		qty, err := parseDecimal("transaction.quantity", t.Quantity)
		if err != nil {
			return RestoreSummary{}, err
		}
		price, err := parseDecimal("transaction.price", t.Price)
		if err != nil {
			return RestoreSummary{}, err
		}
		fees, err := parseDecimal("transaction.fees", t.Fees)
		if err != nil {
			return RestoreSummary{}, err
		}
		occ, err := parseDay("transaction.occurred_on", t.OccurredOn)
		if err != nil {
			return RestoreSummary{}, err
		}
		ca, err := toTimestamp(t.CreatedAt)
		if err != nil {
			return RestoreSummary{}, err
		}
		transactions = append(transactions, store.RestoreInsertTransactionParams{
			ID: t.ID, Type: t.Type,
			FromAccountID: toInt8(t.FromAccountID), ToAccountID: toInt8(t.ToAccountID),
			FromAmount: fromAmt, ToAmount: toAmt, OccurredOn: occ, Description: t.Description, CreatedAt: ca,
			CategoryID: toInt8(t.CategoryID), ImportHash: toText(t.ImportHash), SecurityID: toInt8(t.SecurityID),
			Quantity: qty, Price: price, Fees: fees, Fitid: toText(t.Fitid), Note: t.Note,
		})
	}
	budgets := make([]store.RestoreInsertBudgetParams, 0, len(exp.Budgets))
	for _, b := range exp.Budgets {
		amt, err := parseDecimal("budget.amount", b.Amount)
		if err != nil {
			return RestoreSummary{}, err
		}
		ca, err := toTimestamp(b.CreatedAt)
		if err != nil {
			return RestoreSummary{}, err
		}
		budgets = append(budgets, store.RestoreInsertBudgetParams{ID: b.ID, CategoryID: b.CategoryID, Amount: amt, CreatedAt: ca})
	}
	rules := make([]store.RestoreInsertCategoryRuleParams, 0, len(exp.CategoryRules))
	for _, r := range exp.CategoryRules {
		ca, err := toTimestamp(r.CreatedAt)
		if err != nil {
			return RestoreSummary{}, err
		}
		rules = append(rules, store.RestoreInsertCategoryRuleParams{ID: r.ID, MatchText: r.MatchText, CategoryID: r.CategoryID, CreatedAt: ca})
	}
	recurring := make([]store.RestoreInsertRecurringParams, 0, len(exp.Recurring))
	for _, r := range exp.Recurring {
		amt, err := parseDecimal("recurring.amount", r.Amount)
		if err != nil {
			return RestoreSummary{}, err
		}
		toAmt, err := parseDecimal("recurring.to_amount", r.ToAmount)
		if err != nil {
			return RestoreSummary{}, err
		}
		start, err := parseDay("recurring.start_date", r.StartDate)
		if err != nil {
			return RestoreSummary{}, err
		}
		next, err := parseDay("recurring.next_due", r.NextDue)
		if err != nil {
			return RestoreSummary{}, err
		}
		end, err := toDate("recurring.end_date", r.EndDate)
		if err != nil {
			return RestoreSummary{}, err
		}
		ca, err := toTimestamp(r.CreatedAt)
		if err != nil {
			return RestoreSummary{}, err
		}
		recurring = append(recurring, store.RestoreInsertRecurringParams{
			ID: r.ID, Type: r.Type,
			FromAccountID: toInt8(r.FromAccountID), ToAccountID: toInt8(r.ToAccountID),
			Amount: amt, ToAmount: toAmt, CategoryID: toInt8(r.CategoryID),
			Cadence: r.Cadence, IntervalN: r.IntervalN, StartDate: start, EndDate: end,
			NextDue: next, Description: r.Description, CreatedAt: ca,
		})
	}
	tags := make([]store.RestoreInsertTagParams, 0, len(exp.Tags))
	for _, t := range exp.Tags {
		ca, err := toTimestamp(t.CreatedAt)
		if err != nil {
			return RestoreSummary{}, err
		}
		tags = append(tags, store.RestoreInsertTagParams{ID: t.ID, Name: t.Name, CreatedAt: ca})
	}
	txnTags := make([]store.RestoreInsertTransactionTagParams, 0, len(exp.TransactionTags))
	for _, tt := range exp.TransactionTags {
		txnTags = append(txnTags, store.RestoreInsertTransactionTagParams{TransactionID: tt.TransactionID, TagID: tt.TagID})
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RestoreSummary{}, fmt.Errorf("backup: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	// Delete child→parent so foreign keys never block. The Phase-2 tables go first:
	// transaction_tag references transaction + tag; budget/category_rule reference
	// category; recurring references account + category.
	for _, del := range []func(context.Context) error{
		q.RestoreDeleteTransactionTags, q.RestoreDeleteTags,
		q.RestoreDeleteBudgets, q.RestoreDeleteCategoryRules, q.RestoreDeleteRecurring,
		q.RestoreDeleteTransactions, q.RestoreDeletePrices, q.RestoreDeleteExchangeRates,
		q.RestoreDeleteSecurities, q.RestoreDeleteCategories, q.RestoreDeleteAccounts,
	} {
		if err := del(ctx); err != nil {
			return RestoreSummary{}, fmt.Errorf("backup: clear data: %w", err)
		}
	}

	// Insert parent→child, preserving ids and created_at. A foreign-key or other
	// constraint violation here (a dangling/partial file) aborts the tx → the
	// deferred rollback leaves the instance unchanged → reported as malformed.
	for _, p := range accounts {
		if err := q.RestoreInsertAccount(ctx, p); err != nil {
			return RestoreSummary{}, fmt.Errorf("%w: restoring accounts: %v", ErrMalformed, err)
		}
	}
	for _, p := range categories {
		if err := q.RestoreInsertCategory(ctx, p); err != nil {
			return RestoreSummary{}, fmt.Errorf("%w: restoring categories: %v", ErrMalformed, err)
		}
	}
	for _, p := range securities {
		if err := q.RestoreInsertSecurity(ctx, p); err != nil {
			return RestoreSummary{}, fmt.Errorf("%w: restoring securities: %v", ErrMalformed, err)
		}
	}
	for _, p := range rates {
		if err := q.RestoreInsertExchangeRate(ctx, p); err != nil {
			return RestoreSummary{}, fmt.Errorf("%w: restoring exchange rates: %v", ErrMalformed, err)
		}
	}
	for _, p := range prices {
		if err := q.RestoreInsertPrice(ctx, p); err != nil {
			return RestoreSummary{}, fmt.Errorf("%w: restoring prices: %v", ErrMalformed, err)
		}
	}
	for _, p := range transactions {
		if err := q.RestoreInsertTransaction(ctx, p); err != nil {
			return RestoreSummary{}, fmt.Errorf("%w: restoring transactions: %v", ErrMalformed, err)
		}
	}
	// Phase-2 children: budgets/rules/recurring need category+account; tags stand
	// alone; transaction_tag needs both transaction and tag.
	for _, p := range budgets {
		if err := q.RestoreInsertBudget(ctx, p); err != nil {
			return RestoreSummary{}, fmt.Errorf("%w: restoring budgets: %v", ErrMalformed, err)
		}
	}
	for _, p := range rules {
		if err := q.RestoreInsertCategoryRule(ctx, p); err != nil {
			return RestoreSummary{}, fmt.Errorf("%w: restoring category rules: %v", ErrMalformed, err)
		}
	}
	for _, p := range recurring {
		if err := q.RestoreInsertRecurring(ctx, p); err != nil {
			return RestoreSummary{}, fmt.Errorf("%w: restoring recurring: %v", ErrMalformed, err)
		}
	}
	for _, p := range tags {
		if err := q.RestoreInsertTag(ctx, p); err != nil {
			return RestoreSummary{}, fmt.Errorf("%w: restoring tags: %v", ErrMalformed, err)
		}
	}
	for _, p := range txnTags {
		if err := q.RestoreInsertTransactionTag(ctx, p); err != nil {
			return RestoreSummary{}, fmt.Errorf("%w: restoring transaction tags: %v", ErrMalformed, err)
		}
	}

	// Advance each identity sequence past the restored ids so the next
	// owner-created row gets a fresh, non-colliding id.
	for _, reset := range []func(context.Context) error{
		q.RestoreResetAccountSeq, q.RestoreResetCategorySeq, q.RestoreResetSecuritySeq,
		q.RestoreResetExchangeRateSeq, q.RestoreResetPriceSeq, q.RestoreResetTransactionSeq,
		q.RestoreResetBudgetSeq, q.RestoreResetCategoryRuleSeq, q.RestoreResetRecurringSeq,
		q.RestoreResetTagSeq,
	} {
		if err := reset(ctx); err != nil {
			return RestoreSummary{}, fmt.Errorf("backup: reset sequence: %w", err)
		}
	}

	if err := q.SetDisplayCurrency(ctx, exp.DisplayCurrency); err != nil {
		return RestoreSummary{}, fmt.Errorf("backup: restore display currency: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return RestoreSummary{}, fmt.Errorf("backup: commit: %w", err)
	}
	return RestoreSummary{
		Accounts:        len(accounts),
		Categories:      len(categories),
		Securities:      len(securities),
		ExchangeRates:   len(rates),
		Prices:          len(prices),
		Transactions:    len(transactions),
		Budgets:         len(budgets),
		CategoryRules:   len(rules),
		Recurring:       len(recurring),
		Tags:            len(tags),
		TransactionTags: len(txnTags),
	}, nil
}

// toTimestamp parses an exported RFC3339 created_at back to a Timestamptz. An
// absent value falls back to now (created_at is authored history, not a
// financial figure, and the column is NOT NULL); a present-but-unparseable
// value is a malformed file.
func toTimestamp(s string) (pgtype.Timestamptz, error) {
	if s == "" {
		return pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return pgtype.Timestamptz{}, fmt.Errorf("%w: bad created_at %q: %v", ErrMalformed, s, err)
	}
	return pgtype.Timestamptz{Time: t, Valid: true}, nil
}

// parseDecimal parses an exported decimal string exactly (never a float, NFR-5).
func parseDecimal(field, s string) (decimal.Decimal, error) {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("%w: bad decimal in %s: %v", ErrMalformed, field, err)
	}
	return d, nil
}

// parseDay parses an exported YYYY-MM-DD calendar date.
func parseDay(field, s string) (time.Time, error) {
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: bad date in %s: %v", ErrMalformed, field, err)
	}
	return t, nil
}

// toInt8 maps a nullable *int64 DTO field to pgtype.Int8 (the inverse of intPtr).
func toInt8(p *int64) pgtype.Int8 {
	if p == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *p, Valid: true}
}

// toText maps a nullable *string DTO field to pgtype.Text (the inverse of textPtr).
func toText(p *string) pgtype.Text {
	if p == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *p, Valid: true}
}

// toDate maps a nullable YYYY-MM-DD DTO field to pgtype.Date (inverse of datePtr).
// A present-but-unparseable value is a malformed file.
func toDate(field string, p *string) (pgtype.Date, error) {
	if p == nil {
		return pgtype.Date{}, nil
	}
	t, err := time.Parse(dateLayout, *p)
	if err != nil {
		return pgtype.Date{}, fmt.Errorf("%w: bad date in %s: %v", ErrMalformed, field, err)
	}
	return pgtype.Date{Time: t, Valid: true}, nil
}
