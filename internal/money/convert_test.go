package money

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestConvertNoRoundingFullPrecision(t *testing.T) {
	amt, _ := Parse("10.12345", USD)
	rate := decimal.RequireFromString("5.1234")

	got := Convert(amt, rate, BRL)
	if got.Currency() != BRL {
		t.Fatalf("currency = %s, want BRL", got.Currency())
	}
	want := decimal.RequireFromString("10.12345").Mul(rate)
	if !got.Amount().Equal(want) {
		t.Fatalf("Convert amount = %s, want %s (full precision, no rounding)", got.Amount(), want)
	}
}

// TestConvertThenSumInvariant locks the AD-12 ordering: convert each native leg
// at full precision, sum, then round ONCE. Rounding each leg before summing
// gives a different total — proving the order matters and is not interchangeable.
func TestConvertThenSumInvariant(t *testing.T) {
	rate := decimal.RequireFromString("1") // isolate rounding from scaling
	legs := []string{"0.00005", "0.00005"}

	// Correct: convert-then-sum, round once.
	sum := New(decimal.Zero, BRL)
	for _, l := range legs {
		m, _ := Parse(l, USD)
		sum, _ = sum.Add(Convert(m, rate, BRL))
	}
	roundOnce := sum.Rounded().Amount()

	// Wrong order: round each leg, then sum.
	perLeg := New(decimal.Zero, BRL)
	for _, l := range legs {
		m, _ := Parse(l, USD)
		perLeg, _ = perLeg.Add(Convert(m, rate, BRL).Rounded())
	}
	perLegTotal := perLeg.Amount()

	if !roundOnce.Equal(decimal.RequireFromString("0.0001")) {
		t.Fatalf("convert-then-sum round-once = %s, want 0.0001", roundOnce)
	}
	if !perLegTotal.Equal(decimal.Zero) {
		t.Fatalf("round-each-leg sum = %s, want 0 (each 0.00005 rounds to even 0.0000)", perLegTotal)
	}
	if roundOnce.Equal(perLegTotal) {
		t.Fatal("round-once and per-leg totals must differ to lock the AD-12 invariant")
	}
}
