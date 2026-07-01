package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/db"
	"github.com/claudioaprado/financas/internal/money"
	"github.com/claudioaprado/financas/internal/service/account"
	"github.com/claudioaprado/financas/internal/service/assetcategory"
	"github.com/claudioaprado/financas/internal/service/budget"
	"github.com/claudioaprado/financas/internal/service/category"
	"github.com/claudioaprado/financas/internal/service/categoryrule"
	"github.com/claudioaprado/financas/internal/service/exchangerate"
	"github.com/claudioaprado/financas/internal/service/price"
	"github.com/claudioaprado/financas/internal/service/recurring"
	"github.com/claudioaprado/financas/internal/service/security"
	"github.com/claudioaprado/financas/internal/service/settings"
	"github.com/claudioaprado/financas/internal/service/transaction"
	"github.com/claudioaprado/financas/internal/service/valuation"
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
		exp.ExchangeRates == nil || exp.Prices == nil || exp.Transactions == nil ||
		exp.Budgets == nil || exp.CategoryRules == nil || exp.Recurring == nil ||
		exp.Tags == nil || exp.TransactionTags == nil || exp.AssetCategories == nil {
		t.Errorf("a slice is nil on empty instance: %+v", exp)
	}
	if len(exp.Accounts) != 0 || len(exp.Transactions) != 0 {
		t.Errorf("expected empty arrays, got accounts=%d transactions=%d", len(exp.Accounts), len(exp.Transactions))
	}
}

// --- Story 6.2: Restore ---

// seedSample populates a representative cross-currency instance (Display = BRL)
// and returns the USD broker investment account id (for balance/holdings
// assertions). It mirrors the export-fidelity fixture: cash + investment
// accounts, a category, a security, a rate, a price, and income/expense/
// transfer/buy/sell/dividend.
func seedSample(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	ctx := context.Background()
	accts := account.New(pool)
	cats := category.New(pool)
	secs := security.New(pool)
	txns := transaction.New(pool)
	prices := price.New(pool)
	rates := exchangerate.New(pool)
	set := settings.New(pool)

	if err := set.SetDisplayCurrency(ctx, money.BRL); err != nil {
		t.Fatalf("set display: %v", err)
	}
	cashUSD, err := accts.Create(ctx, "CashUSD", account.Cash, money.USD)
	if err != nil {
		t.Fatalf("cashUSD: %v", err)
	}
	cashBRL, err := accts.Create(ctx, "CashBRL", account.Cash, money.BRL)
	if err != nil {
		t.Fatalf("cashBRL: %v", err)
	}
	broker, err := accts.Create(ctx, "BrokerUSD", account.Investment, money.USD)
	if err != nil {
		t.Fatalf("broker: %v", err)
	}
	salary, err := cats.Create(ctx, "Salary", category.Income)
	if err != nil {
		t.Fatalf("category: %v", err)
	}
	sec, err := secs.Create(ctx, "ACME", "Acme Corp", security.Stock, money.USD)
	if err != nil {
		t.Fatalf("security: %v", err)
	}
	if _, err := rates.Add(ctx, money.USD, money.BRL, dt(t, "2026-06-01"), req("5")); err != nil {
		t.Fatalf("rate: %v", err)
	}
	if _, err := prices.Add(ctx, sec.ID, dt(t, "2026-06-02"), req("120")); err != nil {
		t.Fatalf("price: %v", err)
	}
	inc, err := txns.Record(ctx, cashUSD.ID, transaction.Income, req("1000"), dt(t, "2026-06-03"), "pay", salary.ID)
	if err != nil {
		t.Fatalf("income: %v", err)
	}
	if _, err := txns.Record(ctx, cashUSD.ID, transaction.Expense, req("40.25"), dt(t, "2026-06-04"), "lunch", 0); err != nil {
		t.Fatalf("expense: %v", err)
	}
	// An owner-defined asset category (assigned to a security) must survive the
	// round-trip too — exercising security.asset_category_id.
	ac, err := assetcategory.New(pool).Create(ctx, "Ações BR")
	if err != nil {
		t.Fatalf("asset category: %v", err)
	}
	if err := secs.SetCategory(ctx, sec.ID, ac.ID); err != nil {
		t.Fatalf("assign category: %v", err)
	}
	// Phase-2 authored tables (Epics 8-10) must survive the round-trip too.
	if err := budget.New(pool).Set(ctx, salary.ID, req("2000")); err != nil {
		t.Fatalf("budget: %v", err)
	}
	if _, err := categoryrule.New(pool).Add(ctx, "bonus", salary.ID); err != nil {
		t.Fatalf("category rule: %v", err)
	}
	if _, err := recurring.New(pool).Create(ctx, recurring.Input{
		Type: recurring.Expense, AccountID: cashUSD.ID, Amount: req("50"),
		Cadence: "months", IntervalN: 1, StartDate: dt(t, "2026-06-10"), Description: "rent",
	}); err != nil {
		t.Fatalf("recurring: %v", err)
	}
	// A note + tags on the income row (exercises tag + transaction_tag).
	if err := txns.Annotate(ctx, inc.ID, "first paycheck", []string{"work", "salary"}); err != nil {
		t.Fatalf("annotate: %v", err)
	}
	if err := txns.Transfer(ctx, cashUSD.ID, cashBRL.ID, req("100"), req("500"), dt(t, "2026-06-05"), "move"); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if _, err := txns.Buy(ctx, broker.ID, sec.ID, req("3"), req("100"), req("1.50"), dt(t, "2026-06-06"), "buy"); err != nil {
		t.Fatalf("buy: %v", err)
	}
	if _, err := txns.Dividend(ctx, broker.ID, sec.ID, req("12.00"), dt(t, "2026-06-07"), "div"); err != nil {
		t.Fatalf("dividend: %v", err)
	}
	return broker.ID
}

