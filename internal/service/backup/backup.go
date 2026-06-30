// Package backup is the use-case for exporting the owner's AUTHORED data to a
// single, self-describing, re-importable file (Story 6.1, FR-15). It reads only
// authored state — accounts, categories, securities, exchange rates, prices,
// transactions, and the display-currency setting — never derived figures
// (holdings, balances, valuation, net worth), which are recomputed on read
// (AD-2). It assembles a versioned, JSON-ready value; the http layer serializes
// and streams it (AD-1). Decimal amounts are carried as strings, never floats
// (AD-4/NFR-5). All tables are read inside one transaction so the snapshot is
// internally consistent (NFR-2).
package backup

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

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
		})
	}

	if err := tx.Commit(ctx); err != nil {
		return Export{}, fmt.Errorf("backup: commit: %w", err)
	}
	return exp, nil
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
