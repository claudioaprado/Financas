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
	inc, err := svc.Record(ctx, cash.ID, Income, decimal.RequireFromString("100"), d(t, "2024-01-05"), "salary", 0)
	if err != nil {
		t.Fatalf("record income: %v", err)
	}
	exp, err := svc.Record(ctx, cash.ID, Expense, decimal.RequireFromString("30"), d(t, "2024-01-06"), "groceries", 0)
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
	if err := svc.Edit(ctx, cash.ID, exp.ID, Expense, decimal.RequireFromString("50"), d(t, "2024-01-06"), "groceries", 0); err != nil {
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

	// Credit accounts: an expense increases the balance owed (signed balance goes
	// negative); income (a refund) reduces it.
	credit, err := accts.Create(ctx, fmt.Sprintf("Card-%d", run), account.Credit, money.USD)
	if err != nil {
		t.Fatalf("create credit account: %v", err)
	}
	if _, err := svc.Record(ctx, credit.ID, Expense, decimal.RequireFromString("200"), d(t, "2024-01-07"), "tv", 0); err != nil {
		t.Fatalf("expense on credit: %v", err)
	}
	if bal, err := svc.Balance(ctx, credit.ID); err != nil || !bal.Amount().Equal(decimal.RequireFromString("-200")) {
		t.Errorf("credit balance after expense = %v, %v; want -200 (owed 200)", bal.Amount(), err)
	}
	if _, err := svc.Record(ctx, credit.ID, Income, decimal.RequireFromString("50"), d(t, "2024-01-08"), "refund", 0); err != nil {
		t.Fatalf("refund on credit: %v", err)
	}
	if bal, err := svc.Balance(ctx, credit.ID); err != nil || !bal.Amount().Equal(decimal.RequireFromString("-150")) {
		t.Errorf("credit balance after refund = %v, %v; want -150 (owed 150)", bal.Amount(), err)
	}

	// Investment accounts reject plain income/expense (Epic 4 handles their cash flow).
	invest, err := accts.Create(ctx, fmt.Sprintf("Broker-%d", run), account.Investment, money.USD)
	if err != nil {
		t.Fatalf("create investment account: %v", err)
	}
	if _, err := svc.Record(ctx, invest.ID, Expense, decimal.RequireFromString("10"), d(t, "2024-01-07"), "", 0); !errors.Is(err, ErrUnsupportedAccountType) {
		t.Errorf("expense on investment = %v; want ErrUnsupportedAccountType", err)
	}

	// Validation.
	if _, err := svc.Record(ctx, cash.ID, Income, decimal.RequireFromString("0"), d(t, "2024-01-07"), "", 0); !errors.Is(err, ErrNonPositiveAmount) {
		t.Errorf("zero amount = %v; want ErrNonPositiveAmount", err)
	}
	if _, err := svc.Record(ctx, cash.ID, TxType("transfer"), decimal.RequireFromString("10"), d(t, "2024-01-07"), "", 0); !errors.Is(err, ErrInvalidType) {
		t.Errorf("invalid type = %v; want ErrInvalidType", err)
	}
	if _, err := svc.Record(ctx, -1, Income, decimal.RequireFromString("10"), d(t, "2024-01-07"), "", 0); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("missing account = %v; want ErrAccountNotFound", err)
	}
	if err := svc.Edit(ctx, cash.ID, -1, Income, decimal.RequireFromString("10"), d(t, "2024-01-07"), "", 0); !errors.Is(err, ErrTxNotFound) {
		t.Errorf("edit missing tx = %v; want ErrTxNotFound", err)
	}
	if err := svc.Delete(ctx, -1); !errors.Is(err, ErrTxNotFound) {
		t.Errorf("delete missing tx = %v; want ErrTxNotFound", err)
	}
}

func TestCategoryAssignment(t *testing.T) {
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

	usd, err := accts.Create(ctx, fmt.Sprintf("CatUSD-%d", run), account.Cash, money.USD)
	if err != nil {
		t.Fatalf("create usd account: %v", err)
	}
	brl, err := accts.Create(ctx, fmt.Sprintf("CatBRL-%d", run), account.Cash, money.BRL)
	if err != nil {
		t.Fatalf("create brl account: %v", err)
	}

	// Create an expense category directly via store (avoids importing service/category).
	cat, err := store.New(pool).CreateCategory(ctx, store.CreateCategoryParams{Name: fmt.Sprintf("Food-%d", run), Kind: "expense"})
	if err != nil {
		t.Fatalf("create category: %v", err)
	}

	// Assigning an expense category to an income is rejected.
	if _, err := svc.Record(ctx, usd.ID, Income, decimal.RequireFromString("100"), d(t, "2024-06-01"), "x", cat.ID); !errors.Is(err, ErrCategoryKindMismatch) {
		t.Errorf("expense category on income = %v; want ErrCategoryKindMismatch", err)
	}
	// A missing category is rejected.
	if _, err := svc.Record(ctx, usd.ID, Expense, decimal.RequireFromString("100"), d(t, "2024-06-01"), "x", -1); !errors.Is(err, ErrCategoryNotFound) {
		t.Errorf("missing category = %v; want ErrCategoryNotFound", err)
	}

	// Assign it to two expenses in different currencies.
	if _, err := svc.Record(ctx, usd.ID, Expense, decimal.RequireFromString("30"), d(t, "2024-06-01"), "lunch", cat.ID); err != nil {
		t.Fatalf("record usd expense: %v", err)
	}
	if _, err := svc.Record(ctx, brl.ID, Expense, decimal.RequireFromString("70"), d(t, "2024-06-02"), "jantar", cat.ID); err != nil {
		t.Fatalf("record brl expense: %v", err)
	}

	// The register row carries the resolved category name.
	rows, _ := svc.List(ctx, usd.ID)
	if len(rows) == 0 || rows[0].CategoryID != cat.ID || rows[0].CategoryName != cat.Name {
		t.Errorf("usd register row = %+v; want category %d/%q", rows, cat.ID, cat.Name)
	}

	// CategoryTransactions returns both rows and per-currency totals.
	txns, totals, err := svc.CategoryTransactions(ctx, cat.ID)
	if err != nil {
		t.Fatalf("category transactions: %v", err)
	}
	if len(txns) != 2 {
		t.Errorf("category txns = %d; want 2", len(txns))
	}
	got := map[money.Currency]decimal.Decimal{}
	for _, m := range totals {
		got[m.Currency()] = m.Amount()
	}
	if !got[money.USD].Equal(decimal.RequireFromString("30")) || !got[money.BRL].Equal(decimal.RequireFromString("70")) {
		t.Errorf("category totals = %v; want 30 USD, 70 BRL", got)
	}
}

