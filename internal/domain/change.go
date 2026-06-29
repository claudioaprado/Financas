package domain

import (
	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/internal/money"
)

// PercentChange is the canonical home (AD-10) for a period-over-period percentage
// change between two Display-Currency figures: now vs a prior baseline. It returns
// the signed percentage rounded once to one decimal place with banker's rounding
// (AD-12) and ok=true, or ok=false when the change is undefined — i.e. the two
// figures are in different currencies, or the baseline is zero or negative (a %
// against a non-positive base is meaningless and would render as ±∞). Callers show
// "—" when ok is false (UX-DR8 empty state). All arithmetic is decimal, never
// float (NFR-5).
func PercentChange(now, base money.Money) (decimal.Decimal, bool) {
	if now.Currency() != base.Currency() {
		return decimal.Zero, false
	}
	if !base.Amount().IsPositive() {
		return decimal.Zero, false
	}
	pct := now.Amount().Sub(base.Amount()).
		Div(base.Amount()).
		Mul(decimal.NewFromInt(100))
	return pct.RoundBank(1), true
}
