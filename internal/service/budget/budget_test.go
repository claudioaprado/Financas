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
	"github.com/claudioaprado/financas/internal/service/category"
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
