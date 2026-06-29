package money

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// MoneyScale is the number of fractional digits money is rounded to at the
// display boundary — NUMERIC(19,4) per the architecture conventions.
const MoneyScale = 4

// divisionPrecision bounds intermediate decimal-division precision. It is set
// once, here, so every division across the codebase shares the same precision;
// final figures are rounded to MoneyScale at the display boundary (AD-12).
const divisionPrecision = 12

func init() {
	decimal.DivisionPrecision = divisionPrecision
}

// Currency is an ISO-4217 three-letter currency code.
type Currency string

// Supported currencies (the model is not limited to these, but they are the
// ones Financas uses today).
const (
	USD Currency = "USD"
	BRL Currency = "BRL"
)

// Supported returns the currencies Financas supports today, in display order.
func Supported() []Currency {
	return []Currency{USD, BRL}
}

// IsSupported reports whether c is one of the Supported currencies.
func IsSupported(c Currency) bool {
	for _, s := range Supported() {
		if s == c {
			return true
		}
	}
	return false
}

// Valid reports whether c is a well-formed ISO-4217 code (three uppercase
// ASCII letters).
func (c Currency) Valid() bool {
	if len(c) != 3 {
		return false
	}
	for _, r := range c {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// Money is an exact monetary amount in a single native currency. The amount is
// held at full precision; rounding to MoneyScale happens once at the display
// boundary via Rounded (AD-12). The zero value is 0 in an empty currency.
type Money struct {
	amount   decimal.Decimal
	currency Currency
}

// New returns a Money of amount in currency c.
func New(amount decimal.Decimal, c Currency) Money {
	return Money{amount: amount, currency: c}
}

// Parse builds a Money from a decimal string (never from a float, to avoid
// binary rounding) in currency c.
func Parse(amount string, c Currency) (Money, error) {
	if !c.Valid() {
		return Money{}, fmt.Errorf("money: invalid currency %q", c)
	}
	d, err := decimal.NewFromString(amount)
	if err != nil {
		return Money{}, fmt.Errorf("money: parse amount %q: %w", amount, err)
	}
	return Money{amount: d, currency: c}, nil
}

// Amount returns the exact (unrounded) decimal amount.
func (m Money) Amount() decimal.Decimal { return m.amount }

// Currency returns the money's ISO-4217 currency.
func (m Money) Currency() Currency { return m.currency }

// Rounded returns a copy of m with its amount rounded to MoneyScale using
// banker's rounding (half-to-even) — the single display-boundary rounding.
func (m Money) Rounded() Money {
	return Money{amount: m.amount.RoundBank(MoneyScale), currency: m.currency}
}

// String renders the amount fixed at MoneyScale (banker's rounding) with its
// currency, never as a float.
func (m Money) String() string {
	return fmt.Sprintf("%s %s", m.amount.StringFixedBank(MoneyScale), m.currency)
}

// Add returns the sum of m and other; both must share a currency.
func (m Money) Add(other Money) (Money, error) {
	if m.currency != other.currency {
		return Money{}, fmt.Errorf("money: cannot add %s and %s", m.currency, other.currency)
	}
	return Money{amount: m.amount.Add(other.amount), currency: m.currency}, nil
}

// Sub returns m minus other; both must share a currency.
func (m Money) Sub(other Money) (Money, error) {
	if m.currency != other.currency {
		return Money{}, fmt.Errorf("money: cannot subtract %s from %s", other.currency, m.currency)
	}
	return Money{amount: m.amount.Sub(other.amount), currency: m.currency}, nil
}
