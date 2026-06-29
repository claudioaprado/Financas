package domain

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/internal/money"
)

func usd(s string) money.Money { return money.New(dec(s), money.USD) }
func brl(s string) money.Money { return money.New(dec(s), money.BRL) }

func TestNetWorthAllSameCurrencyNoRates(t *testing.T) {
	// Display == every native currency, so no rates are needed: totals are exact.
	in := ValuationInput{
		Cash:        []money.Money{brl("1000.0000"), brl("250.5000")},
		Liabilities: []money.Money{brl("300.0000")},
		Holdings:    []money.Money{brl("500.0000"), brl("125.2500")},
	}
	v := NetWorth(money.BRL, in, nil)

	if got, want := v.PortfolioValue.String(), "625.2500 BRL"; got != want {
		t.Errorf("PortfolioValue = %s, want %s", got, want)
	}
	// (1000 + 250.5 + 500 + 125.25) − 300 = 1575.75
	if got, want := v.NetWorth.String(), "1575.7500 BRL"; got != want {
		t.Errorf("NetWorth = %s, want %s", got, want)
	}
	if len(v.Missing) != 0 {
		t.Errorf("Missing = %v, want empty", v.Missing)
	}
}

func TestNetWorthConvertsAndSubtractsLiability(t *testing.T) {
	// Display BRL; a USD holding + USD cash convert via USD->BRL = 5; a BRL
	// liability subtracts in display currency directly.
	rates := map[money.Currency]decimal.Decimal{money.USD: dec("5")}
	in := ValuationInput{
		Cash:        []money.Money{brl("100.0000"), usd("10.0000")}, // 100 + 50 = 150
		Liabilities: []money.Money{brl("30.0000")},
		Holdings:    []money.Money{usd("20.0000")}, // 100 BRL
	}
	v := NetWorth(money.BRL, in, rates)

	if got, want := v.PortfolioValue.String(), "100.0000 BRL"; got != want {
		t.Errorf("PortfolioValue = %s, want %s", got, want)
	}
	// cash 150 + holdings 100 − liab 30 = 220
	if got, want := v.NetWorth.String(), "220.0000 BRL"; got != want {
		t.Errorf("NetWorth = %s, want %s", got, want)
	}
	if len(v.Missing) != 0 {
		t.Errorf("Missing = %v, want empty", v.Missing)
	}
}

func TestNetWorthMissingRatePartialTotal(t *testing.T) {
	// No USD->BRL rate: the USD holding/cash are excluded from BOTH totals and
	// USD is recorded in Missing, while the BRL part still totals (partial).
	in := ValuationInput{
		Cash:        []money.Money{brl("100.0000"), usd("10.0000")},
		Liabilities: nil,
		Holdings:    []money.Money{brl("40.0000"), usd("20.0000")},
	}
	v := NetWorth(money.BRL, in, nil)

	if got, want := v.PortfolioValue.String(), "40.0000 BRL"; got != want {
		t.Errorf("PortfolioValue = %s, want %s (USD excluded)", got, want)
	}
	if got, want := v.NetWorth.String(), "140.0000 BRL"; got != want {
		t.Errorf("NetWorth = %s, want %s (partial, USD excluded)", got, want)
	}
	if len(v.Missing) != 1 || v.Missing[0] != money.USD {
		t.Errorf("Missing = %v, want [USD]", v.Missing)
	}
}

func TestNetWorthZeroAmountUnratedNotFlagged(t *testing.T) {
	// A zero USD amount with no rate must NOT raise a spurious missing warning.
	in := ValuationInput{
		Cash:     []money.Money{brl("100.0000"), usd("0.0000")},
		Holdings: []money.Money{usd("0")},
	}
	v := NetWorth(money.BRL, in, nil)

	if len(v.Missing) != 0 {
		t.Errorf("Missing = %v, want empty (zero unrated amount must not flag)", v.Missing)
	}
	if got, want := v.NetWorth.String(), "100.0000 BRL"; got != want {
		t.Errorf("NetWorth = %s, want %s", got, want)
	}
}

func TestNetWorthMissingDedupedAndSorted(t *testing.T) {
	// Two unrated currencies appearing multiple times across categories →
	// deduped and sorted by code.
	eur := func(s string) money.Money { return money.New(dec(s), money.Currency("EUR")) }
	in := ValuationInput{
		Cash:        []money.Money{usd("1.0000"), eur("1.0000")},
		Liabilities: []money.Money{usd("1.0000")},
		Holdings:    []money.Money{eur("1.0000"), usd("1.0000")},
	}
	v := NetWorth(money.BRL, in, nil)

	if len(v.Missing) != 2 || v.Missing[0] != money.Currency("EUR") || v.Missing[1] != money.USD {
		t.Errorf("Missing = %v, want [EUR USD] (deduped, sorted)", v.Missing)
	}
}

func TestNetWorthConvertThenSumRoundsOnce(t *testing.T) {
	// Two USD holdings of 1.0000 at rate 1.23455 BRL/USD. Convert-then-sum rounds
	// ONCE at the end: 1.23455 + 1.23455 = 2.46910 → 2.4691 BRL.
	// Rounding each first (banker's): 1.23455 → 1.2346, twice → 2.4692 — which a
	// correct convert-then-sum must NOT produce.
	rates := map[money.Currency]decimal.Decimal{money.USD: dec("1.23455")}
	in := ValuationInput{
		Holdings: []money.Money{usd("1.0000"), usd("1.0000")},
	}
	v := NetWorth(money.BRL, in, rates)

	if got, want := v.PortfolioValue.String(), "2.4691 BRL"; got != want {
		t.Errorf("PortfolioValue = %s, want %s (convert-then-sum, round once)", got, want)
	}
}
