package budget

import (
	"context"
	"errors"
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
	t.Skip("set TEST_DATABASE_URL or DATABASE_URL to run budget integration tests")
	return ""
}

func TestBudgetCRUD(t *testing.T) {
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
	svc := New(pool)
	run := time.Now().UnixNano()

	food, err := cats.Create(ctx, fmt.Sprintf("Food-%d", run), category.Expense)
	if err != nil {
		t.Fatalf("create category: %v", err)
	}
	pay, err := cats.Create(ctx, fmt.Sprintf("Pay-%d", run), category.Income)
	if err != nil {
		t.Fatalf("create category: %v", err)
	}

	// Set two targets, then upsert one (a second Set overwrites, not duplicates).
	if err := svc.Set(ctx, food.ID, decimal.RequireFromString("500.00")); err != nil {
		t.Fatalf("set food: %v", err)
	}
	if err := svc.Set(ctx, pay.ID, decimal.RequireFromString("3000")); err != nil {
		t.Fatalf("set pay: %v", err)
	}
	if err := svc.Set(ctx, food.ID, decimal.RequireFromString("650.5")); err != nil {
		t.Fatalf("upsert food: %v", err)
	}

	budgets, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byCat := map[int64]Budget{}
	for _, b := range budgets {
		if b.CategoryID == food.ID || b.CategoryID == pay.ID {
			byCat[b.CategoryID] = b
		}
	}
	if len(byCat) != 2 {
		t.Fatalf("expected 2 budgets for this run, got %d (%+v)", len(byCat), byCat)
	}
	if got := byCat[food.ID]; !got.Amount.Equal(decimal.RequireFromString("650.5")) ||
		got.Kind != "expense" || got.CategoryName != food.Name {
		t.Fatalf("food budget = %+v", got)
	}
	if got := byCat[pay.ID]; !got.Amount.Equal(decimal.RequireFromString("3000")) || got.Kind != "income" {
		t.Fatalf("pay budget = %+v", got)
	}

	// A zero or negative target is rejected; the store is untouched.
	if err := svc.Set(ctx, food.ID, decimal.Zero); !errors.Is(err, ErrNonPositiveAmount) {
		t.Fatalf("set zero: got %v, want ErrNonPositiveAmount", err)
	}
	if err := svc.Set(ctx, food.ID, decimal.RequireFromString("-1")); !errors.Is(err, ErrNonPositiveAmount) {
		t.Fatalf("set negative: got %v, want ErrNonPositiveAmount", err)
	}

	// A target for a missing category is rejected by the FK.
	if err := svc.Set(ctx, -1, decimal.RequireFromString("10")); !errors.Is(err, ErrCategoryNotFound) {
		t.Fatalf("set missing category: got %v, want ErrCategoryNotFound", err)
	}

	// Delete removes the target; deleting again reports not-found.
	if err := svc.Delete(ctx, food.ID); err != nil {
		t.Fatalf("delete food: %v", err)
	}
	if err := svc.Delete(ctx, food.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete again: got %v, want ErrNotFound", err)
	}

	budgets, err = svc.List(ctx)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	for _, b := range budgets {
		if b.CategoryID == food.ID {
			t.Fatalf("food budget still present after delete: %+v", b)
		}
	}

	// Clean up this run's categories (cascades the remaining budget away).
	_ = cats.Delete(ctx, food.ID, true)
	_ = cats.Delete(ctx, pay.ID, true)
}

