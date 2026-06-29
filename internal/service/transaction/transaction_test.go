package transaction

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
	"github.com/claudioaprado/financas/internal/store"
)

func testDatabaseURL(t *testing.T) string {
	t.Helper()
	for _, k := range []string{"TEST_DATABASE_URL", "DATABASE_URL"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	t.Skip("set TEST_DATABASE_URL or DATABASE_URL to run transaction integration tests")
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

func TestTransaction(t *testing.T) {
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
	svc := New(pool)

	run := time.Now().UnixNano()
	cash, err := accts.Create(ctx, fmt.Sprintf("Wallet-%d", run), account.Cash, money.USD)
	if err != nil {
		t.Fatalf("create cash account: %v", err)
	}

	wantBalance := func(label, want string) {
		t.Helper()
		bal, err := svc.Balance(ctx, cash.ID)
		if err != nil {
			t.Fatalf("balance (%s): %v", label, err)
		}
		if !bal.Amount().Equal(decimal.RequireFromString(want)) {
			t.Errorf("balance (%s) = %s, want %s USD", label, bal.Amount(), want)
		}
		if bal.Currency() != money.USD {
			t.Errorf("balance currency = %s, want USD", bal.Currency())
		}
	}

	// Income 100, expense 30 -> 70.
	inc, err := svc.Record(ctx, cash.ID, Income, decimal.RequireFromString("100"), d(t, "2024-01-05"), "salary")
	if err != nil {
		t.Fatalf("record income: %v", err)
	}
	exp, err := svc.Record(ctx, cash.ID, Expense, decimal.RequireFromString("30"), d(t, "2024-01-06"), "groceries")
	if err != nil {
		t.Fatalf("record expense: %v", err)
	}
	wantBalance("after income+expense", "70")

	// Stored as non-negative magnitudes; direction from type.
	if !inc.Amount.Equal(decimal.RequireFromString("100")) || inc.Type != Income {
		t.Errorf("income row = %+v; want magnitude 100, type income", inc)
	}
	if !exp.Amount.Equal(decimal.RequireFromString("30")) || exp.Type != Expense {
		t.Errorf("expense row = %+v; want magnitude 30, type expense", exp)
	}

	// Edit the expense 30 -> 50 -> balance 50.
	if err := svc.Edit(ctx, cash.ID, exp.ID, Expense, decimal.RequireFromString("50"), d(t, "2024-01-06"), "groceries"); err != nil {
		t.Fatalf("edit expense: %v", err)
	}
	wantBalance("after editing expense to 50", "50")

	// Delete the income -> balance -50.
	if err := svc.Delete(ctx, inc.ID); err != nil {
		t.Fatalf("delete income: %v", err)
	}
	wantBalance("after deleting income", "-50")

	// List returns the remaining (edited) expense.
	list, err := svc.List(ctx, cash.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != exp.ID || !list[0].Amount.Equal(decimal.RequireFromString("50")) {
		t.Errorf("list = %+v; want one expense of 50", list)
	}

	// Validation.
	credit, err := accts.Create(ctx, fmt.Sprintf("Card-%d", run), account.Credit, money.USD)
	if err != nil {
		t.Fatalf("create credit account: %v", err)
	}
	if _, err := svc.Record(ctx, credit.ID, Expense, decimal.RequireFromString("10"), d(t, "2024-01-07"), ""); !errors.Is(err, ErrNotCashAccount) {
		t.Errorf("expense on credit = %v; want ErrNotCashAccount", err)
	}
	if _, err := svc.Record(ctx, cash.ID, Income, decimal.RequireFromString("0"), d(t, "2024-01-07"), ""); !errors.Is(err, ErrNonPositiveAmount) {
		t.Errorf("zero amount = %v; want ErrNonPositiveAmount", err)
	}
	if _, err := svc.Record(ctx, cash.ID, TxType("transfer"), decimal.RequireFromString("10"), d(t, "2024-01-07"), ""); !errors.Is(err, ErrInvalidType) {
		t.Errorf("invalid type = %v; want ErrInvalidType", err)
	}
	if _, err := svc.Record(ctx, -1, Income, decimal.RequireFromString("10"), d(t, "2024-01-07"), ""); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("missing account = %v; want ErrAccountNotFound", err)
	}
	if err := svc.Edit(ctx, cash.ID, -1, Income, decimal.RequireFromString("10"), d(t, "2024-01-07"), ""); !errors.Is(err, ErrTxNotFound) {
		t.Errorf("edit missing tx = %v; want ErrTxNotFound", err)
	}
	if err := svc.Delete(ctx, -1); !errors.Is(err, ErrTxNotFound) {
		t.Errorf("delete missing tx = %v; want ErrTxNotFound", err)
	}
}
