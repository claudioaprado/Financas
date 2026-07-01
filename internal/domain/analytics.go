package domain

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/internal/money"
)

// LedgerEntry is one categorized-or-not income/expense transaction feeding the
// analytics view (Story 8.3 / FR-19). Amount is the native magnitude (>= 0) in its
// account's currency; Rate is the native→Display effective rate on the entry's
// date (ignored when already in the Display Currency). HasRate is false when no
// rate existed on that date — the amount is then excluded and its currency
// surfaced as a partial-total notice (AD-6), never guessed. Category is the
// category name, empty for an uncategorized entry.
type LedgerEntry struct {
	Type     string // "income" | "expense"
	Year     int
	Month    time.Month
	Category string
	Amount   money.Money
	Rate     decimal.Decimal
	HasRate  bool
}

// CategorySpend is one expense category's total over the window, in the Display
// Currency, with a reconciled integer Percent (Σ Percent == 100 across the set).
type CategorySpend struct {
	Category string
	Total    money.Money
	Percent  int
}

// MonthFlow is one month's income vs expense totals in the Display Currency.
type MonthFlow struct {
	Year    int
	Month   time.Month
	Income  money.Money
	Expense money.Money
}

// Analytics is the derived spending & cash-flow view (Story 8.3) — never stored
// (AD-2/AD-10). Spending is expense-by-category (largest first, reconciled
// percents); Flow is one entry per window month in chronological order (months
// with no activity appear as zero, so the series is continuous).
type Analytics struct {
	Spending []CategorySpend
	Flow     []MonthFlow
	Missing  []money.Currency
}

// Analyze derives the spending breakdown and monthly cash-flow for the `months`
// months ending at (anchorYear, anchorMonth), all in the Display Currency.
// Conversion is convert-then-sum at each entry's own effective rate, at full
// precision, rounded ONCE per figure (AD-12); an entry whose currency has no rate
// on its date is excluded and its currency recorded in Missing (AD-6). Entries
// outside the window are ignored. `months` is clamped to at least 1.
func Analyze(display money.Currency, anchorYear int, anchorMonth time.Month, months int, entries []LedgerEntry) Analytics {
	if months < 1 {
		months = 1
	}
	endIdx := monthIndex(anchorYear, anchorMonth)
	startIdx := endIdx - (months - 1)
	missing := map[money.Currency]bool{}

	convert := func(e LedgerEntry) (decimal.Decimal, bool) {
		cur := e.Amount.Currency()
		switch {
		case cur == display:
			return e.Amount.Amount(), true
		case e.HasRate:
			return money.Convert(e.Amount, e.Rate, display).Amount(), true
		case !e.Amount.Amount().IsZero():
			missing[cur] = true
		}
		return decimal.Zero, false
	}

	spendByCat := map[string]decimal.Decimal{}
	type flow struct{ income, expense decimal.Decimal }
	flowByMonth := map[int]*flow{}

	for _, e := range entries {
		idx := monthIndex(e.Year, e.Month)
		if idx < startIdx || idx > endIdx {
			continue // outside the window
		}
		amt, ok := convert(e)
		if !ok {
			continue
		}
		f := flowByMonth[idx]
		if f == nil {
			f = &flow{}
			flowByMonth[idx] = f
		}
		if e.Type == "income" {
			f.income = f.income.Add(amt)
		} else {
			f.expense = f.expense.Add(amt)
			spendByCat[e.Category] = spendByCat[e.Category].Add(amt)
		}
	}

	// Spending: sort categories by total desc (tie by name), reconcile percents.
	spending := make([]CategorySpend, 0, len(spendByCat))
	for name, total := range spendByCat {
		spending = append(spending, CategorySpend{Category: name, Total: money.New(total, display).Rounded()})
	}
	sort.Slice(spending, func(a, b int) bool {
		if !spending[a].Total.Amount().Equal(spending[b].Total.Amount()) {
			return spending[a].Total.Amount().GreaterThan(spending[b].Total.Amount())
		}
		return spending[a].Category < spending[b].Category
	})
	vals := make([]decimal.Decimal, len(spending))
	for i, s := range spending {
		vals[i] = s.Total.Amount()
	}
	pcts := largestRemainder(vals)
	for i := range spending {
		spending[i].Percent = pcts[i]
	}

	// Flow: one entry per window month, chronological, zero-filled.
	flowRows := make([]MonthFlow, 0, months)
	for idx := startIdx; idx <= endIdx; idx++ {
		y, m := yearMonthOf(idx)
		var inc, exp decimal.Decimal
		if f := flowByMonth[idx]; f != nil {
			inc, exp = f.income, f.expense
		}
		flowRows = append(flowRows, MonthFlow{
			Year:    y,
			Month:   m,
			Income:  money.New(inc, display).Rounded(),
			Expense: money.New(exp, display).Rounded(),
		})
	}

	return Analytics{Spending: spending, Flow: flowRows, Missing: sortedCurrencies(missing)}
}

// yearMonthOf is the inverse of monthIndex: it maps a running month number back to
// its calendar year and month.
func yearMonthOf(idx int) (int, time.Month) {
	return idx / 12, time.Month(idx%12 + 1)
}
