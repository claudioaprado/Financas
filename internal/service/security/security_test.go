package security

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/claudioaprado/financas/db"
	"github.com/claudioaprado/financas/internal/money"
	"github.com/claudioaprado/financas/internal/store"
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
