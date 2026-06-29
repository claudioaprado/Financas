package exchangerate

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

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
	t.Skip("set TEST_DATABASE_URL or DATABASE_URL to run exchangerate integration tests")
	return ""
}

func d(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func TestExchangeRate(t *testing.T) {
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

	// Append two effective-dated USD->BRL rates.
	if _, err := svc.Add(ctx, money.USD, money.BRL, d(t, "2024-01-01"), decimal.RequireFromString("5.0")); err != nil {
		t.Fatalf("add rate 1: %v", err)
	}
	if _, err := svc.Add(ctx, money.USD, money.BRL, d(t, "2024-06-01"), decimal.RequireFromString("5.2")); err != nil {
		t.Fatalf("add rate 2: %v", err)
	}

	// Effective-at selection (latest <= date).
	cases := []struct {
		date string
		want string // "" means ErrNoRate
	}{
		{"2024-03-01", "5"},   // between the two -> first
		{"2024-07-01", "5.2"}, // after both -> latest
		{"2023-12-01", ""},    // before first -> none
	}
	for _, c := range cases {
		got, err := svc.RateAt(ctx, money.USD, money.BRL, d(t, c.date))
		if c.want == "" {
			if !errors.Is(err, ErrNoRate) {
				t.Errorf("RateAt(%s) = %v, %v; want ErrNoRate", c.date, got, err)
			}
			continue
		}
		if err != nil || !got.Equal(decimal.RequireFromString(c.want)) {
			t.Errorf("RateAt(%s) = %v, %v; want %s", c.date, got, err, c.want)
		}
	}

	// No inversion: BRL->USD has no rows even though USD->BRL exists.
	if _, err := svc.RateAt(ctx, money.BRL, money.USD, d(t, "2024-12-01")); !errors.Is(err, ErrNoRate) {
		t.Errorf("RateAt(BRL,USD) = %v; want ErrNoRate (no inversion)", err)
	}

	// Validation.
	if _, err := svc.Add(ctx, money.USD, money.USD, d(t, "2024-01-01"), decimal.RequireFromString("1")); !errors.Is(err, ErrSameCurrency) {
		t.Errorf("same-currency add = %v; want ErrSameCurrency", err)
	}
	if _, err := svc.Add(ctx, money.USD, money.Currency("EUR"), d(t, "2024-01-01"), decimal.RequireFromString("1")); !errors.Is(err, ErrUnsupportedCurrency) {
		t.Errorf("unsupported add = %v; want ErrUnsupportedCurrency", err)
	}
	if _, err := svc.Add(ctx, money.USD, money.BRL, d(t, "2024-01-01"), decimal.RequireFromString("0")); !errors.Is(err, ErrNonPositiveRate) {
		t.Errorf("non-positive add = %v; want ErrNonPositiveRate", err)
	}

	// List returns the appended rows.
	rows, err := svc.List(ctx)
	if err != nil || len(rows) < 2 {
		t.Fatalf("List = %v, %v; want >= 2 rows", len(rows), err)
	}
}
