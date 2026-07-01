package analytics

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/db"
	"github.com/claudioaprado/financas/internal/money"
	"github.com/claudioaprado/financas/internal/service/account"
	"github.com/claudioaprado/financas/internal/service/category"
	"github.com/claudioaprado/financas/internal/service/exchangerate"
	"github.com/claudioaprado/financas/internal/service/settings"
	"github.com/claudioaprado/financas/internal/service/transaction"
	"github.com/claudioaprado/financas/internal/store"
)

func testDatabaseURL(t *testing.T) string {
	t.Helper()
	for _, k := range []string{"TEST_DATABASE_URL", "DATABASE_URL"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	t.Skip("set TEST_DATABASE_URL or DATABASE_URL to run analytics integration tests")
	return ""
}

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func TestAnalyticsReport(t *testing.T) {
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

	cats := category.New(pool)
	accts := account.New(pool)
	txns := transaction.New(pool)
	rates := exchangerate.New(pool)
	set := settings.New(pool)
	svc := New(pool)
	// The cash-flow window aggregates the whole (shared) ledger, so the exact
	// per-month totals below are asserted as ">= this run's contribution"; the
	// category-scoped spending is exact. Anchor June 2026. Restore the default
	// Display Currency afterward so sibling suites that expect USD aren't disturbed.
	svc.now = func() time.Time { return date(2026, 6, 15) }
	run := time.Now().UnixNano()

	if err := set.SetDisplayCurrency(ctx, money.BRL); err != nil {
		t.Fatalf("set display currency: %v", err)
	}
	defer func() { _ = set.SetDisplayCurrency(ctx, money.USD) }()
	// Rate effective 2026-01-01 (not earlier) — an earlier date would make a rate
	// exist for dates other suites assume are rate-free.
	if _, err := rates.Add(ctx, money.USD, money.BRL, date(2026, 1, 1), decimal.RequireFromString("5")); err != nil {
		t.Fatalf("add rate: %v", err)
	}

	brl, err := accts.Create(ctx, fmt.Sprintf("BRL-%d", run), account.Cash, money.BRL)
	if err != nil {
		t.Fatalf("create BRL account: %v", err)
	}
	usd, err := accts.Create(ctx, fmt.Sprintf("USD-%d", run), account.Cash, money.USD)
	if err != nil {
		t.Fatalf("create USD account: %v", err)
	}
	food, err := cats.Create(ctx, fmt.Sprintf("Food-%d", run), category.Expense)
	if err != nil {
		t.Fatalf("create Food: %v", err)
	}
	rent, err := cats.Create(ctx, fmt.Sprintf("Rent-%d", run), category.Expense)
	if err != nil {
		t.Fatalf("create Rent: %v", err)
	}
	salary, err := cats.Create(ctx, fmt.Sprintf("Salary-%d", run), category.Income)
	if err != nil {
		t.Fatalf("create Salary: %v", err)
	}

	exp := func(acct, cat int64, amount string, d time.Time) {
		if _, err := txns.Record(ctx, acct, transaction.Expense, decimal.RequireFromString(amount), d, "t", cat); err != nil {
			t.Fatalf("record expense: %v", err)
		}
	}
	// April: Food 100 BRL. June: Food 300 BRL + 20 USD (→100), Rent 600, income 2000.
	exp(brl.ID, food.ID, "100", date(2026, 4, 10))
	exp(brl.ID, food.ID, "300", date(2026, 6, 12))
	exp(usd.ID, food.ID, "20", date(2026, 6, 20))
	exp(brl.ID, rent.ID, "600", date(2026, 6, 5))
	if _, err := txns.Record(ctx, brl.ID, transaction.Income, decimal.RequireFromString("2000"), date(2026, 6, 25), "pay", salary.ID); err != nil {
		t.Fatalf("record income: %v", err)
	}

	a, err := svc.Report(ctx, 3) // Apr, May, Jun 2026
	if err != nil {
		t.Fatalf("report: %v", err)
	}

	// Spending (this run's categories only): Rent 600 first, Food 500 second.
	byCat := map[string]string{}
	for _, s := range a.Spending {
		if s.Category == food.Name || s.Category == rent.Name {
			byCat[s.Category] = s.Total.String()
		}
	}
	if byCat[rent.Name] != "600.0000 BRL" || byCat[food.Name] != "500.0000 BRL" {
		t.Fatalf("spending totals = %+v (full %+v)", byCat, a.Spending)
	}

	// The cash-flow window is exactly the 3 months, chronological, ending June. The
	// flow aggregates the WHOLE ledger, so on the shared DB we assert it CONTAINS at
	// least this run's contributions (the exact convert-then-sum is proven by the
	// category-scoped spending above and by the domain unit tests).
	if len(a.Flow) != 3 {
		t.Fatalf("flow len = %d, want 3 (%+v)", len(a.Flow), a.Flow)
	}
	apr, may, jun := a.Flow[0], a.Flow[1], a.Flow[2]
	if apr.Month != time.April || may.Month != time.May || jun.Month != time.June {
		t.Fatalf("flow months = %v/%v/%v, want Apr/May/Jun", apr.Month, may.Month, jun.Month)
	}
	atLeast := func(m money.Money, want string) bool {
		return m.Amount().GreaterThanOrEqual(decimal.RequireFromString(want))
	}
	if !atLeast(apr.Expense, "100") {
		t.Fatalf("apr expense = %s, want >= 100", apr.Expense)
	}
	// June includes this run's 300 + 100 (USD→BRL) + 600 = 1000 expense and 2000 income.
	if !atLeast(jun.Expense, "1000") || !atLeast(jun.Income, "2000") {
		t.Fatalf("jun = %+v, want expense>=1000 income>=2000", jun)
	}
	if len(a.Missing) != 0 {
		t.Fatalf("missing = %v, want empty", a.Missing)
	}

	_ = cats.Delete(ctx, food.ID, true)
	_ = cats.Delete(ctx, rent.ID, true)
	_ = cats.Delete(ctx, salary.ID, true)
}
