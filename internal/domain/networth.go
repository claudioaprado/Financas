package domain

import (
	"sort"

	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/internal/money"
)

// ValuationInput is the native-currency raw material for the cross-account
// Display-Currency aggregation (AD-12). Each element carries its own currency;
// nothing here is converted yet. Cash and Holdings are assets; Liabilities are
// credit balances OWED, supplied as POSITIVE magnitudes (via AmountOwed).
type ValuationInput struct {
	Cash        []money.Money // cash + investment cash balances (assets), native
	Liabilities []money.Money // credit balances owed, as POSITIVE magnitudes, native
	Holdings    []money.Money // per-holding market value, native (priced holdings only)
}

// Valuation is the result of NetWorth, expressed in the Display Currency and
// rounded once at the boundary (AD-12). Missing lists the native currencies that
// had a non-zero amount but NO rate to the Display Currency — they are excluded
// from the totals so the figures are partial, never guessed or inverted (Q5/AD-6).
type Valuation struct {
	PortfolioValue money.Money      // Σ converted Holdings market value
	NetWorth       money.Money      // Σ converted (Cash + Holdings) − Σ converted Liabilities
	Missing        []money.Currency // native currencies with no rate to Display — excluded
}

// NetWorth is the single canonical home (AD-10) for the Portfolio total and Net
// Worth, both in the Display Currency. The two figures share inputs, so they are
// computed in one walk.
//
// Conversion is convert-then-sum (AD-12): each native amount is taken as-is when
// its currency equals display, else converted at FULL precision via money.Convert
// using the exact native→display rate (never inverted, AD-6); a native currency
// with no rate is skipped and recorded in Missing (Q5 — partial total, never
// blocked). The converted amounts are summed at full precision, then rounded ONCE
// to the money scale with banker's rounding for each figure:
//
//	PortfolioValue = round(Σ holdingsConv)
//	NetWorth       = round(Σ cashConv + Σ holdingsConv − Σ liabConv)
//
// Missing is deduplicated, sorted by code, and only includes a currency when a
// NON-ZERO amount was skipped (a zero balance in an unrated currency must not
// raise a spurious warning).
func NetWorth(display money.Currency, in ValuationInput, rates map[money.Currency]decimal.Decimal) Valuation {
	missing := make(map[money.Currency]bool)

	// convertSum folds a native slice into the display currency at full precision,
	// recording any non-zero unrated amount's currency in missing.
	convertSum := func(items []money.Money) decimal.Decimal {
		sum := decimal.Zero
		for _, m := range items {
			cur := m.Currency()
			switch {
			case cur == display:
				sum = sum.Add(m.Amount())
			case hasRate(rates, cur):
				sum = sum.Add(money.Convert(m, rates[cur], display).Amount())
			case !m.Amount().IsZero():
				missing[cur] = true
			}
		}
		return sum
	}

	cashConv := convertSum(in.Cash)
	holdingsConv := convertSum(in.Holdings)
	liabConv := convertSum(in.Liabilities)

	codes := make([]string, 0, len(missing))
	for c := range missing {
		codes = append(codes, string(c))
	}
	sort.Strings(codes)
	miss := make([]money.Currency, 0, len(codes))
	for _, c := range codes {
		miss = append(miss, money.Currency(c))
	}

	return Valuation{
		PortfolioValue: money.New(holdingsConv, display).Rounded(),
		NetWorth:       money.New(cashConv.Add(holdingsConv).Sub(liabConv), display).Rounded(),
		Missing:        miss,
	}
}

// hasRate reports whether a direct native→display rate exists.
func hasRate(rates map[money.Currency]decimal.Decimal, c money.Currency) bool {
	_, ok := rates[c]
	return ok
}
