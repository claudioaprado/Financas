package money

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestMoneyDisplay(t *testing.T) {
	cases := []struct {
		amount string
		cur    Currency
		want   string
	}{
		{"0", USD, "0,00 USD"},
		{"1234.56", BRL, "1.234,56 BRL"},
		{"1234.5600", BRL, "1.234,56 BRL"},   // stored 4dp, shown 2dp
		{"1234.567", BRL, "1.234,57 BRL"},    // banker's rounding to 2dp
		{"1000000", USD, "1.000.000,00 USD"}, // millions grouping
		{"-2500.5", BRL, "-2.500,50 BRL"},    // negative
		{"-0.001", BRL, "0,00 BRL"},          // rounds to zero, no "-0,00"
		{"12.5", USD, "12,50 USD"},
		{"999.999", USD, "1.000,00 USD"}, // rounds up across grouping
	}
	for _, c := range cases {
		got := New(decimal.RequireFromString(c.amount), c.cur).Display()
		if got != c.want {
			t.Errorf("Display(%s %s) = %q, want %q", c.amount, c.cur, got, c.want)
		}
	}
}

func TestFormatDecimal(t *testing.T) {
	cases := []struct{ in, want string }{
		{"3", "3"},
		{"1.5", "1,5"},
		{"1234.5678", "1.234,5678"},
		{"-12.3", "-12,3"},
		{"0", "0"},
		{"1000000.1", "1.000.000,1"},
		{"120.00", "120"}, // decimal.String() normalizes trailing zeros (good for quantities)
	}
	for _, c := range cases {
		got := FormatDecimal(decimal.RequireFromString(c.in))
		if got != c.want {
			t.Errorf("FormatDecimal(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

// String stays canonical (4dp, dot decimal) — the financial/debug representation
// is unchanged; only Display is Brazilian.
func TestStringRemainsCanonical(t *testing.T) {
	got := New(decimal.RequireFromString("1234.5"), BRL).String()
	if got != "1234.5000 BRL" {
		t.Errorf("String() = %q, want canonical 1234.5000 BRL", got)
	}
}
