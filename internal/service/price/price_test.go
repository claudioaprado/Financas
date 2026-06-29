package price

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/db"
	"github.com/claudioaprado/financas/internal/store"
)

func testDatabaseURL(t *testing.T) string {
	t.Helper()
	for _, k := range []string{"TEST_DATABASE_URL", "DATABASE_URL"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	t.Skip("set TEST_DATABASE_URL or DATABASE_URL to run price integration tests")
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

func TestPrice(t *testing.T) {
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

	// Seed a security to price (unique symbol per run to avoid cross-run clashes).
	symbol := "PRC" + time.Now().Format("150405.000000000")
	sec, err := store.New(pool).CreateSecurity(ctx, store.CreateSecurityParams{
		Symbol:        symbol,
		Name:          "Price Test Co",
		Type:          "stock",
		QuoteCurrency: "BRL",
	})
	if err != nil {
		t.Fatalf("seed security: %v", err)
	}

	// Append two effective-dated prices.
	if _, err := svc.Add(ctx, sec.ID, d(t, "2024-01-01"), decimal.RequireFromString("10.00")); err != nil {
		t.Fatalf("add price 1: %v", err)
	}
	if _, err := svc.Add(ctx, sec.ID, d(t, "2024-06-01"), decimal.RequireFromString("12.00")); err != nil {
		t.Fatalf("add price 2: %v", err)
	}

	// Effective-at selection (latest <= date).
	cases := []struct {
		date string
		want string // "" means ErrNoPrice
	}{
		{"2024-03-01", "10"}, // between the two -> first
		{"2024-07-01", "12"}, // after both -> latest
		{"2023-12-01", ""},   // before first -> none
	}
	for _, c := range cases {
		got, err := svc.PriceAt(ctx, sec.ID, d(t, c.date))
		if c.want == "" {
			if !errors.Is(err, ErrNoPrice) {
				t.Errorf("PriceAt(%s) = %v, %v; want ErrNoPrice", c.date, got, err)
			}
			continue
		}
		if err != nil || !got.Equal(decimal.RequireFromString(c.want)) {
			t.Errorf("PriceAt(%s) = %v, %v; want %s", c.date, got, err, c.want)
		}
	}

	// LatestPrices returns the newest point (12.00 @ 2024-06-01) for this security.
	latest, err := svc.LatestPrices(ctx, d(t, "2024-12-31"))
	if err != nil {
		t.Fatalf("LatestPrices: %v", err)
	}
	pp, ok := latest[sec.ID]
	if !ok || !pp.Price.Equal(decimal.RequireFromString("12")) {
		t.Errorf("LatestPrices[%d] = %+v, ok=%v; want price 12", sec.ID, pp, ok)
	}
	if !pp.EffectiveDate.Equal(d(t, "2024-06-01")) {
		t.Errorf("LatestPrices effective date = %v; want 2024-06-01", pp.EffectiveDate)
	}

	// Validation.
	if _, err := svc.Add(ctx, sec.ID, d(t, "2024-01-01"), decimal.RequireFromString("0")); !errors.Is(err, ErrNonPositivePrice) {
		t.Errorf("non-positive add = %v; want ErrNonPositivePrice", err)
	}
	// A price with >4 dp is rounded to the money scale (banker's) at ingest, so the
	// stored value is exact (no silent half-away-from-zero re-rounding by NUMERIC).
	if got, err := svc.Add(ctx, sec.ID, d(t, "2024-02-01"), decimal.RequireFromString("16.99995")); err != nil || !got.Price.Equal(decimal.RequireFromString("17")) {
		t.Errorf("over-precise add = %v, %v; want price 17 (banker's round to 4dp)", got.Price, err)
	}
	// A positive price below half a cent rounds to zero → typed error, not a raw
	// CHECK (price > 0) violation echoed from Postgres.
	if _, err := svc.Add(ctx, sec.ID, d(t, "2024-02-01"), decimal.RequireFromString("0.00004")); !errors.Is(err, ErrNonPositivePrice) {
		t.Errorf("sub-cent add = %v; want ErrNonPositivePrice (rounds to 0)", err)
	}
	if _, err := svc.Add(ctx, 999_999_999, d(t, "2024-01-01"), decimal.RequireFromString("1")); !errors.Is(err, ErrSecurityNotFound) {
		t.Errorf("missing-security add = %v; want ErrSecurityNotFound", err)
	}

	// List returns the appended rows newest-first, with the symbol resolved.
	rows, err := svc.List(ctx)
	if err != nil || len(rows) < 2 {
		t.Fatalf("List = %d, %v; want >= 2 rows", len(rows), err)
	}
	for _, r := range rows {
		if r.SecurityID == sec.ID && r.Symbol != symbol {
			t.Errorf("List symbol = %q; want %q", r.Symbol, symbol)
		}
	}
}
