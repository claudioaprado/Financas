package valuation

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
	t.Skip("set TEST_DATABASE_URL or DATABASE_URL to run valuation integration tests")
	return ""
}

// isolatedDB creates a throwaway database for this test and returns its URL plus
// a cleanup that drops it. Portfolio aggregates across EVERY account/security/
// rate AND reads the global Display-Currency singleton, so — unlike the
// account-scoped service tests — it cannot share the base DB: a concurrent
// settings test (package binaries run in parallel) would race on the display
// currency. A private database gives this test a pristine, uncontended schema.
func isolatedDB(t *testing.T, baseURL string) string {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("fin_val_test_%d", time.Now().UnixNano())

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

func d(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

// TestPortfolioValuation exercises the cross-account aggregation end to end: a
// BRL and a USD investment account (each with a priced holding), an unpriced BRL
// holding, a realized BRL gain, and a BRL credit liability. Display = BRL.
//
// Investment buys debit the account's cash with no offsetting deposit, so cash
// balances are negative here — a legitimate ledger state that exercises the
// signed convert-then-sum faithfully.
func TestPortfolioValuation(t *testing.T) {
	ctx := context.Background()
	url := isolatedDB(t, testDatabaseURL(t))
	if err := store.Migrate(ctx, url, db.Migrations); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := store.NewPool(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	accts := account.New(pool)
	secs := security.New(pool)
	txns := transaction.New(pool)
	prices := price.New(pool)
	rates := exchangerate.New(pool)
	set := settings.New(pool)
	svc := New(pool)
	run := time.Now().UnixNano()
	date := d(t, "2026-06-01")
	later := d(t, "2026-06-10")

	// Private DB → free to set the Display Currency without leaking into other tests.
	if err := set.SetDisplayCurrency(ctx, money.BRL); err != nil {
		t.Fatalf("set display currency: %v", err)
	}

	// --- BRL investment account: a priced holding, an unpriced holding, a sell. ---
	brokerBRL, err := accts.Create(ctx, fmt.Sprintf("BrokerBRL-%d", run), account.Investment, money.BRL)
	if err != nil {
		t.Fatalf("create brokerBRL: %v", err)
	}
	brlSec, err := secs.Create(ctx, fmt.Sprintf("BSEC%d", run), "BRL Stock", security.Stock, money.BRL)
	if err != nil {
		t.Fatalf("create brlSec: %v", err)
	}
	unpr, err := secs.Create(ctx, fmt.Sprintf("UNPR%d", run), "Unpriced", security.Stock, money.BRL)
	if err != nil {
		t.Fatalf("create unpr: %v", err)
	}
	// buy 10 @ 100 → basis 1000; sell 4 @ 120 → realized 80, remaining qty 6 basis 600.
	if _, err := txns.Buy(ctx, brokerBRL.ID, brlSec.ID, req("10"), req("100"), req("0"), date, "buy"); err != nil {
		t.Fatalf("buy brl: %v", err)
	}
	if _, err := txns.Sell(ctx, brokerBRL.ID, brlSec.ID, req("4"), req("120"), req("0"), later, "sell"); err != nil {
		t.Fatalf("sell brl: %v", err)
	}
	// buy 4 @ 50 of the unpriced security → basis 200, qty 4.
	if _, err := txns.Buy(ctx, brokerBRL.ID, unpr.ID, req("4"), req("50"), req("0"), date, "buy unpr"); err != nil {
		t.Fatalf("buy unpr: %v", err)
	}
	if _, err := prices.Add(ctx, brlSec.ID, date, req("110")); err != nil {
		t.Fatalf("price brl: %v", err)
	}

	// --- USD investment account: a priced holding. ---
	brokerUSD, err := accts.Create(ctx, fmt.Sprintf("BrokerUSD-%d", run), account.Investment, money.USD)
	if err != nil {
		t.Fatalf("create brokerUSD: %v", err)
	}
	usdSec, err := secs.Create(ctx, fmt.Sprintf("USEC%d", run), "USD Stock", security.Stock, money.USD)
	if err != nil {
		t.Fatalf("create usdSec: %v", err)
	}
	if _, err := txns.Buy(ctx, brokerUSD.ID, usdSec.ID, req("5"), req("20"), req("0"), date, "buy usd"); err != nil {
		t.Fatalf("buy usd: %v", err)
	}
	if _, err := prices.Add(ctx, usdSec.ID, date, req("30")); err != nil {
		t.Fatalf("price usd: %v", err)
	}

	// --- BRL credit account with an owed balance. ---
	card, err := accts.Create(ctx, fmt.Sprintf("Card-%d", run), account.Credit, money.BRL)
	if err != nil {
		t.Fatalf("create card: %v", err)
	}
	if _, err := txns.Record(ctx, card.ID, transaction.Expense, req("200"), date, "spend", 0); err != nil {
		t.Fatalf("card expense: %v", err)
	}

	// ---- 1) Missing rate: USD excluded from BOTH totals, BRL part still totals. ----
	p, err := svc.Portfolio(ctx)
	if err != nil {
		t.Fatalf("portfolio (no rate): %v", err)
	}
	if got := p.PortfolioValue.String(); got != "660.0000 BRL" {
		t.Errorf("PortfolioValue (no rate) = %s, want 660.0000 BRL", got)
	}
	if got := p.NetWorth.String(); got != "-260.0000 BRL" {
		t.Errorf("NetWorth (no rate) = %s, want -260.0000 BRL", got)
	}
	if len(p.Missing) != 1 || p.Missing[0] != money.USD {
		t.Errorf("Missing (no rate) = %v, want [USD]", p.Missing)
	}
	if len(p.Unpriced) != 1 || p.Unpriced[0] != unpr.Symbol {
		t.Errorf("Unpriced = %v, want [%s]", p.Unpriced, unpr.Symbol)
	}
	assertRealized(t, p, money.BRL, "80")

	// ---- 2) With a USD->BRL rate: convert-then-sum, Missing empty. ----
	if _, err := rates.Add(ctx, money.USD, money.BRL, date, req("5")); err != nil {
		t.Fatalf("add rate: %v", err)
	}
	p, err = svc.Portfolio(ctx)
	if err != nil {
		t.Fatalf("portfolio (rate): %v", err)
	}
	// 660 BRL + 150 USD × 5 = 1410.
	if got := p.PortfolioValue.String(); got != "1410.0000 BRL" {
		t.Errorf("PortfolioValue (rate) = %s, want 1410.0000 BRL", got)
	}
	// cash (-720 + -100×5) + holdings 1410 − owed 200 = -10.
	if got := p.NetWorth.String(); got != "-10.0000 BRL" {
		t.Errorf("NetWorth (rate) = %s, want -10.0000 BRL", got)
	}
	if len(p.Missing) != 0 {
		t.Errorf("Missing (rate) = %v, want empty", p.Missing)
	}
	// A USD holding row carries native USD figures (no FX at the per-holding level).
	usdRow := findHolding(t, p, usdSec.Symbol)
	if got := usdRow.Valuation.String(); got != "150.0000 USD" {
		t.Errorf("USD holding valuation = %s, want 150.0000 USD", got)
	}

	// ---- 3) Archiving the USD account drops it from the totals. ----
	if err := accts.SetArchived(ctx, brokerUSD.ID, true); err != nil {
		t.Fatalf("archive usd: %v", err)
	}
	p, err = svc.Portfolio(ctx)
	if err != nil {
		t.Fatalf("portfolio (archived): %v", err)
	}
	if got := p.PortfolioValue.String(); got != "660.0000 BRL" {
		t.Errorf("PortfolioValue (archived) = %s, want 660.0000 BRL", got)
	}
	if got := p.NetWorth.String(); got != "-260.0000 BRL" {
		t.Errorf("NetWorth (archived) = %s, want -260.0000 BRL", got)
	}
	if len(p.Missing) != 0 {
		t.Errorf("Missing (archived) = %v, want empty (no USD present)", p.Missing)
	}
	for _, h := range p.Holdings {
		if h.Symbol == usdSec.Symbol {
			t.Errorf("archived USD holding %s should be excluded, got %+v", h.Symbol, h)
		}
	}
}

// assertRealized asserts the cumulative realized G/L for a currency.
func assertRealized(t *testing.T, p Portfolio, cur money.Currency, want string) {
	t.Helper()
	for _, m := range p.RealizedByCurrency {
		if m.Currency() == cur {
			if !m.Amount().Equal(req(want)) {
				t.Errorf("realized %s = %s, want %s", cur, m.Amount(), want)
			}
			return
		}
	}
	t.Errorf("realized %s not found in %v", cur, p.RealizedByCurrency)
}

// findHolding returns the holding row for a symbol, failing if absent.
func findHolding(t *testing.T, p Portfolio, symbol string) HoldingValuation {
	t.Helper()
	for _, h := range p.Holdings {
		if h.Symbol == symbol {
			return h
		}
	}
	t.Fatalf("holding %s not found in %v", symbol, p.Holdings)
	return HoldingValuation{}
}
