package money

import "github.com/shopspring/decimal"

// Convert projects amount into the target currency by multiplying by rate
// (units of target currency per one unit of amount's currency). It multiplies
// at FULL precision and does NOT round: rounding to MoneyScale happens once at
// the display boundary via Money.Rounded (AD-12), so aggregates convert-then-sum
// and round only the final total.
//
// Rates are directional and never inverted in code (AD-6); the caller supplies
// the target currency matching rate's direction.
func Convert(amount Money, rate decimal.Decimal, target Currency) Money {
	return Money{amount: amount.amount.Mul(rate), currency: target}
}
