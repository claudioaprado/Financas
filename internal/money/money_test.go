package money

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestCurrencyValid(t *testing.T) {
	for _, c := range []Currency{USD, BRL, "EUR"} {
		if !c.Valid() {
			t.Errorf("%q should be valid", c)
		}
	}
	for _, c := range []Currency{"US", "usd", "USDD", "12A", ""} {
		if c.Valid() {
			t.Errorf("%q should be invalid", c)
		}
	}
}

func TestSupported(t *testing.T) {
	if got := Supported(); len(got) != 2 || got[0] != USD || got[1] != BRL {
		t.Fatalf("Supported() = %v, want [USD BRL]", got)
	}
	for _, c := range []Currency{USD, BRL} {
		if !IsSupported(c) {
			t.Errorf("IsSupported(%s) = false, want true", c)
		}
	}
	for _, c := range []Currency{"EUR", "GBP", "usd", ""} {
		if IsSupported(c) {
			t.Errorf("IsSupported(%q) = true, want false", c)
		}
	}
}

func TestParseRejectsBadInput(t *testing.T) {
	if _, err := Parse("1.00", "usd"); err == nil {
		t.Error("expected error for invalid currency")
	}
	if _, err := Parse("not-a-number", USD); err == nil {
		t.Error("expected error for non-numeric amount")
	}
}

// TestRoundedBankersHalfEven verifies banker's rounding (half-to-even) at the
// money scale — the exact-half cases are the ones that distinguish it from
// round-half-away.
func TestRoundedBankersHalfEven(t *testing.T) {
	cases := []struct{ in, want string }{
		{"1.00005", "1.0000"}, // 4th digit 0 (even) -> stays
		{"1.00015", "1.0002"}, // 4th digit 1 (odd)  -> up to 2
		{"1.00025", "1.0002"}, // 4th digit 2 (even) -> stays
		{"1.00035", "1.0004"}, // 4th digit 3 (odd)  -> up to 4
	}
	for _, tc := range cases {
		m, err := Parse(tc.in, USD)
		if err != nil {
			t.Fatal(err)
		}
		got := m.Rounded().Amount()
		want := decimal.RequireFromString(tc.want)
		if !got.Equal(want) {
			t.Errorf("Parse(%s).Rounded() = %s, want %s", tc.in, got, want)
		}
	}
}

func TestAddSubCurrencyGuard(t *testing.T) {
	usd, _ := Parse("10.00", USD)
	brl, _ := Parse("5.00", BRL)

	if _, err := usd.Add(brl); err == nil {
		t.Error("Add across currencies should error")
	}
	if _, err := usd.Sub(brl); err == nil {
		t.Error("Sub across currencies should error")
	}

	other, _ := Parse("2.50", USD)
	sum, err := usd.Add(other)
	if err != nil {
		t.Fatalf("same-currency Add error: %v", err)
	}
	if !sum.Amount().Equal(decimal.RequireFromString("12.50")) {
		t.Errorf("Add = %s, want 12.50 USD", sum)
	}
}

func TestStringNeverFloat(t *testing.T) {
	m, _ := Parse("1234.5", BRL)
	if got := m.String(); got != "1234.5000 BRL" {
		t.Errorf("String() = %q, want %q", got, "1234.5000 BRL")
	}
}