func TestBudgetReport(t *testing.T) {
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
	run := time.Now().UnixNano()

	if err := set.SetDisplayCurrency(ctx, money.BRL); err != nil {
		t.Fatalf("set display currency: %v", err)
	}
	// A USD→BRL rate of 5, effective before the transactions we date.
	if _, err := rates.Add(ctx, money.USD, money.BRL, date(2026, 1, 1), decimal.RequireFromString("5")); err != nil {
		t.Fatalf("add rate: %v", err)
	}

	brlAcct, err := accts.Create(ctx, fmt.Sprintf("BRL-%d", run), account.Cash, money.BRL)
	if err != nil {
		t.Fatalf("create BRL account: %v", err)
	}
	usdAcct, err := accts.Create(ctx, fmt.Sprintf("USD-%d", run), account.Cash, money.USD)
	if err != nil {
		t.Fatalf("create USD account: %v", err)
	}
	food, err := cats.Create(ctx, fmt.Sprintf("Food-%d", run), category.Expense)
	if err != nil {
		t.Fatalf("create category: %v", err)
	}

	// May: 80 BRL Food. June: 100 BRL + 20 USD (→100 BRL) Food ⇒ actual 200.
	rec := func(acct int64, amount string, d time.Time) {
		if _, err := txns.Record(ctx, acct, transaction.Expense, decimal.RequireFromString(amount), d, "t", food.ID); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	rec(brlAcct.ID, "80", date(2026, 5, 10))
	rec(brlAcct.ID, "100", date(2026, 6, 12))
	rec(usdAcct.ID, "20", date(2026, 6, 20))

	// Target 300. June carryover = (300 − 80) = 220 ⇒ planned 520; actual 200 ⇒
	// remaining 320.
	if err := svc.Set(ctx, food.ID, decimal.RequireFromString("300")); err != nil {
		t.Fatalf("set target: %v", err)
	}

	rep, err := svc.Report(ctx, 2026, time.June)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	found := false
	for _, l := range rep.Lines {
		if l.CategoryID != food.ID {
			continue
		}
		found = true
		if l.Target.String() != "300.0000 BRL" || l.Carryover.String() != "220.0000 BRL" ||
			l.Planned.String() != "520.0000 BRL" || l.Actual.String() != "200.0000 BRL" ||
			l.Remaining.String() != "320.0000 BRL" {
			t.Fatalf("Food line = %+v", l)
		}
	}
	if !found {
		t.Fatalf("no line for Food in report %+v", rep.Lines)
	}
	if len(rep.Missing) != 0 {
		t.Fatalf("Missing = %v, want empty", rep.Missing)
	}

	_ = cats.Delete(ctx, food.ID, true)
}

// TestBudgetReportMissingRate exercises the AD-6 partial-total path end to end: a
// transaction in a currency with no effective rate on its date is excluded from
// the actual and its currency surfaced in Missing (never guessed). The txn is
// dated in 1990 so no rate any other test seeds (all modern) can cover it.
func TestBudgetReportMissingRate(t *testing.T) {
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
	set := settings.New(pool)
	svc := New(pool)
	run := time.Now().UnixNano()

	if err := set.SetDisplayCurrency(ctx, money.BRL); err != nil {
		t.Fatalf("set display currency: %v", err)
	}
	usdAcct, err := accts.Create(ctx, fmt.Sprintf("USDm-%d", run), account.Cash, money.USD)
	if err != nil {
		t.Fatalf("create USD account: %v", err)
	}
	food, err := cats.Create(ctx, fmt.Sprintf("Foodm-%d", run), category.Expense)
	if err != nil {
		t.Fatalf("create category: %v", err)
	}
	if _, err := txns.Record(ctx, usdAcct.ID, transaction.Expense, decimal.RequireFromString("20"), date(1990, 6, 15), "t", food.ID); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := svc.Set(ctx, food.ID, decimal.RequireFromString("300")); err != nil {
		t.Fatalf("set target: %v", err)
	}

	rep, err := svc.Report(ctx, 1990, time.June)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	found := false
	for _, l := range rep.Lines {
		if l.CategoryID != food.ID {
			continue
		}
		found = true
		// The unrated USD spend is excluded ⇒ actual 0, remaining equals target.
		if l.Actual.String() != "0.0000 BRL" || l.Remaining.String() != "300.0000 BRL" {
			t.Fatalf("Food line = %+v", l)
		}
	}
	if !found {
		t.Fatalf("no line for Food in report %+v", rep.Lines)
	}
	missingUSD := false
	for _, c := range rep.Missing {
		if c == money.USD {
			missingUSD = true
		}
	}
	if !missingUSD {
		t.Fatalf("Missing = %v, want it to contain USD", rep.Missing)
	}

	_ = cats.Delete(ctx, food.ID, true)
}

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
