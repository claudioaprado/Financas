package settings

import (
	"context"
	"errors"
	"os"
	"testing"

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
	t.Skip("set TEST_DATABASE_URL or DATABASE_URL to run settings integration tests")
	return ""
}

func TestDisplayCurrencyLifecycle(t *testing.T) {
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

	// Default seeded value is USD.
	if got, err := svc.DisplayCurrency(ctx); err != nil || got != money.USD {
		t.Fatalf("default DisplayCurrency = %q, %v; want USD", got, err)
	}

	// Switching to a supported currency persists.
	if err := svc.SetDisplayCurrency(ctx, money.BRL); err != nil {
		t.Fatalf("SetDisplayCurrency(BRL): %v", err)
	}
	if got, _ := svc.DisplayCurrency(ctx); got != money.BRL {
		t.Fatalf("after set, DisplayCurrency = %q; want BRL", got)
	}

	// Unsupported currency is rejected and does not change the stored value.
	if err := svc.SetDisplayCurrency(ctx, money.Currency("EUR")); !errors.Is(err, ErrUnsupportedCurrency) {
		t.Fatalf("SetDisplayCurrency(EUR) = %v; want ErrUnsupportedCurrency", err)
	}
	if got, _ := svc.DisplayCurrency(ctx); got != money.BRL {
		t.Fatalf("after rejected set, DisplayCurrency = %q; want still BRL", got)
	}

	// ListCurrencies returns the supported set.
	currs, err := svc.ListCurrencies(ctx)
	if err != nil || len(currs) != 2 {
		t.Fatalf("ListCurrencies = %v, %v; want 2 currencies", currs, err)
	}

	// Restore default for repeatability.
	if err := svc.SetDisplayCurrency(ctx, money.USD); err != nil {
		t.Fatalf("restore USD: %v", err)
	}
}
