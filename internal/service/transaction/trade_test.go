package transaction

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/db"
	"github.com/claudioaprado/financas/internal/money"
	"github.com/claudioaprado/financas/internal/service/account"
	"github.com/claudioaprado/financas/internal/service/security"
	"github.com/claudioaprado/financas/internal/store"
)

func req(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestInvestmentTrades(t *testing.T) {
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
	secs := security.New(pool)
	svc := New(pool)
	run := time.Now().UnixNano()
	date := d(t, "2026-06-01")

	// An investment account in BRL + a BRL security (same-currency-only).
	inv, err := accts.Create(ctx, fmt.Sprintf("Broker-%d", run), account.Investment, money.BRL)
	if err != nil {
		t.Fatalf("create investment account: %v", err)
	}
	sec, err := secs.Create(ctx, fmt.Sprintf("PETR%d", run), "Petrobras", security.Stock, money.BRL)
	if err != nil {
		t.Fatalf("create security: %v", err)
	}

	// Buy 100 @ 10.00 fee 5.00 → cost 1005.00.
	if _, err := svc.Buy(ctx, inv.ID, sec.ID, req("100"), req("10.00"), req("5.00"), date, "buy1"); err != nil {
		t.Fatalf("buy1: %v", err)
	}
	// Buy 100 @ 12.00 fee 0 → basis 2205, qty 200.
	if _, err := svc.Buy(ctx, inv.ID, sec.ID, req("100"), req("12.00"), req("0"), date, "buy2"); err != nil {
		t.Fatalf("buy2: %v", err)
	}

	assertHolding := func(label string, wantQty, wantBasis, wantRealized string) {
		t.Helper()
		views, realized, _, err := svc.Holdings(ctx, inv.ID)
		if err != nil {
			t.Fatalf("%s holdings: %v", label, err)
		}
		if !realized.Amount().Equal(req(wantRealized)) {
			t.Errorf("%s cumulative realized = %s, want %s", label, realized.Amount(), wantRealized)
		}
		if wantQty == "0" {
			for _, v := range views {
				if v.SecurityID == sec.ID {
					t.Errorf("%s: closed position should be hidden from active holdings, got %+v", label, v)
				}
			}
			return
		}
		for _, v := range views {
			if v.SecurityID == sec.ID {
				if !v.Quantity.Equal(req(wantQty)) {
					t.Errorf("%s qty = %s, want %s", label, v.Quantity, wantQty)
				}
				if !v.CostBasis.Amount().Equal(req(wantBasis)) {
					t.Errorf("%s basis = %s, want %s", label, v.CostBasis.Amount(), wantBasis)
				}
				return
			}
		}
		t.Errorf("%s: holding not found", label)
	}

	assertHolding("after buys", "200", "2205", "0")

	// Cash balance after buys = −2205 (two debits).
	bal, err := svc.Balance(ctx, inv.ID)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if !bal.Amount().Equal(req("-2205")) {
		t.Errorf("cash after buys = %s, want -2205", bal.Amount())
	}

	// Sell 50 @ 15.00 fee 3.00 → proceeds 747, basis_sold 551.25, realized 195.75.
	if _, err := svc.Sell(ctx, inv.ID, sec.ID, req("50"), req("15.00"), req("3.00"), date, "sell1"); err != nil {
		t.Fatalf("sell1: %v", err)
	}
	assertHolding("after partial sell", "150", "1653.75", "195.75")
	// Cash now −2205 + 747 = −1458.
	bal, _ = svc.Balance(ctx, inv.ID)
	if !bal.Amount().Equal(req("-1458")) {
		t.Errorf("cash after sell = %s, want -1458", bal.Amount())
	}

	// Dividend 40.00 → cash +40, holding unchanged.
	if _, err := svc.Dividend(ctx, inv.ID, sec.ID, req("40.00"), date, "div"); err != nil {
		t.Fatalf("dividend: %v", err)
	}
	assertHolding("after dividend", "150", "1653.75", "195.75")
	bal, _ = svc.Balance(ctx, inv.ID)
	if !bal.Amount().Equal(req("-1418")) {
		t.Errorf("cash after dividend = %s, want -1418", bal.Amount())
	}

	// Sell all 150 @ 16 fee 0 → exact wipe; realized += 2400 − 1653.75 = 746.25 → 942.00.
	if _, err := svc.Sell(ctx, inv.ID, sec.ID, req("150"), req("16.00"), req("0"), date, "sell-all"); err != nil {
		t.Fatalf("sell-all: %v", err)
	}
	assertHolding("after full sell", "0", "0", "942")

	// Oversell now (holding is 0) → rejected.
	if _, err := svc.Sell(ctx, inv.ID, sec.ID, req("1"), req("16"), req("0"), date, "oversell"); !errors.Is(err, ErrOversold) {
		t.Errorf("oversell = %v, want ErrOversold", err)
	}
}

func TestTradeValidation(t *testing.T) {
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
	secs := security.New(pool)
	svc := New(pool)
	run := time.Now().UnixNano()
	date := d(t, "2026-06-01")

	invBRL, _ := accts.Create(ctx, fmt.Sprintf("BrokerBRL-%d", run), account.Investment, money.BRL)
	cash, _ := accts.Create(ctx, fmt.Sprintf("Wallet-%d", run), account.Cash, money.BRL)
	secUSD, _ := secs.Create(ctx, fmt.Sprintf("VOO%d", run), "Vanguard", security.ETF, money.USD)

	// Cross-currency trade (USD security on a BRL account) → rejected.
	if _, err := svc.Buy(ctx, invBRL.ID, secUSD.ID, req("1"), req("1"), req("0"), date, "x"); !errors.Is(err, ErrTradeCurrencyMismatch) {
		t.Errorf("cross-currency buy = %v, want ErrTradeCurrencyMismatch", err)
	}
	// Trade on a cash account → rejected.
	secBRL, _ := secs.Create(ctx, fmt.Sprintf("ITUB%d", run), "Itau", security.Stock, money.BRL)
	if _, err := svc.Buy(ctx, cash.ID, secBRL.ID, req("1"), req("1"), req("0"), date, "y"); !errors.Is(err, ErrNotInvestmentAccount) {
		t.Errorf("trade on cash account = %v, want ErrNotInvestmentAccount", err)
	}
	// Income/expense still rejected on an investment account (unchanged behavior).
	if _, err := svc.Record(ctx, invBRL.ID, Income, req("10"), date, "z", 0); !errors.Is(err, ErrUnsupportedAccountType) {
		t.Errorf("income on investment account = %v, want ErrUnsupportedAccountType", err)
	}
	// Non-positive quantity / negative fees.
	if _, err := svc.Buy(ctx, invBRL.ID, secBRL.ID, req("0"), req("1"), req("0"), date, "q"); !errors.Is(err, ErrNonPositiveQuantity) {
		t.Errorf("zero quantity = %v, want ErrNonPositiveQuantity", err)
	}
	// Selling with nothing held → oversell.
	if _, err := svc.Sell(ctx, invBRL.ID, secBRL.ID, req("1"), req("1"), req("0"), date, "s"); !errors.Is(err, ErrOversold) {
		t.Errorf("sell with no holding = %v, want ErrOversold", err)
	}
}

// TestBackdatedSellRejected locks in the review fix: a sell dated BEFORE its buy
// (or recorded before its buy on the same date) must be rejected at entry — the
// guard re-derives the resulting ledger on the insert tx, so it agrees with the
// chronological read derivation rather than the date-agnostic net quantity.
func TestBackdatedSellRejected(t *testing.T) {
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
	secs := security.New(pool)
	svc := New(pool)
	run := time.Now().UnixNano()

	inv, _ := accts.Create(ctx, fmt.Sprintf("BD-%d", run), account.Investment, money.BRL)
	sec, _ := secs.Create(ctx, fmt.Sprintf("BDSEC%d", run), "S", security.Stock, money.BRL)

	// Buy 10 on 2026-06-10.
	if _, err := svc.Buy(ctx, inv.ID, sec.ID, req("10"), req("5"), req("0"), d(t, "2026-06-10"), "buy"); err != nil {
		t.Fatalf("buy: %v", err)
	}
	// A sell of 10 dated 2026-06-01 (before the buy) nets to 0 by date-agnostic
	// math, but is oversold chronologically (nothing held on 06-01) → reject, and
	// the row must NOT persist (so Holdings still derives cleanly afterward).
	if _, err := svc.Sell(ctx, inv.ID, sec.ID, req("10"), req("6"), req("0"), d(t, "2026-06-01"), "backdated"); !errors.Is(err, ErrOversold) {
		t.Errorf("backdated sell = %v, want ErrOversold", err)
	}
	views, _, _, err := svc.Holdings(ctx, inv.ID)
	if err != nil {
		t.Fatalf("holdings must still derive after a rejected backdated sell: %v", err)
	}
	found := false
	for _, v := range views {
		if v.SecurityID == sec.ID {
			found = true
			if !v.Quantity.Equal(req("10")) {
				t.Errorf("qty = %s, want 10 (backdated sell must not have persisted)", v.Quantity)
			}
		}
	}
	if !found {
		t.Errorf("holding missing — the rejected sell may have persisted and bricked derivation")
	}
}

// TestHoldingValuation proves market value / unrealized G/L are derived on read
// (Story 4.3): a holding has no market value until a price row exists, then
// re-values purely because the price was appended — nothing is stored/recomputed.
func TestHoldingValuation(t *testing.T) {
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
	secs := security.New(pool)
	svc := New(pool)
	run := time.Now().UnixNano()
	date := d(t, "2026-06-01")

	inv, err := accts.Create(ctx, fmt.Sprintf("Broker-%d", run), account.Investment, money.BRL)
	if err != nil {
		t.Fatalf("create investment account: %v", err)
	}
	sec, err := secs.Create(ctx, fmt.Sprintf("VAL%d", run), "Valuation Co", security.Stock, money.BRL)
	if err != nil {
		t.Fatalf("create security: %v", err)
	}
	// Buy 100 @ 10.00 fee 5.00 → qty 100, basis 1005.00.
	if _, err := svc.Buy(ctx, inv.ID, sec.ID, req("100"), req("10.00"), req("5.00"), date, "buy"); err != nil {
		t.Fatalf("buy: %v", err)
	}

	findHolding := func(label string) HoldingView {
		t.Helper()
		views, _, _, err := svc.Holdings(ctx, inv.ID)
		if err != nil {
			t.Fatalf("%s holdings: %v", label, err)
		}
		for _, v := range views {
			if v.SecurityID == sec.ID {
				return v
			}
		}
		t.Fatalf("%s: holding for security %d not found", label, sec.ID)
		return HoldingView{}
	}

	// No price yet → HasPrice false (the page renders "—").
	if v := findHolding("pre-price"); v.HasPrice {
		t.Errorf("pre-price: HasPrice = true, want false (no price exists)")
	}

	// Append a price effective on or before today.
	if _, err := store.New(pool).AddPrice(ctx, store.AddPriceParams{
		SecurityID:    sec.ID,
		EffectiveDate: date,
		Price:         req("16.00"),
	}); err != nil {
		t.Fatalf("add price: %v", err)
	}

	// Now the same holding re-values on read: market 100×16 = 1600, unrealized
	// 1600 − 1005 = 595, price date = the effective date entered.
	v := findHolding("post-price")
	if !v.HasPrice {
		t.Fatalf("post-price: HasPrice = false, want true")
	}
	if got := v.MarketValue.String(); got != "1600.0000 BRL" {
		t.Errorf("market value = %s, want 1600.0000 BRL", got)
	}
	if got := v.UnrealizedGain.String(); got != "595.0000 BRL" {
		t.Errorf("unrealized gain = %s, want 595.0000 BRL", got)
	}
	if got := v.Price.String(); got != "16.0000 BRL" {
		t.Errorf("price = %s, want 16.0000 BRL", got)
	}
	if !v.PriceDate.Equal(date) {
		t.Errorf("price date = %v, want %v", v.PriceDate, date)
	}
}

// TestOversoldIsolatedPerSecurity proves the per-security isolation end to end
// (the point of the change): when one security is made inconsistent (a buy
// deleted out from under a later sell), the OTHER security's holding still
// derives, is still sellable, and only the broken symbol is reported oversold —
// the whole account is not hidden or blocked.
func TestOversoldIsolatedPerSecurity(t *testing.T) {
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
	secs := security.New(pool)
	svc := New(pool)
	run := time.Now().UnixNano()
	date := d(t, "2026-06-01")

	inv, err := accts.Create(ctx, fmt.Sprintf("Broker-%d", run), account.Investment, money.BRL)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	good, err := secs.Create(ctx, fmt.Sprintf("GOOD%d", run), "Good Co", security.Stock, money.BRL)
	if err != nil {
		t.Fatalf("good security: %v", err)
	}
	bad, err := secs.Create(ctx, fmt.Sprintf("BAD%d", run), "Bad Co", security.Stock, money.BRL)
	if err != nil {
		t.Fatalf("bad security: %v", err)
	}

	if _, err := svc.Buy(ctx, inv.ID, good.ID, req("4"), req("20"), req("0"), date, "buy good"); err != nil {
		t.Fatalf("buy good: %v", err)
	}
	badBuy, err := svc.Buy(ctx, inv.ID, bad.ID, req("10"), req("5"), req("0"), date, "buy bad")
	if err != nil {
		t.Fatalf("buy bad: %v", err)
	}
	if _, err := svc.Sell(ctx, inv.ID, bad.ID, req("4"), req("6"), req("0"), d(t, "2026-06-02"), "sell bad"); err != nil {
		t.Fatalf("sell bad: %v", err)
	}

	// Break `bad`: delete its buy, leaving the later sell overselling it.
	if err := svc.Delete(ctx, badBuy.ID); err != nil {
		t.Fatalf("delete bad buy: %v", err)
	}

	// Holdings: the good position still derives; only `bad` is reported oversold.
	views, _, oversold, err := svc.Holdings(ctx, inv.ID)
	if err != nil {
		t.Fatalf("holdings after breaking bad: %v", err)
	}
	if len(oversold) != 1 || oversold[0] != bad.Symbol {
		t.Fatalf("oversold = %v, want [%s]", oversold, bad.Symbol)
	}
	foundGood := false
	for _, v := range views {
		if v.SecurityID == bad.ID {
			t.Errorf("oversold security %s must be excluded from holdings", bad.Symbol)
		}
		if v.SecurityID == good.ID {
			foundGood = true
			if !v.Quantity.Equal(req("4")) {
				t.Errorf("good qty = %s, want 4", v.Quantity)
			}
		}
	}
	if !foundGood {
		t.Error("good holding should still derive despite the broken position")
	}

	// The good security is still sellable — a pre-existing inconsistency in `bad`
	// must not block it.
	if _, err := svc.Sell(ctx, inv.ID, good.ID, req("1"), req("30"), req("0"), d(t, "2026-06-03"), "sell good"); err != nil {
		t.Fatalf("selling the good security should succeed, got: %v", err)
	}
}