func TestTransfer(t *testing.T) {
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
	mk := func(label string, typ account.AccountType, cur money.Currency) account.Account {
		a, err := accts.Create(ctx, fmt.Sprintf("%s-%d", label, run), typ, cur)
		if err != nil {
			t.Fatalf("create %s: %v", label, err)
		}
		return a
	}
	bal := func(a account.Account) decimal.Decimal {
		b, err := svc.Balance(ctx, a.ID)
		if err != nil {
			t.Fatalf("balance %d: %v", a.ID, err)
		}
		return b.Amount()
	}

	checking := mk("Checking", account.Cash, money.USD)
	savings := mk("Savings", account.Cash, money.USD)
	card := mk("Card", account.Credit, money.USD)
	brl := mk("Brl", account.Cash, money.BRL)

	// Same-currency: 200 Checking -> Savings. Source -200, dest +200; one row,
	// from_amount == to_amount.
	if err := svc.Transfer(ctx, checking.ID, savings.ID, decimal.RequireFromString("200"), decimal.Zero, d(t, "2024-04-01"), "move"); err != nil {
		t.Fatalf("same-currency transfer: %v", err)
	}
	if !bal(checking).Equal(decimal.RequireFromString("-200")) || !bal(savings).Equal(decimal.RequireFromString("200")) {
		t.Errorf("after transfer: checking=%s savings=%s; want -200 / 200", bal(checking), bal(savings))
	}
	rows, _ := svc.List(ctx, savings.ID)
	if len(rows) != 1 || rows[0].Type != Transfer || rows[0].Counterparty != checking.Name || !rows[0].Incoming {
		t.Errorf("savings register = %+v; want one incoming transfer from %q", rows, checking.Name)
	}
	if !rows[0].Amount.Equal(decimal.RequireFromString("200")) {
		t.Errorf("transfer to_amount = %s; want 200 (same-currency from==to)", rows[0].Amount)
	}

	// Pay the card: 150 Checking -> Card reduces owed (card balance += 150 toward 0).
	if err := svc.Transfer(ctx, checking.ID, card.ID, decimal.RequireFromString("150"), decimal.Zero, d(t, "2024-04-02"), "pay card"); err != nil {
		t.Fatalf("transfer to credit: %v", err)
	}
	if !bal(card).Equal(decimal.RequireFromString("150")) { // credited the credit account (owed -150)
		t.Errorf("card balance after payment = %s; want 150", bal(card))
	}

	// Cross-currency: 100 USD Checking -> 520 BRL. Source -100 USD, dest +520 BRL.
	if err := svc.Transfer(ctx, checking.ID, brl.ID, decimal.RequireFromString("100"), decimal.RequireFromString("520"), d(t, "2024-04-03"), "fx"); err != nil {
		t.Fatalf("cross-currency transfer: %v", err)
	}
	if !bal(brl).Equal(decimal.RequireFromString("520")) {
		t.Errorf("brl balance = %s; want 520", bal(brl))
	}
	// Checking: -200 -150 -100 = -450.
	if !bal(checking).Equal(decimal.RequireFromString("-450")) {
		t.Errorf("checking after three transfers = %s; want -450", bal(checking))
	}

	// Validation.
	if err := svc.Transfer(ctx, checking.ID, checking.ID, decimal.RequireFromString("10"), decimal.Zero, d(t, "2024-04-04"), ""); !errors.Is(err, ErrSameAccount) {
		t.Errorf("same-account transfer = %v; want ErrSameAccount", err)
	}
	if err := svc.Transfer(ctx, checking.ID, -1, decimal.RequireFromString("10"), decimal.Zero, d(t, "2024-04-04"), ""); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("missing dest = %v; want ErrAccountNotFound", err)
	}
	if err := svc.Transfer(ctx, checking.ID, savings.ID, decimal.RequireFromString("0"), decimal.Zero, d(t, "2024-04-04"), ""); !errors.Is(err, ErrNonPositiveAmount) {
		t.Errorf("non-positive from = %v; want ErrNonPositiveAmount", err)
	}
	if err := svc.Transfer(ctx, checking.ID, savings.ID, decimal.RequireFromString("10"), decimal.RequireFromString("11"), d(t, "2024-04-04"), ""); !errors.Is(err, ErrSameCurrencyAmountMismatch) {
		t.Errorf("same-currency mismatch = %v; want ErrSameCurrencyAmountMismatch", err)
	}
	if err := svc.Transfer(ctx, checking.ID, brl.ID, decimal.RequireFromString("10"), decimal.Zero, d(t, "2024-04-04"), ""); !errors.Is(err, ErrCrossCurrencyToAmountRequired) {
		t.Errorf("cross-currency missing to = %v; want ErrCrossCurrencyToAmountRequired", err)
	}
}
