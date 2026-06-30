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

// TestDashboardAsOfAndDeltas exercises the period-change machinery end to end: a
// holding priced 100 ten days ago and 110 today, plus a positive cash balance.
// portfolioAsOf(prior) must value the holding at the OLD price (as-of history),
// and Dashboard must compute each card's per-card % delta against that baseline —
// with the gain/loss card showing "—" because its baseline is zero.
func TestDashboardAsOfAndDeltas(t *testing.T) {
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
	set := settings.New(pool)
	svc := New(pool)
	run := time.Now().UnixNano()

	today := dateOnlyUTC(time.Now())
	old := today.AddDate(0, 0, -10)

	if err := set.SetDisplayCurrency(ctx, money.BRL); err != nil {
		t.Fatalf("set display currency: %v", err)
	}

	// A cash account with a positive balance so Net Worth/Cash baselines are > 0.
	wallet, err := accts.Create(ctx, fmt.Sprintf("Wallet-%d", run), account.Cash, money.BRL)
	if err != nil {
		t.Fatalf("create wallet: %v", err)
	}
	if _, err := txns.Record(ctx, wallet.ID, transaction.Income, req("5000"), old, "salary", 0); err != nil {
		t.Fatalf("income: %v", err)
	}

	// An investment account: buy 10 @ 100 ten days ago (basis 1000, invest cash −1000).
	broker, err := accts.Create(ctx, fmt.Sprintf("Broker-%d", run), account.Investment, money.BRL)
	if err != nil {
		t.Fatalf("create broker: %v", err)
	}
	sec, err := secs.Create(ctx, fmt.Sprintf("SEC%d", run), "Stock", security.Stock, money.BRL)
	if err != nil {
		t.Fatalf("create sec: %v", err)
	}
	if _, err := txns.Buy(ctx, broker.ID, sec.ID, req("10"), req("100"), req("0"), old, "buy"); err != nil {
		t.Fatalf("buy: %v", err)
	}
	// Price 100 effective ten days ago, 110 effective today.
	if _, err := prices.Add(ctx, sec.ID, old, req("100")); err != nil {
		t.Fatalf("price old: %v", err)
	}
	if _, err := prices.Add(ctx, sec.ID, today, req("110")); err != nil {
		t.Fatalf("price new: %v", err)
	}

	// Baseline (as of the prior sample = old): holding at the OLD price (100).
	base, err := svc.portfolioAsOf(ctx, old)
	if err != nil {
		t.Fatalf("portfolioAsOf(old): %v", err)
	}
	if got := base.PortfolioValue.String(); got != "1000.0000 BRL" {
		t.Errorf("baseline PortfolioValue = %s, want 1000.0000 BRL (old price)", got)
	}
	if got := base.TotalGain.String(); got != "0.0000 BRL" {
		t.Errorf("baseline TotalGain = %s, want 0.0000 BRL", got)
	}

	d, err := svc.Dashboard(ctx)
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}

	// Current figures: cash 5000−1000=4000; holdings 10×110=1100; NW 5100; gain 100.
	if got := d.NetWorth.Value.String(); got != "5100.0000 BRL" {
		t.Errorf("NetWorth = %s, want 5100.0000 BRL", got)
	}
	if got := d.Portfolio.Value.String(); got != "1100.0000 BRL" {
		t.Errorf("Portfolio = %s, want 1100.0000 BRL", got)
	}
	if got := d.Cash.Value.String(); got != "4000.0000 BRL" {
		t.Errorf("Cash = %s, want 4000.0000 BRL", got)
	}
	if got := d.GainLoss.Value.String(); got != "100.0000 BRL" {
		t.Errorf("GainLoss = %s, want 100.0000 BRL", got)
	}
	if !d.GainLoss.Positive {
		t.Error("GainLoss.Positive = false, want true (a gain)")
	}

	// Per-card deltas vs the old baseline.
	if !d.PriorDate.Equal(old) {
		t.Errorf("PriorDate = %s, want %s", d.PriorDate.Format("2006-01-02"), old.Format("2006-01-02"))
	}
	if !d.NetWorth.HasDelta || !d.NetWorth.DeltaUp || d.NetWorth.DeltaPct.StringFixed(1) != "2.0" {
		t.Errorf("NetWorth delta = %+v, want up 2.0%%", d.NetWorth)
	}
	if !d.Portfolio.HasDelta || !d.Portfolio.DeltaUp || d.Portfolio.DeltaPct.StringFixed(1) != "10.0" {
		t.Errorf("Portfolio delta = %+v, want up 10.0%%", d.Portfolio)
	}
	if !d.Cash.HasDelta || d.Cash.DeltaUp || d.Cash.DeltaDown || d.Cash.DeltaPct.StringFixed(1) != "0.0" {
		t.Errorf("Cash delta = %+v, want flat 0.0%%", d.Cash)
	}
	// Gain/loss baseline is zero → % undefined → "—".
	if d.GainLoss.HasDelta {
		t.Errorf("GainLoss.HasDelta = true, want false (zero baseline → \"—\")")
	}
}