// TestRestoreRoundTrip is the crown jewel (AC#1, AC#2): seed instance A, export
// it, restore into a FRESH instance B, and assert B's derived Net Worth /
// portfolio value reproduce A's exactly, with preserved primary keys and
// created_at — all from authored data alone.
func TestRestoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	poolA := newPool(t)
	brokerID := seedSample(t, poolA)

	srcVal, err := valuation.New(poolA).Portfolio(ctx)
	if err != nil {
		t.Fatalf("source portfolio: %v", err)
	}
	// Capture the broker account's derived balance + holdings (AC#2 names both).
	srcBal, err := transaction.New(poolA).Balance(ctx, brokerID)
	if err != nil {
		t.Fatalf("source balance: %v", err)
	}
	srcHoldings, srcRealized, _, err := transaction.New(poolA).Holdings(ctx, brokerID)
	if err != nil {
		t.Fatalf("source holdings: %v", err)
	}
	srcExp, err := New(poolA).Export(ctx)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	raw, err := json.Marshal(srcExp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Fresh, migrated, empty instance B.
	poolB := newPool(t)
	sum, err := New(poolB).Restore(ctx, raw)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if sum.Accounts != len(srcExp.Accounts) || sum.Transactions != len(srcExp.Transactions) ||
		sum.Securities != len(srcExp.Securities) || sum.Prices != len(srcExp.Prices) ||
		sum.ExchangeRates != len(srcExp.ExchangeRates) || sum.Categories != len(srcExp.Categories) ||
		sum.Budgets != len(srcExp.Budgets) || sum.CategoryRules != len(srcExp.CategoryRules) ||
		sum.Recurring != len(srcExp.Recurring) || sum.Tags != len(srcExp.Tags) ||
		sum.TransactionTags != len(srcExp.TransactionTags) ||
		sum.AssetCategories != len(srcExp.AssetCategories) {
		t.Fatalf("summary %+v does not match source counts", sum)
	}
	// The Phase-2 tables + asset categories must be non-empty in the fixture, else
	// the round-trip below would vacuously "pass" without exercising them.
	if len(srcExp.Budgets) == 0 || len(srcExp.CategoryRules) == 0 || len(srcExp.Recurring) == 0 ||
		len(srcExp.Tags) == 0 || len(srcExp.TransactionTags) == 0 || len(srcExp.AssetCategories) == 0 {
		t.Fatalf("fixture missing Phase-2 rows: %+v", sum)
	}

	// B's export must be byte-identical to A's except the exported_at stamp:
	// same PKs, created_at, and rows (preserved identity, AC#1).
	dstExp, err := New(poolB).Export(ctx)
	if err != nil {
		t.Fatalf("re-export: %v", err)
	}
	srcExp.ExportedAt, dstExp.ExportedAt = "", ""
	srcJSON, _ := json.Marshal(srcExp)
	dstJSON, _ := json.Marshal(dstExp)
	if string(srcJSON) != string(dstJSON) {
		t.Errorf("restored export differs from source:\n src=%s\n dst=%s", srcJSON, dstJSON)
	}

	// Derived figures reproduce on read (AC#2, NFR-2/AD-2).
	dstVal, err := valuation.New(poolB).Portfolio(ctx)
	if err != nil {
		t.Fatalf("dest portfolio: %v", err)
	}
	if srcVal.NetWorth.String() != dstVal.NetWorth.String() {
		t.Errorf("NetWorth: src %s != dst %s", srcVal.NetWorth.String(), dstVal.NetWorth.String())
	}
	if srcVal.PortfolioValue.String() != dstVal.PortfolioValue.String() {
		t.Errorf("PortfolioValue: src %s != dst %s", srcVal.PortfolioValue.String(), dstVal.PortfolioValue.String())
	}

	// Account balance + holdings re-derive identically (AC#2 names both directly).
	dstBal, err := transaction.New(poolB).Balance(ctx, brokerID)
	if err != nil {
		t.Fatalf("dest balance: %v", err)
	}
	if srcBal.String() != dstBal.String() {
		t.Errorf("broker balance: src %s != dst %s", srcBal.String(), dstBal.String())
	}
	dstHoldings, dstRealized, _, err := transaction.New(poolB).Holdings(ctx, brokerID)
	if err != nil {
		t.Fatalf("dest holdings: %v", err)
	}
	if srcRealized.String() != dstRealized.String() {
		t.Errorf("broker realized gain: src %s != dst %s", srcRealized.String(), dstRealized.String())
	}
	if len(srcHoldings) != len(dstHoldings) {
		t.Fatalf("holdings count: src %d != dst %d", len(srcHoldings), len(dstHoldings))
	}
	for i := range srcHoldings {
		if srcHoldings[i].Symbol != dstHoldings[i].Symbol ||
			srcHoldings[i].Quantity.String() != dstHoldings[i].Quantity.String() ||
			srcHoldings[i].MarketValue.String() != dstHoldings[i].MarketValue.String() {
			t.Errorf("holding[%d] differs: src %+v dst %+v", i, srcHoldings[i], dstHoldings[i])
		}
	}

	// Identity sequence advanced past restored ids: a new account gets MAX+1.
	maxID := int64(0)
	for _, a := range dstExp.Accounts {
		if a.ID > maxID {
			maxID = a.ID
		}
	}
	created, err := account.New(poolB).Create(ctx, "PostRestore", account.Cash, money.USD)
	if err != nil {
		t.Fatalf("post-restore create: %v", err)
	}
	if created.ID != maxID+1 {
		t.Errorf("post-restore account id = %d, want %d (MAX+1)", created.ID, maxID+1)
	}
}

