package domain

import (
	"testing"

	"github.com/claudioaprado/financas/internal/money"
)

// holdingFor builds a Holding with the given quantity and cost basis in BRL.
func holdingFor(qty, basis string) Holding {
	return Holding{
		SecurityID:   1,
		Quantity:     dec(qty),
		CostBasis:    money.New(dec(basis), money.BRL),
		RealizedGain: money.New(dec("0"), money.BRL),
	}
}

func TestValueHolding(t *testing.T) {
	tests := []struct {
		name           string
		qty, basis     string
		price          string
		wantMarket     string
		wantUnrealized string
	}{
		{"gain", "150", "1653.75", "16.00", "2400.0000 BRL", "746.2500 BRL"},
		{"loss", "150", "1653.75", "10.00", "1500.0000 BRL", "-153.7500 BRL"},
		{"break-even", "100", "1005.00", "10.05", "1005.0000 BRL", "0.0000 BRL"},
		// Fractional quantity × price carries >4dp and must round once (banker's):
		// 3.5 × 10.125 = 35.4375 -> 35.4375 (already 4dp), basis 30 -> +5.4375.
		{"fractional rounds once", "3.5", "30.00", "10.125", "35.4375 BRL", "5.4375 BRL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := holdingFor(tt.qty, tt.basis)
			market, unrealized := ValueHolding(h, dec(tt.price))
			if got := market.String(); got != tt.wantMarket {
				t.Errorf("market value = %s, want %s", got, tt.wantMarket)
			}
			if got := unrealized.String(); got != tt.wantUnrealized {
				t.Errorf("unrealized gain = %s, want %s", got, tt.wantUnrealized)
			}
			// Market value is in the holding's native currency.
			if market.Currency() != money.BRL {
				t.Errorf("market currency = %s, want BRL", market.Currency())
			}
		})
	}
}

// TestValueHoldingRoundsHalfEven proves banker's rounding at the display boundary:
// 1.5 × 0.00005 = 0.000075 rounds to 0.0001 is wrong for half-even at 4dp; use a
// value whose 5th digit is exactly 5 with an even/odd 4th digit to lock the mode.
func TestValueHoldingRoundsHalfEven(t *testing.T) {
	// 1 × 2.00005 = 2.00005 -> half-even at 4dp -> 2.0000 (4th digit 0 is even).
	h := holdingFor("1", "2.0000")
	market, _ := ValueHolding(h, dec("2.00005"))
	if got := market.String(); got != "2.0000 BRL" {
		t.Errorf("half-even round = %s, want 2.0000 BRL", got)
	}
	// 1 × 2.00015 -> 2.0002 (4th digit 1 is odd, rounds up to even 2).
	h2 := holdingFor("1", "2.0000")
	market2, _ := ValueHolding(h2, dec("2.00015"))
	if got := market2.String(); got != "2.0002 BRL" {
		t.Errorf("half-even round = %s, want 2.0002 BRL", got)
	}
}