// TestDashboardPriorSampleWhenLatestIsPast guards the period-change semantics
// when the most recent sample is in the PAST (no price entered today): the
// baseline must be the sample BEFORE the latest one (today-20), not the latest
// itself (today-10) — otherwise the delta degenerates to a misleading 0%. The
// current value reflects the today-10 price; the baseline must reflect today-20.
func TestDashboardPriorSampleWhenLatestIsPast(t *testing.T) {
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
	set := settings.New(pool)
	svc := New(pool)
	run := time.Now().UnixNano()
	today := dateOnlyUTC(time.Now())
	older := today.AddDate(0, 0, -20)
	recent := today.AddDate(0, 0, -10)

	if err := set.SetDisplayCurrency(ctx, money.BRL); err != nil {
		t.Fatalf("set display currency: %v", err)
	}
	broker, err := accts.Create(ctx, fmt.Sprintf("Broker-%d", run), account.Investment, money.BRL)
	if err != nil {
		t.Fatalf("create broker: %v", err)
	}
	sec, err := secs.Create(ctx, fmt.Sprintf("SEC%d", run), "Stock", security.Stock, money.BRL)
	if err != nil {
		t.Fatalf("create sec: %v", err)
	}
	if _, err := txns.Buy(ctx, broker.ID, sec.ID, req("10"), req("100"), req("0"), older, "buy"); err != nil {
		t.Fatalf("buy: %v", err)
	}
	// Two PAST price samples; none today.
	if _, err := prices.Add(ctx, sec.ID, older, req("100")); err != nil {
		t.Fatalf("price older: %v", err)
	}
	if _, err := prices.Add(ctx, sec.ID, recent, req("110")); err != nil {
		t.Fatalf("price recent: %v", err)
	}

	d, err := svc.Dashboard(ctx)
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	// Baseline = older (today-20) @ price 100 → portfolio 1000; current reflects
	// the recent (today-10) price 110 → 1100. Delta must be a real +10%, not 0%.
	if !d.PriorDate.Equal(older) {
		t.Errorf("PriorDate = %s, want %s (the sample before the latest)", d.PriorDate.Format("2006-01-02"), older.Format("2006-01-02"))
	}
	if got := d.Portfolio.Value.String(); got != "1100.0000 BRL" {
		t.Errorf("Portfolio = %s, want 1100.0000 BRL (recent price)", got)
	}
	if !d.Portfolio.HasDelta || !d.Portfolio.DeltaUp || d.Portfolio.DeltaPct.StringFixed(1) != "10.0" {
		t.Errorf("Portfolio delta = %+v, want up 10.0%% (not a degenerate 0%%)", d.Portfolio)
	}
}

// TestDashboardNoPriorSample confirms the day-one state: with only a price
// effective today (no input changed before today), every card's delta is "—".
func TestDashboardNoPriorSample(t *testing.T) {
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
	set := settings.New(pool)
	svc := New(pool)
	run := time.Now().UnixNano()
	today := dateOnlyUTC(time.Now())

	if err := set.SetDisplayCurrency(ctx, money.BRL); err != nil {
		t.Fatalf("set display currency: %v", err)
	}
	broker, err := accts.Create(ctx, fmt.Sprintf("Broker-%d", run), account.Investment, money.BRL)
	if err != nil {
		t.Fatalf("create broker: %v", err)
	}
	sec, err := secs.Create(ctx, fmt.Sprintf("SEC%d", run), "Stock", security.Stock, money.BRL)
	if err != nil {
		t.Fatalf("create sec: %v", err)
	}
	if _, err := txns.Buy(ctx, broker.ID, sec.ID, req("10"), req("100"), req("0"), today, "buy"); err != nil {
		t.Fatalf("buy: %v", err)
	}
	if _, err := prices.Add(ctx, sec.ID, today, req("110")); err != nil {
		t.Fatalf("price: %v", err)
	}

	d, err := svc.Dashboard(ctx)
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	if !d.PriorDate.IsZero() {
		t.Errorf("PriorDate = %s, want zero (no prior sample)", d.PriorDate)
	}
	for name, k := range map[string]KPI{"NetWorth": d.NetWorth, "Portfolio": d.Portfolio, "Cash": d.Cash, "GainLoss": d.GainLoss} {
		if k.HasDelta {
			t.Errorf("%s.HasDelta = true, want false (no prior sample → \"—\")", name)
		}
	}
}

