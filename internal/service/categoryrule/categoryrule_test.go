package categoryrule

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

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
	t.Skip("set TEST_DATABASE_URL or DATABASE_URL to run categoryrule integration tests")
	return ""
}

func TestCategoryRuleCRUD(t *testing.T) {
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

	// Add two rules; List returns them in id order with kind/name joined.
	r1, err := svc.Add(ctx, "uber eats", food.ID)
	if err != nil {
		t.Fatalf("add rule 1: %v", err)
	}
	if _, err := svc.Add(ctx, "salary", pay.ID); err != nil {
		t.Fatalf("add rule 2: %v", err)
	}

	rules, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// Filter to this run's categories (the base DB may hold other rules).
	var mine []Rule
	for _, r := range rules {
		if r.CategoryID == food.ID || r.CategoryID == pay.ID {
			mine = append(mine, r)
		}
	}
	if len(mine) != 2 || mine[0].MatchText != "uber eats" || mine[0].Kind != "expense" ||
		mine[0].CategoryName != food.Name || mine[1].Kind != "income" {
		t.Fatalf("list = %+v", mine)
	}

	// Empty match text rejected.
	if _, err := svc.Add(ctx, "   ", food.ID); !errors.Is(err, ErrEmptyMatch) {
		t.Errorf("empty match = %v; want ErrEmptyMatch", err)
	}
	// A rule to a non-existent category is rejected (FK).
	if _, err := svc.Add(ctx, "x", 999999999); !errors.Is(err, ErrCategoryNotFound) {
		t.Errorf("missing category = %v; want ErrCategoryNotFound", err)
	}

	// Delete removes a rule; deleting again is ErrNotFound.
	if err := svc.Delete(ctx, r1.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := svc.Delete(ctx, r1.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("re-delete = %v; want ErrNotFound", err)
	}

	// Deleting the category cascades its remaining rules away.
	if err := cats.Delete(ctx, pay.ID, true); err != nil {
		t.Fatalf("delete category: %v", err)
	}
	after, _ := svc.List(ctx)
	for _, r := range after {
		if r.CategoryID == pay.ID {
			t.Errorf("rule for deleted category still present: %+v", r)
		}
	}
}
