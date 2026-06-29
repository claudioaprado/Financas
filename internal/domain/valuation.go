package domain

import (
	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/internal/money"
)

// ValueHolding is the single canonical valuation of a derived Holding at a price
// (AD-10): it returns the market value (quantity × price) and the unrealized
// Gain/Loss (market value − cost basis), BOTH in the holding's native currency.
//
// Same-currency-only (the Epic-4 trade rule) means the price is already quoted in
// the holding's currency — there is NO FX here; Display-Currency convert-then-sum
// aggregation across accounts is Story 4.4 (AD-12). Rounding to the money scale
// happens once, at this display boundary (banker's rounding via money.New/Rounded).
//
// The caller decides whether a price exists at all; ValueHolding is only called
// when one does. CostBasis is already at money scale, so unrealized gain is the
// exact difference of the (rounded) market value and the cost basis.
func ValueHolding(h Holding, price decimal.Decimal) (marketValue, unrealizedGain money.Money) {
	cur := h.CostBasis.Currency()
	marketValue = money.New(h.Quantity.Mul(price), cur).Rounded()
	unrealizedGain = money.New(marketValue.Amount().Sub(h.CostBasis.Amount()), cur)
	return marketValue, unrealizedGain
}
