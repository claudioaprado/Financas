package domain

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/internal/money"
)

// BudgetTarget is one category's monthly target. v1 applies this single current
// target uniformly to every month of the carryover chain (effective-dated targets
// are a deferred refinement). Amount is in the Display Currency and positive.
type BudgetTarget struct {
	CategoryID int64
	Name       string
	Kind       string // "income" | "expense"
	Amount     decimal.Decimal
}

// BudgetTxn is one categorized income/expense transaction feeding both the
// selected month's actual and the carryover chain. Amount is the native magnitude
// (>= 0) in its account's currency; Rate is the native→Display effective rate on
// the transaction's date (ignored when the transaction is already in the Display
// Currency). HasRate is false when no rate existed on that date — the amount is
// then excluded from the totals and its currency surfaced as a partial-total
// notice (AD-6), never guessed or inverted.
type BudgetTxn struct {
	CategoryID int64
	Year       int
	Month      time.Month
	Amount     money.Money
	Rate       decimal.Decimal
	HasRate    bool
}

// BudgetLine is one budgeted category's standing for the selected month, every
// figure in the Display Currency and rounded once at the boundary (AD-12).
// Carryover and Remaining are signed.
type BudgetLine struct {
	CategoryID int64
	Name       string
	Kind       string
	Target     money.Money // the monthly target
	Carryover  money.Money // Σ prior months (target − actual); signed
	Planned    money.Money // target + carryover
	Actual     money.Money // this month's converted categorized income/expense
	Remaining  money.Money // planned − actual; signed
}

// BudgetReport is the derived budget view for one month — never stored (AD-2/AD-10).
type BudgetReport struct {
	Lines   []BudgetLine
	Missing []money.Currency // native currencies excluded for lack of a rate on a txn date
}

// monthIndex maps a (year, month) to a comparable running month number.
func monthIndex(year int, m time.Month) int { return year*12 + int(m) - 1 }

// Budget is the single canonical home (AD-10) for the planned×actual×remaining
// budget view with rollover, all in the Display Currency.
//
// For the selected month (year, sel) and each budgeted category:
//
//	actual    = Σ that month's categorized income/expense, converted to Display
//	carryover = Σ_{m = firstTxnMonth .. sel-1} (target − actualₘ)
//	planned   = target + carryover
//	remaining = planned − actual
//
// Carryover accumulates from the earliest month in which the category has a
// categorized transaction (so a category with no prior activity has none), and
// v1 applies the current target uniformly across that span — a prior month with
// no spend banks the full target (true rollover of unspent budget). It is derived
// on read, never stored (AD-2/AD-10).
//
// Conversion is convert-then-sum at FULL precision, rounded ONCE per figure with
// banker's rounding (AD-12). A transaction whose native currency has no rate on
// its date is excluded and its currency recorded in Missing (a partial total,
// AD-6) — deduplicated, sorted, and only for a non-zero excluded amount.
func Budget(display money.Currency, year int, sel time.Month, targets []BudgetTarget, txns []BudgetTxn) BudgetReport {
	selIdx := monthIndex(year, sel)
	missing := map[money.Currency]bool{}

	// convert folds a txn's native amount into the Display Currency at full
	// precision, recording an unrated non-zero currency in missing.
	convert := func(t BudgetTxn) (decimal.Decimal, bool) {
		cur := t.Amount.Currency()
		switch {
		case cur == display:
			return t.Amount.Amount(), true
		case t.HasRate:
			return money.Convert(t.Amount, t.Rate, display).Amount(), true
		case !t.Amount.Amount().IsZero():
			missing[cur] = true
		}
		return decimal.Zero, false
	}

	// Per category: converted actual per month index, and the earliest month seen.
	type acc struct {
		byMonth  map[int]decimal.Decimal
		firstIdx int
	}
	perCat := map[int64]*acc{}
	for _, t := range txns {
		idx := monthIndex(t.Year, t.Month)
		if idx > selIdx {
			continue // a future transaction does not affect this month's view
		}
		amt, ok := convert(t)
		if !ok {
			continue
		}
		a := perCat[t.CategoryID]
		if a == nil {
			a = &acc{byMonth: map[int]decimal.Decimal{}, firstIdx: idx}
			perCat[t.CategoryID] = a
		}
		if idx < a.firstIdx {
			a.firstIdx = idx
		}
		a.byMonth[idx] = a.byMonth[idx].Add(amt)
	}

	lines := make([]BudgetLine, 0, len(targets))
	for _, tg := range targets {
		a := perCat[tg.CategoryID]
		carry := decimal.Zero
		if a != nil {
			for m := a.firstIdx; m < selIdx; m++ {
				carry = carry.Add(tg.Amount.Sub(a.byMonth[m])) // absent month → actual 0
			}
		}
		actual := decimal.Zero
		if a != nil {
			actual = a.byMonth[selIdx]
		}
		planned := tg.Amount.Add(carry)
		remaining := planned.Sub(actual)
		lines = append(lines, BudgetLine{
			CategoryID: tg.CategoryID,
			Name:       tg.Name,
			Kind:       tg.Kind,
			Target:     money.New(tg.Amount, display).Rounded(),
			Carryover:  money.New(carry, display).Rounded(),
			Planned:    money.New(planned, display).Rounded(),
			Actual:     money.New(actual, display).Rounded(),
			Remaining:  money.New(remaining, display).Rounded(),
		})
	}

	codes := make([]string, 0, len(missing))
	for c := range missing {
		codes = append(codes, string(c))
	}
	sort.Strings(codes)
	miss := make([]money.Currency, 0, len(codes))
	for _, c := range codes {
		miss = append(miss, money.Currency(c))
	}

	return BudgetReport{Lines: lines, Missing: miss}
}
