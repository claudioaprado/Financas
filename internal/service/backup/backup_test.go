package backup

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/db"
	"github.com/claudioaprado/financas/internal/money"
	"github.com/claudioaprado/financas/internal/service/account"
	"github.com/claudioaprado/financas/internal/service/category"
	"github.com/claudioaprado/financas/internal/service/exchangerate"
	"github.com/claudioaprado/financas/internal/service/price"
	"github.com/claudioaprado/financas/internal/service/security"
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
	t.Skip("set TEST_DATABASE_URL or DATABASE_URL to run backup integration tests")
	return ""
}

// isolatedDB creates a throwaway database for this test and returns its URL; the
// cleanup drops it. The backup export reads EVERY table plus the global
// Display-Currency singleton, so — like the valuation tests — it cannot share
// the base DB without racing concurrent settings tests. A private database gives
// a pristine, uncontended schema.
func isolatedDB(t *testing.T, baseURL string) string {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("fin_backup_test_%d", time.Now().UnixNano())

	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatalf("create database: %v", err)
	}
	admin.Close()

	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}
	u.Path = "/" + name
	testURL := u.String()

	t.Cleanup(func() {
		a, err := pgxpool.New(ctx, baseURL)
		if err != nil {
			return
		}
		defer a.Close()
		_, _ = a.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	return testURL
}

func req(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func dt(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	u := isolatedDB(t, testDatabaseURL(t))
	if err := store.Migrate(ctx, u, db.Migrations); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := store.NewPool(ctx, u)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestExportFullFidelity seeds a representative instance (cash + cross-currency
// investment accounts incl. an archived one, a category, securities, an exchange
// rate, a price, and transactions covering income/expense/transfer/buy/sell/
// dividend) and asserts the export reproduces every authored row faithfully:
// matching PKs, decimal strings, null-vs-set pointers, created_at present, and
// the schema/version tag.
func TestExportFullFidelity(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)

	accts := account.New(pool)
	cats := category.New(pool)
	secs := security.New(pool)
	txns := transaction.New(pool)
	prices := price.New(pool)
	rates := exchangerate.New(pool)
	set := settings.New(pool)
	svc := New(pool)

	if err := set.SetDisplayCurrency(ctx, money.BRL); err != nil {
		t.Fatalf("set display currency: %v", err)
	}

	cashUSD, err := accts.Create(ctx, "CashUSD", account.Cash, money.USD)
	if err != nil {
		t.Fatalf("create cashUSD: %v", err)
	}
	cashBRL, err := accts.Create(ctx, "CashBRL", account.Cash, money.BRL)
	if err != nil {
		t.Fatalf("create cashBRL: %v", err)
	}
	brokerUSD, err := accts.Create(ctx, "BrokerUSD", account.Investment, money.USD)
	if err != nil {
		t.Fatalf("create brokerUSD: %v", err)
	}
	archived, err := accts.Create(ctx, "OldAccount", account.Cash, money.USD)
	if err != nil {
		t.Fatalf("create archived: %v", err)
	}
	if err := accts.SetArchived(ctx, archived.ID, true); err != nil {
		t.Fatalf("archive: %v", err)
	}

	salary, err := cats.Create(ctx, "Salary", category.Income)
	if err != nil {
		t.Fatalf("create category: %v", err)
	}

	sec, err := secs.Create(ctx, "ACME", "Acme Corp", security.Stock, money.USD)
	if err != nil {
		t.Fatalf("create security: %v", err)
	}

	if _, err := rates.Add(ctx, money.USD, money.BRL, dt(t, "2026-06-01"), req("5.1234")); err != nil {
		t.Fatalf("add rate: %v", err)
	}
	if _, err := prices.Add(ctx, sec.ID, dt(t, "2026-06-02"), req("123.4500")); err != nil {
		t.Fatalf("add price: %v", err)
	}

	// income (to-only, categorized), expense (from-only, uncategorized),
	// transfer (both sides), buy + dividend (security set).
	if _, err := txns.Record(ctx, cashUSD.ID, transaction.Income, req("1000"), dt(t, "2026-06-03"), "pay", salary.ID); err != nil {
		t.Fatalf("income: %v", err)
	}
	if _, err := txns.Record(ctx, cashUSD.ID, transaction.Expense, req("40.25"), dt(t, "2026-06-04"), "lunch", 0); err != nil {
		t.Fatalf("expense: %v", err)
	}
	if err := txns.Transfer(ctx, cashUSD.ID, cashBRL.ID, req("100"), req("512.34"), dt(t, "2026-06-05"), "move"); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if _, err := txns.Buy(ctx, brokerUSD.ID, sec.ID, req("3"), req("100"), req("1.50"), dt(t, "2026-06-06"), "buy acme"); err != nil {
		t.Fatalf("buy: %v", err)
	}
	if _, err := txns.Dividend(ctx, brokerUSD.ID, sec.ID, req("12.00"), dt(t, "2026-06-07"), "div"); err != nil {
		t.Fatalf("dividend: %v", err)
	}

	exp, err := svc.Export(ctx)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	// Schema / version tag.
	if exp.Schema != ExportSchema || exp.Version != ExportVersion {
		t.Errorf("schema/version = %q/%d, want %q/%d", exp.Schema, exp.Version, ExportSchema, ExportVersion)
	}
	if exp.DisplayCurrency != "BRL" {
		t.Errorf("display currency = %q, want BRL", exp.DisplayCurrency)
	}
	if exp.ExportedAt == "" {
		t.Error("exported_at is empty")
	}

	// Accounts: 4, ordered by id, archived flag preserved, created_at present.
	if len(exp.Accounts) != 4 {
		t.Fatalf("accounts = %d, want 4", len(exp.Accounts))
	}
	if exp.Accounts[0].ID != cashUSD.ID || exp.Accounts[0].Currency != "USD" || exp.Accounts[0].Type != "cash" {
		t.Errorf("account[0] = %+v", exp.Accounts[0])
	}
	if !exp.Accounts[3].Archived {
		t.Errorf("account[3] should be archived: %+v", exp.Accounts[3])
	}
	for i, a := range exp.Accounts {
		if a.CreatedAt == "" {
			t.Errorf("account[%d] created_at empty", i)
		}
	}

	// Category.
	if len(exp.Categories) != 1 || exp.Categories[0].ID != salary.ID || exp.Categories[0].Kind != "income" {
		t.Errorf("categories = %+v", exp.Categories)
	}

	// Security.
	if len(exp.Securities) != 1 || exp.Securities[0].Symbol != "ACME" || exp.Securities[0].QuoteCurrency != "USD" {
		t.Errorf("securities = %+v", exp.Securities)
	}

	// Exchange rate: decimal kept as string.
	if len(exp.ExchangeRates) != 1 {
		t.Fatalf("exchange rates = %d, want 1", len(exp.ExchangeRates))
	}
	if got := exp.ExchangeRates[0].Rate; got != "5.1234" {
		t.Errorf("rate = %q, want 5.1234", got)
	}
	if got := exp.ExchangeRates[0].EffectiveDate; got != "2026-06-01" {
		t.Errorf("rate effective_date = %q, want 2026-06-01", got)
	}

	// Price: decimal string.
	if len(exp.Prices) != 1 || exp.Prices[0].SecurityID != sec.ID {
		t.Fatalf("prices = %+v", exp.Prices)
	}
	if got := exp.Prices[0].Price; got != "123.45" && got != "123.4500" {
		t.Errorf("price = %q, want 123.45(00)", got)
	}

	// Transactions: 5 rows (transfer is a single row, AD-9). Verify null-vs-set
	// pointers and decimal strings.
	if len(exp.Transactions) != 5 {
		t.Fatalf("transactions = %d, want 5", len(exp.Transactions))
	}
	byType := map[string]TransactionDTO{}
	for _, tr := range exp.Transactions {
		byType[tr.Type] = tr
	}

	income := byType["income"]
	if income.ToAccountID == nil || *income.ToAccountID != cashUSD.ID {
		t.Errorf("income to_account_id = %v, want %d", income.ToAccountID, cashUSD.ID)
	}
	if income.FromAccountID != nil {
		t.Errorf("income from_account_id = %v, want nil", income.FromAccountID)
	}
	if income.CategoryID == nil || *income.CategoryID != salary.ID {
		t.Errorf("income category_id = %v, want %d", income.CategoryID, salary.ID)
	}
	if income.SecurityID != nil {
		t.Errorf("income security_id = %v, want nil", income.SecurityID)
	}

	expense := byType["expense"]
	if expense.FromAccountID == nil || *expense.FromAccountID != cashUSD.ID {
		t.Errorf("expense from_account_id = %v, want %d", expense.FromAccountID, cashUSD.ID)
	}
	if expense.ToAccountID != nil {
		t.Errorf("expense to_account_id = %v, want nil", expense.ToAccountID)
	}
	if expense.CategoryID != nil {
		t.Errorf("expense category_id = %v, want nil (uncategorized)", expense.CategoryID)
	}

	transfer := byType["transfer"]
	if transfer.FromAccountID == nil || transfer.ToAccountID == nil {
		t.Errorf("transfer should have both account ids: %+v", transfer)
	}

	buy := byType["buy"]
	if buy.SecurityID == nil || *buy.SecurityID != sec.ID {
		t.Errorf("buy security_id = %v, want %d", buy.SecurityID, sec.ID)
	}
	if buy.Quantity != "3" {
		t.Errorf("buy quantity = %q, want 3", buy.Quantity)
	}
	if buy.Fees != "1.5" && buy.Fees != "1.5000" {
		t.Errorf("buy fees = %q, want 1.5(000)", buy.Fees)
	}

	if _, ok := byType["dividend"]; !ok {
		t.Error("dividend transaction missing from export")
	}
}

// TestExportEmptyInstance asserts a fresh instance exports a well-formed file
// with the schema/version/display-currency set and all six arrays non-nil empty
// (so JSON emits [], not null).
func TestExportEmptyInstance(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	svc := New(pool)

	exp, err := svc.Export(ctx)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if exp.Schema != ExportSchema || exp.Version != ExportVersion {
		t.Errorf("schema/version = %q/%d", exp.Schema, exp.Version)
	}
	if exp.DisplayCurrency == "" {
		t.Error("display currency empty on fresh instance")
	}
	if exp.Accounts == nil || exp.Categories == nil || exp.Securities == nil ||
		exp.ExchangeRates == nil || exp.Prices == nil || exp.Transactions == nil {
		t.Errorf("a slice is nil on empty instance: %+v", exp)
	}
	if len(exp.Accounts) != 0 || len(exp.Transactions) != 0 {
		t.Errorf("expected empty arrays, got accounts=%d transactions=%d", len(exp.Accounts), len(exp.Transactions))
	}
}
