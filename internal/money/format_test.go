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

func TestParseDecimal(t *testing.T) {
	ok := []struct{ in, want string }{
		{"1.234,56", "1234.56"},
		{"50,00", "50"},
		{"1234", "1234"},
		{"1234.56", "123456"}, // dot is thousands (BR), so "1234.56" => 123456
		{"1.000.000,10", "1000000.1"},
		{"  12,5  ", "12.5"}, // trimmed
		{"0,3333", "0.3333"},
		{"-2.500,50", "-2500.5"},
		// Intentional dot-as-thousands convention (matches the importer): a dot is
		// ALWAYS a grouping separator, never a decimal point. These therefore parse
		// to the grouped value, NOT the dot-decimal a non-BR user might expect.
		// Pinned so the behavior can't silently drift (the comma is the only decimal).
		{"1.5", "15"},
		{".5", "5"},
		{"12.34", "1234"},
		{"100.00", "10000"},
		{"1.2.3", "123"},
	}
	for _, c := range ok {
		got, err := ParseDecimal(c.in)
		if err != nil {
			t.Errorf("ParseDecimal(%q) error: %v", c.in, err)
			continue
		}
		if got.String() != c.want {
			t.Errorf("ParseDecimal(%q) = %s, want %s", c.in, got.String(), c.want)
		}
	}
	for _, bad := range []string{"", "   ", "abc", "1,2,3", "R$ 5"} {
		if _, err := ParseDecimal(bad); err == nil {
			t.Errorf("ParseDecimal(%q) should error", bad)
		}
	}
}

// Round-trip: FormatDecimal(x) must parse back to x via ParseDecimal.
func TestFormatParseRoundTrip(t *testing.T) {
	for _, s := range []string{"1234.56", "1000000.1", "0.3333", "-2500.5", "42"} {
		d := decimal.RequireFromString(s)
		back, err := ParseDecimal(FormatDecimal(d))
		if err != nil {
			t.Fatalf("round-trip %s: %v", s, err)
		}
		if !back.Equal(d) {
			t.Errorf("round-trip %s -> %q -> %s", s, FormatDecimal(d), back.String())
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
