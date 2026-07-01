package category

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/db"
	"github.com/claudioaprado/financas/internal/money"
	"github.com/claudioaprado/financas/internal/service/account"
	"github.com/claudioaprado/financas/internal/service/transaction"
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
	t.Skip("set TEST_DATABASE_URL or DATABASE_URL to run category integration tests")
	return ""
}

func TestCategory(t *testing.T) {
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
	accts := account.New(pool)
	txns := transaction.New(pool)
	run := time.Now().UnixNano()

	// Create + validation.
	food, err := svc.Create(ctx, fmt.Sprintf("Food-%d", run), Expense)
	if err != nil || food.Kind != Expense {
		t.Fatalf("create expense category: %+v %v", food, err)
	}
	if _, err := svc.Create(ctx, "  ", Income); !errors.Is(err, ErrEmptyName) {
		t.Errorf("empty name = %v; want ErrEmptyName", err)
	}
	if _, err := svc.Create(ctx, strings.Repeat("x", validate.MaxNameLen+1), Income); !errors.Is(err, validate.ErrNameTooLong) {
		t.Errorf("over-long name = %v; want ErrNameTooLong", err)
	}
	if _, err := svc.Create(ctx, "Bad", Kind("savings")); !errors.Is(err, ErrInvalidKind) {
		t.Errorf("bad kind = %v; want ErrInvalidKind", err)
	}

	// Unused category deletes cleanly.
	tmp, _ := svc.Create(ctx, fmt.Sprintf("Tmp-%d", run), Income)
	if err := svc.Delete(ctx, tmp.ID, false); err != nil {
		t.Errorf("delete unused category: %v", err)
	}
	if err := svc.Delete(ctx, -1, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete missing = %v; want ErrNotFound", err)
	}

	// Assign Food to an expense, then it is in use.
	cash, err := accts.Create(ctx, fmt.Sprintf("Wallet-%d", run), account.Cash, money.USD)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if _, err := txns.Record(ctx, cash.ID, transaction.Expense, decimal.RequireFromString("12"), tm(t), "lunch", food.ID); err != nil {
		t.Fatalf("record categorized expense: %v", err)
	}

	usage, err := svc.ListWithUsage(ctx)
	if err != nil {
		t.Fatalf("list with usage: %v", err)
	}
	if got := usageCount(usage, food.ID); got != 1 {
		t.Errorf("Food usage = %d; want 1", got)
	}

	// Guarded delete: refused while in use, then forced (unassigns + deletes).
	if err := svc.Delete(ctx, food.ID, false); !errors.Is(err, ErrCategoryInUse) {
		t.Errorf("delete in-use = %v; want ErrCategoryInUse", err)
	}
	if err := svc.Delete(ctx, food.ID, true); err != nil {
		t.Fatalf("force delete: %v", err)
	}
	after, _ := svc.List(ctx)
	for _, c := range after {
		if c.ID == food.ID {
			t.Errorf("Food should be gone after force delete")
		}
	}
}

func usageCount(us []CategoryUsage, id int64) int64 {
	for _, u := range us {
		if u.ID == id {
			return u.Count
		}
	}
	return -1
}

func tm(t *testing.T) time.Time {
	t.Helper()
	v, err := time.Parse("2006-01-02", "2024-06-01")
	if err != nil {
		t.Fatal(err)
	}
	return v
}
