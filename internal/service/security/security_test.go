package security

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/claudioaprado/financas/db"
	"github.com/claudioaprado/financas/internal/money"
	"github.com/claudioaprado/financas/internal/store"
	"github.com/claudioaprado/financas/internal/validate"
)

func testDatabaseURL(t *testing.T) string {
	t.Helper()
	for _, k := range []string{"TEST_DATABASE_URL", "DATABASE_URL"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	t.Skip("set TEST_DATABASE_URL or DATABASE_URL to run security integration tests")
	return ""
}

func TestSecurity(t *testing.T) {
	url := testDatabaseURL(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, url, db.Migrations); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := store.NewPool(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	svc := New(pool)
	run := time.Now().UnixNano()
	sym := fmt.Sprintf("PETR%d", run)

	// Create + the stored row is normalized.
	sec, err := svc.Create(ctx, "  "+sym+"  ", "Petrobras PN", Stock, money.BRL)
	if err != nil {
		t.Fatalf("create security: %v", err)
	}
	if sec.Symbol != sym {
		t.Errorf("symbol = %q; want normalized %q", sec.Symbol, sym)
	}
	if sec.Type != Stock || sec.QuoteCurrency != money.BRL {
		t.Errorf("stored security = %+v; want stock/BRL", sec)
	}

	// Duplicate symbol is rejected case-insensitively.
	lower := ""
	for _, r := range sym {
		if r >= 'A' && r <= 'Z' {
			lower += string(r + ('a' - 'A'))
		} else {
			lower += string(r)
		}
	}
	if _, err := svc.Create(ctx, lower, "Dup", Stock, money.BRL); !errors.Is(err, ErrDuplicateSymbol) {
		t.Errorf("duplicate symbol = %v; want ErrDuplicateSymbol", err)
	}

	// Validation.
	if _, err := svc.Create(ctx, "", "X", Stock, money.USD); !errors.Is(err, ErrEmptySymbol) {
		t.Errorf("empty symbol = %v; want ErrEmptySymbol", err)
	}
	if _, err := svc.Create(ctx, fmt.Sprintf("A%d", run), "  ", Stock, money.USD); !errors.Is(err, ErrEmptyName) {
		t.Errorf("empty name = %v; want ErrEmptyName", err)
	}
	if _, err := svc.Create(ctx, fmt.Sprintf("B%d", run), "Bad", SecurityType("crypto"), money.USD); !errors.Is(err, ErrInvalidType) {
		t.Errorf("bad type = %v; want ErrInvalidType", err)
	}
	if _, err := svc.Create(ctx, fmt.Sprintf("C%d", run), "Bad", Stock, money.Currency("EUR")); !errors.Is(err, ErrUnsupportedCurrency) {
		t.Errorf("bad currency = %v; want ErrUnsupportedCurrency", err)
	}

	// Get + ErrNotFound.
	got, err := svc.Get(ctx, sec.ID)
	if err != nil || got.Symbol != sym {
		t.Fatalf("get security = %+v %v", got, err)
	}
	if _, err := svc.Get(ctx, -1); !errors.Is(err, ErrNotFound) {
		t.Errorf("get missing = %v; want ErrNotFound", err)
	}

	// SetCategory: clearing (0) is safe; an unknown category id violates the FK;
	// a missing security is ErrNotFound.
	if err := svc.SetCategory(ctx, sec.ID, 0); err != nil {
		t.Errorf("clear category: %v", err)
	}
	if err := svc.SetCategory(ctx, sec.ID, 999999999); !errors.Is(err, ErrCategoryNotFound) {
		t.Errorf("bad category = %v; want ErrCategoryNotFound", err)
	}
	if err := svc.SetCategory(ctx, -1, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("set on missing security = %v; want ErrNotFound", err)
	}

	// Free-text guards (deferred 4.1): a symbol with interior whitespace and an
	// over-long name are rejected by the shared validator.
	if _, err := svc.Create(ctx, fmt.Sprintf("PE TR%d", run), "Spaced", Stock, money.USD); !errors.Is(err, validate.ErrSymbolBadChars) {
		t.Errorf("spaced symbol = %v; want ErrSymbolBadChars", err)
	}
	if _, err := svc.Create(ctx, fmt.Sprintf("D%d", run), strings.Repeat("x", validate.MaxNameLen+1), Stock, money.USD); !errors.Is(err, validate.ErrNameTooLong) {
		t.Errorf("over-long name = %v; want ErrNameTooLong", err)
	}

	// A second, distinct security lists, ordered by symbol.
	voo := fmt.Sprintf("AAA%d", run)
	if _, err := svc.Create(ctx, voo, "Vanguard S&P 500 ETF", ETF, money.USD); err != nil {
		t.Fatalf("create second security: %v", err)
	}
	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) < 2 {
		t.Fatalf("list returned %d securities; want >= 2", len(list))
	}
	// List is ordered by symbol; confirm it is non-decreasing.
	for i := 1; i < len(list); i++ {
		if list[i-1].Symbol > list[i].Symbol {
			t.Errorf("list not ordered by symbol: %q before %q", list[i-1].Symbol, list[i].Symbol)
		}
	}
}

// TestSymbolCaseInsensitiveDBBackstop proves the DB enforces case-insensitive
// uniqueness independently of the service (migration 00017's functional index).
// The service always upper-cases, so a case-collision can only arrive from a
// non-service write path (e.g. a restore from export) — simulated here with a
// raw lower-case INSERT, which must fail with a unique violation.
func TestSymbolCaseInsensitiveDBBackstop(t *testing.T) {
	url := testDatabaseURL(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, url, db.Migrations); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := store.NewPool(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	run := time.Now().UnixNano()
	sym := fmt.Sprintf("ZZB%d", run) // upper-case, letters + digits, well under the cap

	if _, err := New(pool).Create(ctx, sym, "Backstop", Stock, money.USD); err != nil {
		t.Fatalf("create security: %v", err)
	}

	// Raw insert of the lower-cased symbol bypasses the service normalization.
	_, err = pool.Exec(ctx,
		`INSERT INTO security (symbol, name, type, quote_currency) VALUES ($1, $2, 'stock', 'USD')`,
		strings.ToLower(sym), "Sneaky")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("raw lower-case insert err = %v; want unique violation (23505)", err)
	}
}
