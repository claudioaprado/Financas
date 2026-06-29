package account

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
	t.Skip("set TEST_DATABASE_URL or DATABASE_URL to run account integration tests")
	return ""
}

func TestAccount(t *testing.T) {
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

	// Unique suffix so the test is resilient to rows accumulating across runs.
	run := time.Now().UnixNano()
	name := func(s string) string { return fmt.Sprintf("%s-%d", s, run) }

	// Create one of each type, across both currencies.
	cash, err := svc.Create(ctx, name("Checking"), Cash, money.USD)
	if err != nil {
		t.Fatalf("create cash: %v", err)
	}
	if cash.ID == 0 || cash.Type != Cash || cash.Currency != money.USD || cash.Archived || cash.CreatedAt.IsZero() {
		t.Fatalf("created cash = %+v; want non-zero id, cash/USD, not archived, created_at set", cash)
	}
	credit, err := svc.Create(ctx, "  "+name("Visa")+"  ", Credit, money.USD) // also checks trimming
	if err != nil {
		t.Fatalf("create credit: %v", err)
	}
	if credit.Name != name("Visa") {
		t.Errorf("credit name = %q; want trimmed %q", credit.Name, name("Visa"))
	}
	invest, err := svc.Create(ctx, name("Brokerage"), Investment, money.BRL)
	if err != nil {
		t.Fatalf("create investment: %v", err)
	}

	// Validation.
	if _, err := svc.Create(ctx, "   ", Cash, money.USD); !errors.Is(err, ErrEmptyName) {
		t.Errorf("empty-name create = %v; want ErrEmptyName", err)
	}
	if _, err := svc.Create(ctx, name("Bad"), AccountType("savings"), money.USD); !errors.Is(err, ErrInvalidType) {
		t.Errorf("invalid-type create = %v; want ErrInvalidType", err)
	}
	if _, err := svc.Create(ctx, name("Bad"), Cash, money.Currency("EUR")); !errors.Is(err, ErrUnsupportedCurrency) {
		t.Errorf("unsupported-currency create = %v; want ErrUnsupportedCurrency", err)
	}

	// Rename: happy path + not-found + empty-name rejection.
	if err := svc.Rename(ctx, cash.ID, name("Main Checking")); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := svc.Rename(ctx, -1, name("Ghost")); !errors.Is(err, ErrNotFound) {
		t.Errorf("rename missing = %v; want ErrNotFound", err)
	}
	if err := svc.Rename(ctx, cash.ID, "   "); !errors.Is(err, ErrEmptyName) {
		t.Errorf("rename empty = %v; want ErrEmptyName", err)
	}

	// Archive excludes from the default list; SetArchived(false) restores it.
	if err := svc.SetArchived(ctx, credit.ID, true); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if err := svc.SetArchived(ctx, -1, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("archive missing = %v; want ErrNotFound", err)
	}

	active, err := svc.List(ctx, false)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if has(active, credit.ID) {
		t.Errorf("archived credit should be absent from the default (active) list")
	}
	if !has(active, cash.ID) || !has(active, invest.ID) {
		t.Errorf("active list should contain the non-archived cash and investment accounts")
	}

	all, err := svc.List(ctx, true)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if !has(all, credit.ID) {
		t.Errorf("archived credit should appear in the include-archived list")
	}

	// Unarchive restores it to the active list.
	if err := svc.SetArchived(ctx, credit.ID, false); err != nil {
		t.Fatalf("unarchive: %v", err)
	}
	active2, err := svc.List(ctx, false)
	if err != nil {
		t.Fatalf("list active after unarchive: %v", err)
	}
	if !has(active2, credit.ID) {
		t.Errorf("unarchived credit should be back in the active list")
	}
}

func has(accts []Account, id int64) bool {
	for _, a := range accts {
		if a.ID == id {
			return true
		}
	}
	return false
}