// TestValueSeries exercises the value-over-time series: a holding priced 100
// twenty days ago and 110 ten days ago, plus a cash deposit. The series must
// have a point per sample date valued AS OF that date (the older point uses 100,
// NOT today's 110 — proving no retroactive repricing, AD-11), end at today, and
// a windowed call must start at the window boundary with the correct as-of value.
func TestValueSeries(t *testing.T) {
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
	set := settings.New(pool)
	svc := New(pool)
	run := time.Now().UnixNano()
	today := dateOnlyUTC(time.Now())
	older := today.AddDate(0, 0, -20)
	recent := today.AddDate(0, 0, -10)

	if err := set.SetDisplayCurrency(ctx, money.BRL); err != nil {
		t.Fatalf("set display currency: %v", err)
	}
	wallet, err := accts.Create(ctx, fmt.Sprintf("Wallet-%d", run), account.Cash, money.BRL)
	if err != nil {
		t.Fatalf("create wallet: %v", err)
	}
	if _, err := txns.Record(ctx, wallet.ID, transaction.Income, req("5000"), older, "salary", 0); err != nil {
		t.Fatalf("income: %v", err)
	}
	broker, err := accts.Create(ctx, fmt.Sprintf("Broker-%d", run), account.Investment, money.BRL)
	if err != nil {
		t.Fatalf("create broker: %v", err)
	}
	sec, err := secs.Create(ctx, fmt.Sprintf("SEC%d", run), "Stock", security.Stock, money.BRL)
	if err != nil {
		t.Fatalf("create sec: %v", err)
	}
	if _, err := txns.Buy(ctx, broker.ID, sec.ID, req("10"), req("100"), req("0"), older, "buy"); err != nil {
		t.Fatalf("buy: %v", err)
	}
	if _, err := prices.Add(ctx, sec.ID, older, req("100")); err != nil {
		t.Fatalf("price older: %v", err)
	}
	if _, err := prices.Add(ctx, sec.ID, recent, req("110")); err != nil {
		t.Fatalf("price recent: %v", err)
	}

	// All history: points at older, recent, today.
	all, err := svc.ValueSeries(ctx, time.Time{})
	if err != nil {
		t.Fatalf("ValueSeries(all): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("ValueSeries(all) len = %d, want 3 (older, recent, today)", len(all))
	}
	// cash 5000−1000=4000; older holding 10×100=1000 → NW 5000 (OLD price, AD-11).
	if !all[0].Date.Equal(older) || all[0].Value.String() != "5000.0000 BRL" {
		t.Errorf("point[0] = {%s, %s}, want {%s, 5000.0000 BRL} (then-current price, not 110)",
			all[0].Date.Format("2006-01-02"), all[0].Value, older.Format("2006-01-02"))
	}
	// recent holding 10×110=1100 → NW 5100.
	if !all[1].Date.Equal(recent) || all[1].Value.String() != "5100.0000 BRL" {
		t.Errorf("point[1] = {%s, %s}, want {%s, 5100.0000 BRL}", all[1].Date.Format("2006-01-02"), all[1].Value, recent.Format("2006-01-02"))
	}
	if !all[2].Date.Equal(today) || all[2].Value.String() != "5100.0000 BRL" {
		t.Errorf("point[2] = {%s, %s}, want {today, 5100.0000 BRL}", all[2].Date.Format("2006-01-02"), all[2].Value)
	}

	// Windowed: from = today-15 excludes the older (today-20) sample but starts at
	// the boundary with the correct as-of value (still the 100 price → 5000).
	win, err := svc.ValueSeries(ctx, today.AddDate(0, 0, -15))
	if err != nil {
		t.Fatalf("ValueSeries(window): %v", err)
	}
	if len(win) != 3 {
		t.Fatalf("ValueSeries(window) len = %d, want 3 (boundary, recent, today)", len(win))
	}
	if !win[0].Date.Equal(today.AddDate(0, 0, -15)) || win[0].Value.String() != "5000.0000 BRL" {
		t.Errorf("window boundary = {%s, %s}, want {today-15, 5000.0000 BRL}", win[0].Date.Format("2006-01-02"), win[0].Value)
	}
	for _, p := range all {
		if p.Partial {
			t.Errorf("point %s unexpectedly partial", p.Date.Format("2006-01-02"))
		}
	}

	// Window that predates all history must NOT add a pre-history boundary point
	// (which would value to 0 and draw a flat-zero lead-in): the line starts at
	// the earliest in-window sample instead.
	pre, err := svc.ValueSeries(ctx, today.AddDate(0, 0, -40))
	if err != nil {
		t.Fatalf("ValueSeries(pre-history): %v", err)
	}
	if len(pre) == 0 || !pre[0].Date.Equal(older) {
		t.Errorf("pre-history window first point = %v, want the earliest sample %s (no flat-zero lead-in)",
			func() string {
				if len(pre) == 0 {
					return "<empty>"
				}
				return pre[0].Date.Format("2006-01-02")
			}(), older.Format("2006-01-02"))
	}
}

// TestValueSeriesPartialAndEmpty: a USD holding with no USD→BRL rate makes the
// points partial; an empty database yields at most a single point (no line).
func TestValueSeriesPartialAndEmpty(t *testing.T) {
	ctx := context.Background()
	base := testDatabaseURL(t)

	// Empty DB → at most one point (today); callers render the empty state.
	emptyURL := isolatedDB(t, base)
	if err := store.Migrate(ctx, emptyURL, db.Migrations); err != nil {
		t.Fatalf("migrate empty: %v", err)
	}
	emptyPool, err := store.NewPool(ctx, emptyURL)
	if err != nil {
		t.Fatalf("pool empty: %v", err)
	}
	defer emptyPool.Close()
	if err := settings.New(emptyPool).SetDisplayCurrency(ctx, money.BRL); err != nil {
		t.Fatalf("set display: %v", err)
	}
	empty, err := New(emptyPool).ValueSeries(ctx, time.Time{})
	if err != nil {
		t.Fatalf("ValueSeries(empty): %v", err)
	}
	if len(empty) > 1 {
		t.Errorf("empty ValueSeries len = %d, want ≤ 1", len(empty))
	}

	// Partial: a USD holding priced with NO USD→BRL rate → Net Worth excludes USD.
	url := isolatedDB(t, base)
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
	svc := New(pool)
	run := time.Now().UnixNano()
	today := dateOnlyUTC(time.Now())
	d1 := today.AddDate(0, 0, -10)
	if err := settings.New(pool).SetDisplayCurrency(ctx, money.BRL); err != nil {
		t.Fatalf("set display: %v", err)
	}
	broker, err := accts.Create(ctx, fmt.Sprintf("USB-%d", run), account.Investment, money.USD)
	if err != nil {
		t.Fatalf("create broker: %v", err)
	}
	sec, err := secs.Create(ctx, fmt.Sprintf("US%d", run), "US Stock", security.Stock, money.USD)
	if err != nil {
		t.Fatalf("create sec: %v", err)
	}
	if _, err := txns.Buy(ctx, broker.ID, sec.ID, req("5"), req("20"), req("0"), d1, "buy"); err != nil {
		t.Fatalf("buy: %v", err)
	}
	if _, err := prices.Add(ctx, sec.ID, d1, req("30")); err != nil {
		t.Fatalf("price: %v", err)
	}
	series, err := svc.ValueSeries(ctx, time.Time{})
	if err != nil {
		t.Fatalf("ValueSeries(partial): %v", err)
	}
	if len(series) == 0 {
		t.Fatal("expected at least one point")
	}
	for _, p := range series {
		if !p.Partial {
			t.Errorf("point %s should be partial (USD has no rate to BRL)", p.Date.Format("2006-01-02"))
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