// TestRestoreIdempotent (AC#4): restoring the same file twice yields the same
// final state (replace-all).
func TestRestoreIdempotent(t *testing.T) {
	ctx := context.Background()
	poolA := newPool(t)
	seedSample(t, poolA)
	srcExp, _ := New(poolA).Export(ctx)
	raw, _ := json.Marshal(srcExp)

	poolB := newPool(t)
	if _, err := New(poolB).Restore(ctx, raw); err != nil {
		t.Fatalf("restore 1: %v", err)
	}
	first, _ := valuation.New(poolB).Portfolio(ctx)
	if _, err := New(poolB).Restore(ctx, raw); err != nil {
		t.Fatalf("restore 2: %v", err)
	}
	second, _ := valuation.New(poolB).Portfolio(ctx)
	if first.NetWorth.String() != second.NetWorth.String() {
		t.Errorf("idempotency broken: %s != %s", first.NetWorth.String(), second.NetWorth.String())
	}
	// Row counts unchanged after the second restore.
	exp2, _ := New(poolB).Export(ctx)
	if len(exp2.Accounts) != len(srcExp.Accounts) || len(exp2.Transactions) != len(srcExp.Transactions) {
		t.Errorf("second restore changed row counts")
	}
}

// TestRestoreAtomicReject (AC#3): a malformed / wrong-schema / wrong-version /
// dangling file is rejected with a clear typed error and leaves the instance
// unchanged.
func TestRestoreAtomicReject(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	seedSample(t, pool)
	svc := New(pool)

	before, _ := svc.Export(ctx)
	beforeVal, _ := valuation.New(pool).Portfolio(ctx)
	assertUnchanged := func(t *testing.T, label string) {
		t.Helper()
		after, err := svc.Export(ctx)
		if err != nil {
			t.Fatalf("%s: re-export: %v", label, err)
		}
		if len(after.Accounts) != len(before.Accounts) || len(after.Transactions) != len(before.Transactions) {
			t.Errorf("%s: row counts changed (instance not left unchanged)", label)
		}
		afterVal, _ := valuation.New(pool).Portfolio(ctx)
		if afterVal.NetWorth.String() != beforeVal.NetWorth.String() {
			t.Errorf("%s: NetWorth changed %s -> %s", label, beforeVal.NetWorth.String(), afterVal.NetWorth.String())
		}
	}

	// (a) not JSON.
	if _, err := svc.Restore(ctx, []byte("this is not json")); !errors.Is(err, ErrMalformed) {
		t.Errorf("non-JSON: err = %v, want ErrMalformed", err)
	}
	assertUnchanged(t, "non-JSON")

	// (b) wrong schema.
	bad := before
	bad.Schema = "bogus.format"
	badRaw, _ := json.Marshal(bad)
	if _, err := svc.Restore(ctx, badRaw); !errors.Is(err, ErrUnsupportedSchema) {
		t.Errorf("wrong schema: err = %v, want ErrUnsupportedSchema", err)
	}
	assertUnchanged(t, "wrong schema")

	// (c) unsupported version.
	ver := before
	ver.Version = 999
	verRaw, _ := json.Marshal(ver)
	if _, err := svc.Restore(ctx, verRaw); !errors.Is(err, ErrUnsupportedVersion) {
		t.Errorf("bad version: err = %v, want ErrUnsupportedVersion", err)
	}
	assertUnchanged(t, "bad version")

	// (d) dangling FK: a transaction referencing a non-existent account. The DB
	// foreign key aborts the tx → rollback → instance unchanged.
	dangling := Export{
		Schema:          ExportSchema,
		Version:         ExportVersion,
		DisplayCurrency: "USD",
		Accounts:        []AccountDTO{{ID: 1, Name: "Only", Type: "cash", Currency: "USD", CreatedAt: "2026-06-01T00:00:00Z"}},
		Categories:      []CategoryDTO{},
		Securities:      []SecurityDTO{},
		ExchangeRates:   []ExchangeRateDTO{},
		Prices:          []PriceDTO{},
		Transactions: []TransactionDTO{{
			ID: 1, Type: "income", ToAccountID: ptrInt64(999), // 999 does not exist
			FromAmount: "0", ToAmount: "100", OccurredOn: "2026-06-03", Description: "bad",
			Quantity: "0", Price: "0", Fees: "0",
		}},
	}
	danglingRaw, _ := json.Marshal(dangling)
	if _, err := svc.Restore(ctx, danglingRaw); !errors.Is(err, ErrMalformed) {
		t.Errorf("dangling FK: err = %v, want ErrMalformed", err)
	}
	assertUnchanged(t, "dangling FK")
}

