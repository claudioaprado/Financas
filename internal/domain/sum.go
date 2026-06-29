package domain

import (
	"sort"

	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/internal/money"
)

// SumByCurrency groups amounts by currency and sums each group exactly (full
// precision, no conversion). It is the single home for this aggregation
// (AD-10); cross-currency conversion to a Display Currency is a separate concern
// (AD-12, Epic 5). The result is ordered by currency code for stable display.
func SumByCurrency(amounts []money.Money) []money.Money {
	totals := make(map[money.Currency]decimal.Decimal)
	for _, m := range amounts {
		totals[m.Currency()] = totals[m.Currency()].Add(m.Amount())
	}
	codes := make([]string, 0, len(totals))
	for c := range totals {
		codes = append(codes, string(c))
	}
	sort.Strings(codes)
	out := make([]money.Money, 0, len(codes))
	for _, c := range codes {
		cur := money.Currency(c)
		out = append(out, money.New(totals[cur], cur))
	}
	return out
}
