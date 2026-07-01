package importer

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
	t.Skip("set TEST_DATABASE_URL or DATABASE_URL to run importer integration tests")
	return ""
}

func TestImport(t *testing.T) {
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

	accts := account.New(pool)
	txns := transaction.New(pool)
	svc := New(pool)
	run := time.Now().UnixNano()

	cash, err := accts.Create(ctx, fmt.Sprintf("Import-%d", run), account.Cash, money.USD)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	// 2 valid (income 5000, expense 1234.56) + 1 error (bad date).
	content := "15/03/2024\tSalary\t5.000,00\n" +
		"01/02/24\tGrocery\t-1.234,56\n" +
		"31/02/24\tBad\t10,00\n"

	prev, err := svc.Preview(ctx, cash.ID, content)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if prev.New != 2 || prev.Errors != 1 || prev.Duplicate != 0 {
		t.Fatalf("preview = %d new / %d dup / %d err; want 2/0/1", prev.New, prev.Duplicate, prev.Errors)
	}

	res, err := svc.Commit(ctx, cash.ID, content, nil)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if res.New != 2 {
		t.Fatalf("commit inserted %d; want 2", res.New)
	}

	// Balance = +5000 - 1234.56 = 3765.44 USD.
	bal, err := txns.Balance(ctx, cash.ID)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if !bal.Amount().Equal(decimal.RequireFromString("3765.44")) {
		t.Errorf("balance after import = %s; want 3765.44", bal.Amount())
	}

	// Re-import the same content: everything is a duplicate, nothing inserted.
	prev2, err := svc.Preview(ctx, cash.ID, content)
	if err != nil {
		t.Fatalf("preview 2: %v", err)
	}
	if prev2.New != 0 || prev2.Duplicate != 2 {
		t.Errorf("re-preview = %d new / %d dup; want 0/2", prev2.New, prev2.Duplicate)
	}
	res2, err := svc.Commit(ctx, cash.ID, content, nil)
	if err != nil {
		t.Fatalf("commit 2: %v", err)
	}
	if res2.New != 0 {
		t.Errorf("re-commit inserted %d; want 0 (idempotent)", res2.New)
	}
	if bal2, _ := txns.Balance(ctx, cash.ID); !bal2.Amount().Equal(decimal.RequireFromString("3765.44")) {
		t.Errorf("balance changed on re-import = %s; want 3765.44", bal2.Amount())
	}

	// Non-cash/credit account is rejected.
	inv, err := accts.Create(ctx, fmt.Sprintf("ImportInv-%d", run), account.Investment, money.USD)
	if err != nil {
		t.Fatalf("create investment: %v", err)
	}
	if _, err := svc.Preview(ctx, inv.ID, content); !errors.Is(err, ErrUnsupportedAccountType) {
		t.Errorf("import to investment = %v; want ErrUnsupportedAccountType", err)
	}
}