func ptrInt64(v int64) *int64 { return &v }

// TestRestorePreservesFitid (Story 7.1): the OFX dedup key is authored state on
// the transaction row, so export→restore must carry it — otherwise a restore
// silently drops idempotency and a re-imported OFX would duplicate. Also asserts
// a pre-7.1 export (no fitid field) restores as NULL (forward-tolerant).
func TestRestorePreservesFitid(t *testing.T) {
	ctx := context.Background()
	poolA := newPool(t)

	cash, err := account.New(poolA).Create(ctx, "Cash", account.Cash, money.USD)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	// An OFX-imported income row carrying a FITID (inserted via the store directly).
	if _, err := store.New(poolA).CreateOFXTransaction(ctx, store.CreateOFXTransactionParams{
		Type:        "income",
		ToAccountID: pgtype.Int8{Int64: cash.ID, Valid: true},
		FromAmount:  req("0"),
		ToAmount:    req("100"),
		OccurredOn:  dt(t, "2026-06-03"),
		Description: "ofx pay",
		Fitid:       pgtype.Text{String: "BANK-FIT-1", Valid: true},
	}); err != nil {
		t.Fatalf("create ofx tx: %v", err)
	}

	srcExp, err := New(poolA).Export(ctx)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(srcExp.Transactions) != 1 || srcExp.Transactions[0].Fitid == nil || *srcExp.Transactions[0].Fitid != "BANK-FIT-1" {
		t.Fatalf("export did not carry fitid: %+v", srcExp.Transactions)
	}

	// Round-trip into a fresh instance: fitid survives.
	raw, _ := json.Marshal(srcExp)
	poolB := newPool(t)
	if _, err := New(poolB).Restore(ctx, raw); err != nil {
		t.Fatalf("restore: %v", err)
	}
	dstExp, _ := New(poolB).Export(ctx)
	if len(dstExp.Transactions) != 1 || dstExp.Transactions[0].Fitid == nil || *dstExp.Transactions[0].Fitid != "BANK-FIT-1" {
		t.Errorf("restored export lost fitid: %+v", dstExp.Transactions)
	}

	// Backward compatibility: an export that omits fitid (pre-7.1) restores as NULL.
	old := srcExp
	old.Transactions = []TransactionDTO{srcExp.Transactions[0]}
	old.Transactions[0].Fitid = nil // omitempty ⇒ the field is absent in the JSON
	oldRaw, _ := json.Marshal(old)
	poolC := newPool(t)
	if _, err := New(poolC).Restore(ctx, oldRaw); err != nil {
		t.Fatalf("restore old export: %v", err)
	}
	cExp, _ := New(poolC).Export(ctx)
	if len(cExp.Transactions) != 1 || cExp.Transactions[0].Fitid != nil {
		t.Errorf("pre-7.1 export should restore fitid as NULL: %+v", cExp.Transactions)
	}
}
