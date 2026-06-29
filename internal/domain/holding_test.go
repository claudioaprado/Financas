package domain

import (
	"errors"
	"testing"

	"github.com/claudioaprado/financas/internal/money"
)

func TestBasisSold(t *testing.T) {
	// Zero-crossing: selling the entire position wipes the basis exactly — no
	// proportional rounding, even when the proportional value would differ.
	if got := BasisSold(dec("100"), dec("100"), dec("1005.0001")); !got.Equal(dec("1005.0001")) {
		t.Errorf("full-position sell: got %s, want exact basisBefore 1005.0001", got)
	}
	// Partial: basisBefore × (qtySold/qtyHeld), rounded once to money scale.
	// 2205 × (50/200) = 551.25 exactly.
	if got := BasisSold(dec("50"), dec("200"), dec("2205")); !got.Equal(dec("551.25")) {
		t.Errorf("partial sell basis: got %s, want 551.25", got)
	}
	// Rounding: 100 × (1/3) = 33.3333... → banker's round to 33.3333 at scale 4.
	if got := BasisSold(dec("1"), dec("3"), dec("100")); !got.Equal(dec("33.3333")) {
		t.Errorf("rounded basis: got %s, want 33.3333", got)
	}
}

func TestDeriveHoldings(t *testing.T) {
	const sec = int64(7)
	usd := money.USD

	// Two buys then a partial sell then the rest, plus a dividend.
	events := []TradeEvent{
		{SecurityID: sec, Type: "buy", Quantity: dec("100"), Price: dec("10.00"), Fees: dec("5.00")}, // basis 1005
		{SecurityID: sec, Type: "buy", Quantity: dec("100"), Price: dec("12.00"), Fees: dec("0")},    // basis += 1200 -> 2205, qty 200
		{SecurityID: sec, Type: "dividend", CashAmount: dec("40.00")},                                // no qty/basis change
		{SecurityID: sec, Type: "sell", Quantity: dec("50"), Price: dec("15.00"), Fees: dec("3.00")}, // proceeds 747, basis_sold 551.25
	}
	hs, err := DeriveHoldings(usd, events)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if len(hs) != 1 {
		t.Fatalf("got %d holdings, want 1", len(hs))
	}
	h := hs[0]
	if !h.Quantity.Equal(dec("150")) {
		t.Errorf("qty after partial sell = %s, want 150", h.Quantity)
	}
	// remaining basis = 2205 − 551.25 = 1653.75
	if !h.CostBasis.Amount().Equal(dec("1653.75")) {
		t.Errorf("cost basis = %s, want 1653.75", h.CostBasis.Amount())
	}
	// realized = proceeds 747 − basis_sold 551.25 = 195.75
	if !h.RealizedGain.Amount().Equal(dec("195.75")) {
		t.Errorf("realized gain = %s, want 195.75", h.RealizedGain.Amount())
	}
	if h.CostBasis.Currency() != usd || h.RealizedGain.Currency() != usd {
		t.Errorf("holding currency not USD: %+v", h)
	}

	// Now sell the entire remaining 150 @ 16, fee 0 → exact wipe.
	events = append(events, TradeEvent{SecurityID: sec, Type: "sell", Quantity: dec("150"), Price: dec("16.00"), Fees: dec("0")})
	hs, err = DeriveHoldings(usd, events)
	if err != nil {
		t.Fatalf("derive after full sell: %v", err)
	}
	h = hs[0]
	if !h.Quantity.IsZero() {
		t.Errorf("qty after full sell = %s, want 0", h.Quantity)
	}
	if !h.CostBasis.Amount().IsZero() {
		t.Errorf("cost basis after full sell = %s, want exactly 0 (no crumb)", h.CostBasis.Amount())
	}
	// realized adds (150×16 − 0) − 1653.75 = 2400 − 1653.75 = 746.25 → total 195.75 + 746.25 = 942.00
	if !h.RealizedGain.Amount().Equal(dec("942")) {
		t.Errorf("cumulative realized = %s, want 942", h.RealizedGain.Amount())
	}

	// remaining + basis_sold == basis_before reconciliation is guaranteed by the
	// shared BasisSold (checked here implicitly: basis went 2205 → 1653.75 → 0).
}

func TestDeriveHoldingsOversold(t *testing.T) {
	const sec = int64(1)
	events := []TradeEvent{
		{SecurityID: sec, Type: "buy", Quantity: dec("10"), Price: dec("5"), Fees: dec("0")},
		{SecurityID: sec, Type: "sell", Quantity: dec("11"), Price: dec("6"), Fees: dec("0")},
	}
	if _, err := DeriveHoldings(money.USD, events); !errors.Is(err, ErrOversold) {
		t.Errorf("oversell err = %v, want ErrOversold", err)
	}
}

func TestDeriveHoldingsDividendOnly(t *testing.T) {
	// A dividend with no buy: holding present but zero qty/basis, no realized gain.
	events := []TradeEvent{{SecurityID: 3, Type: "dividend", CashAmount: dec("12.34")}}
	hs, err := DeriveHoldings(money.BRL, events)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if len(hs) != 1 || !hs[0].Quantity.IsZero() || !hs[0].CostBasis.Amount().IsZero() || !hs[0].RealizedGain.Amount().IsZero() {
		t.Errorf("dividend-only holding = %+v, want zero qty/basis/realized", hs)
	}
}
