package money

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// DisplayScale is the number of fraction digits shown to the owner for money
// amounts (Brazilian currency convention: two). It is a PRESENTATION choice and
// is independent of MoneyScale (the financial rounding boundary, AD-12) — stored
// amounts keep full NUMERIC(19,4) precision; only the rendered figure is at two
// places. Use Display for owner-facing money; use String for canonical/debug.
const DisplayScale = 2

// Display renders the amount in Brazilian format — "." for thousands, "," for the
// decimal separator, two fraction digits — followed by the currency code, e.g.
// "1.234,56 BRL". Rounding to DisplayScale uses banker's rounding (the single
// display-boundary rounding, AD-12). The currency code (USD/BRL) stays as a
// suffix so multi-currency figures are unambiguous.
func (m Money) Display() string {
	return formatBR(m.amount.StringFixedBank(DisplayScale)) + " " + string(m.currency)
}

// FormatDecimal renders a bare decimal (a quantity, price, rate, or percentage)
// in Brazilian format — "." thousands, "," decimal — preserving the value's own
// fraction digits (no forced scale), e.g. 1234.5 -> "1.234,5", 3 -> "3". Use it
// for non-money numbers shown to the owner; money goes through Display.
func FormatDecimal(d decimal.Decimal) string {
	return formatBR(d.String())
}

// FormatDecimalFixed renders a bare decimal in Brazilian format with exactly
// `places` fraction digits (banker's rounding) — for figures shown at a fixed
// precision, e.g. a percentage: FormatDecimalFixed(12.3, 1) -> "12,3".
func FormatDecimalFixed(d decimal.Decimal, places int32) string {
	return formatBR(d.StringFixedBank(places))
}

// ParseDecimal parses an owner-typed number in the Brazilian convention — "."
// groups thousands and "," is the decimal separator, e.g. "1.234,56" -> 1234.56,
// "50,00" -> 50, "1234" -> 1234. It is the inverse of FormatDecimal and the
// single canonical home for manual-entry parsing (the importer shares this
// convention). Surrounding whitespace is trimmed; an empty or unparseable string
// is an error. NFR-5: never goes through float.
func ParseDecimal(s string) (decimal.Decimal, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return decimal.Decimal{}, fmt.Errorf("money: empty number")
	}
	norm := strings.ReplaceAll(t, ".", "") // drop thousands separators
	norm = strings.ReplaceAll(norm, ",", ".")
	d, err := decimal.NewFromString(norm)
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("money: invalid number %q", s)
	}
	return d, nil
}

// formatBR converts a canonical decimal string (optional leading "-", a "."
// decimal point, ASCII digits — as produced by decimal.String/StringFixedBank)
// into Brazilian grouping: thousands separated by ".", decimal separator ",".
// A negative sign is dropped when the magnitude is all zeros (no "-0,00").
func formatBR(s string) string {
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")

	intPart, fracPart := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, fracPart = s[:i], s[i+1:]
	}
	if neg && strings.Trim(intPart+fracPart, "0") == "" {
		neg = false // avoid "-0,00"
	}

	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	n := len(intPart)
	for i := 0; i < n; i++ {
		if i > 0 && (n-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteByte(intPart[i])
	}
	if fracPart != "" {
		b.WriteByte(',')
		b.WriteString(fracPart)
	}
	return b.String()
}
