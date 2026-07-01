// Package security is the use-case for the owner's securities (FR-3). A Security
// is an instrument the owner trades — a symbol, display name, type (stock, ETF,
// fund, or other), and the currency it is quoted in. Securities are created and
// listed here; trades, derived Holdings, prices, and valuation are added by later
// Epic 4 stories (AD-2, derived on read).
//
// The symbol is normalized (trimmed + uppercased) so duplicates are prevented
// case-insensitively, backed by a UNIQUE constraint. The quote currency is stored
// natively and never converted (AD-5). Writes go through one DB transaction per
// use-case (AD-3); the service is the validation authority, with DB CHECK/FK/
// UNIQUE constraints as the backstop.
package security

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioaprado/financas/internal/money"
	"github.com/claudioaprado/financas/internal/store"
	"github.com/claudioaprado/financas/internal/validate"
)

// SecurityType is the kind of instrument.
type SecurityType string

const (
	// Stock is an individual equity.
	Stock SecurityType = "stock"
	// ETF is an exchange-traded fund.
	ETF SecurityType = "etf"
	// Fund is a mutual/investment fund.
	Fund SecurityType = "fund"
	// Other is any instrument that does not fit the above.
	Other SecurityType = "other"
)

// IsValid reports whether t is one of the supported security types.
func (t SecurityType) IsValid() bool {
	switch t {
	case Stock, ETF, Fund, Other:
		return true
	default:
		return false
	}
}

// Input errors. The service is the validation authority; DB CHECK/FK/UNIQUE
// constraints are the backstop.
var (
	ErrEmptySymbol         = errors.New("security: symbol must not be empty")
	ErrEmptyName           = errors.New("security: name must not be empty")
	ErrInvalidType         = errors.New("security: type must be stock, etf, fund, or other")
	ErrUnsupportedCurrency = errors.New("security: unsupported quote currency")
	ErrDuplicateSymbol     = errors.New("security: a security with that symbol already exists")
	// ErrNotFound means no security matched the given id.
	ErrNotFound = errors.New("security: not found")
	// ErrCategoryNotFound means the assigned asset category id does not exist.
	ErrCategoryNotFound = errors.New("security: asset category not found")
)

// Security is one instrument the owner trades. Holdings and valuation are derived
// elsewhere (AD-2) and are deliberately absent here.
type Security struct {
	ID            int64
	Symbol        string
	Name          string
	Type          SecurityType
	QuoteCurrency money.Currency
	// AssetCategoryID is the owner-defined asset category, or 0 if uncategorized.
	AssetCategoryID int64
	CreatedAt       time.Time
}

// Service creates and lists securities.
type Service struct {
	pool *pgxpool.Pool
}

// New returns a security Service backed by the given pool.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// Create validates and appends a new security, returning the stored row. The
// symbol is normalized to upper-case so duplicates are rejected case-insensitively
// (ErrDuplicateSymbol). It writes inside one transaction (AD-3).
func (s *Service) Create(ctx context.Context, symbol, name string, typ SecurityType, quote money.Currency) (Security, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	name = strings.TrimSpace(name)
	if symbol == "" {
		return Security{}, ErrEmptySymbol
	}
	if err := validate.Symbol(symbol); err != nil {
		return Security{}, err
	}
	if name == "" {
		return Security{}, ErrEmptyName
	}
	if err := validate.Name(name); err != nil {
		return Security{}, err
	}
	if !typ.IsValid() {
		return Security{}, ErrInvalidType
	}
	if !money.IsSupported(quote) {
		return Security{}, ErrUnsupportedCurrency
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Security{}, fmt.Errorf("security: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row, err := store.New(tx).CreateSecurity(ctx, store.CreateSecurityParams{
		Symbol:        symbol,
		Name:          name,
		Type:          string(typ),
		QuoteCurrency: string(quote),
	})
	if err != nil {
		// A unique violation means the symbol is already taken.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Security{}, ErrDuplicateSymbol
		}
		return Security{}, fmt.Errorf("security: insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Security{}, fmt.Errorf("security: commit: %w", err)
	}
	return toSecurity(row), nil
}

// SetCategory assigns a security's asset category, or clears it when
// assetCategoryID is 0. A missing security returns ErrNotFound; an unknown
// category id violates the FK and returns ErrCategoryNotFound. It writes inside
// one transaction (AD-3).
func (s *Service) SetCategory(ctx context.Context, securityID, assetCategoryID int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("security: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	n, err := store.New(tx).SetSecurityCategory(ctx, store.SetSecurityCategoryParams{
		ID:              securityID,
		AssetCategoryID: int8OrNull(assetCategoryID),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return ErrCategoryNotFound
		}
		return fmt.Errorf("security: set category: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("security: commit: %w", err)
	}
	return nil
}

// Get returns one security by id, or ErrNotFound if none matches.
func (s *Service) Get(ctx context.Context, id int64) (Security, error) {
	row, err := store.New(s.pool).GetSecurity(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Security{}, ErrNotFound
	}
	if err != nil {
		return Security{}, fmt.Errorf("security: get: %w", err)
	}
	return toSecurity(row), nil
}

// List returns all securities ordered by symbol.
func (s *Service) List(ctx context.Context) ([]Security, error) {
	rows, err := store.New(s.pool).ListSecurities(ctx)
	if err != nil {
		return nil, fmt.Errorf("security: list: %w", err)
	}
	out := make([]Security, len(rows))
	for i, r := range rows {
		out[i] = toSecurity(r)
	}
	return out, nil
}

func toSecurity(r store.Security) Security {
	return Security{
		ID:              r.ID,
		Symbol:          r.Symbol,
		Name:            r.Name,
		Type:            SecurityType(r.Type),
		QuoteCurrency:   money.Currency(r.QuoteCurrency),
		AssetCategoryID: nullInt8(r.AssetCategoryID),
		CreatedAt:       r.CreatedAt.Time,
	}
}

// int8OrNull maps a category id to a nullable column value (0 ⇒ NULL/none).
func int8OrNull(id int64) pgtype.Int8 {
	if id <= 0 {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: id, Valid: true}
}

// nullInt8 maps a nullable column value to an id (0 ⇒ none).
func nullInt8(v pgtype.Int8) int64 {
	if v.Valid {
		return v.Int64
	}
	return 0
}
