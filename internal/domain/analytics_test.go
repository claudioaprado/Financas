package domain

import (
	"testing"
	"time"

	"github.com/claudioaprado/financas/internal/money"
)

func entry(typ string, y int, m time.Month, cat string, amount money.Money) LedgerEntry {
	return LedgerEntry{Type: typ, Year: y, Month: m, Category: cat, Amount: amount, HasRate: true}
}

func TestAnalyzeSpendingAndFlow(t *testing.T) {
	// Window = 3 months ending June 2026 (Apr, May, Jun). Expenses across Food and
	// Rent; income in June. May has no activity (zero-filled).
	entries := []LedgerEntry{
		entry("expense", 2026, time.April, "Food", brl("100")),
		entry("expense", 2026, time.June, "Food", brl("300")),
		entry("expense", 2026, time.June, "Rent", brl("600")),
		entry("income", 2026, time.June, "Salary", brl("2000")),
	}

	a := Analyze(money.BRL, 2026, time.June, 3, entries)

	// Spending: Rent 600 (60%), Food 400 (40%); Σ percent == 100.
	if len(a.Spending) != 2 {
		t.Fatalf("spending = %+v", a.Spending)
	}
	if a.Spending[0].Category != "Rent" || a.Spending[0].Total.String() != "600.0000 BRL" || a.Spending[0].Percent != 60 {
		t.Fatalf("spending[0] = %+v", a.Spending[0])
	}
	if a.Spending[1].Category != "Food" || a.Spending[1].Total.String() != "400.0000 BRL" || a.Spending[1].Percent != 40 {
		t.Fatalf("spending[1] = %+v", a.Spending[1])
	}
	if a.Spending[0].Percent+a.Spending[1].Percent != 100 {
		t.Fatalf("percents do not sum to 100: %+v", a.Spending)
	}

	// Flow: three chronological months; May is zero.
	if len(a.Flow) != 3 {
		t.Fatalf("flow = %+v", a.Flow)
	}
	if a.Flow[0].Month != time.April || a.Flow[0].Expense.String() != "100.0000 BRL" || a.Flow[0].Income.String() != "0.0000 BRL" {
		t.Fatalf("flow[0] = %+v", a.Flow[0])
	}
	if a.Flow[1].Month != time.May || a.Flow[1].Expense.String() != "0.0000 BRL" || a.Flow[1].Income.String() != "0.0000 BRL" {
		t.Fatalf("flow[1] = %+v", a.Flow[1])
	}
	if a.Flow[2].Month != time.June || a.Flow[2].Income.String() != "2000.0000 BRL" || a.Flow[2].Expense.String() != "900.0000 BRL" {
		t.Fatalf("flow[2] = %+v", a.Flow[2])
	}
}

func TestAnalyzeConvertsAndFlagsMissing(t *testing.T) {
	// Display BRL. June expenses: BRL 50 + USD 20 @5 → 100 BRL (Food total 150),
	// and a EUR 10 with no rate → excluded and surfaced in Missing.
	entries := []LedgerEntry{
		{Type: "expense", Year: 2026, Month: time.June, Category: "Food", Amount: brl("50"), HasRate: true},
		{Type: "expense", Year: 2026, Month: time.June, Category: "Food", Amount: usd("20"), Rate: dec("5"), HasRate: true},
		{Type: "expense", Year: 2026, Month: time.June, Category: "Food", Amount: money.New(dec("10"), money.Currency("EUR")), HasRate: false},
	}

	a := Analyze(money.BRL, 2026, time.June, 1, entries)
	if len(a.Spending) != 1 || a.Spending[0].Total.String() != "150.0000 BRL" || a.Spending[0].Percent != 100 {
		t.Fatalf("spending = %+v", a.Spending)
	}
	if len(a.Missing) != 1 || a.Missing[0] != money.Currency("EUR") {
		t.Fatalf("missing = %v, want [EUR]", a.Missing)
	}
	if a.Flow[0].Expense.String() != "150.0000 BRL" {
		t.Fatalf("flow expense = %+v", a.Flow[0])
	}
}

func TestAnalyzeWindowExcludesOutside(t *testing.T) {
	// A 1-month window (June) ignores entries in other months.
	entries := []LedgerEntry{
		entry("expense", 2026, time.May, "Food", brl("999")),
		entry("expense", 2026, time.June, "Food", brl("40")),
		entry("expense", 2026, time.July, "Food", brl("999")),
	}
	a := Analyze(money.BRL, 2026, time.June, 1, entries)
	if len(a.Flow) != 1 || a.Flow[0].Month != time.June || a.Flow[0].Expense.String() != "40.0000 BRL" {
		t.Fatalf("flow = %+v", a.Flow)
	}
	if len(a.Spending) != 1 || a.Spending[0].Total.String() != "40.0000 BRL" {
		t.Fatalf("spending = %+v", a.Spending)
	}
}

func TestAnalyzeUncategorizedExpense(t *testing.T) {
	// An uncategorized expense groups under the empty category (the caller labels
	// it); it still counts in the month's cash-flow.
	entries := []LedgerEntry{entry("expense", 2026, time.June, "", brl("70"))}
	a := Analyze(money.BRL, 2026, time.June, 1, entries)
	if len(a.Spending) != 1 || a.Spending[0].Category != "" || a.Spending[0].Total.String() != "70.0000 BRL" {
		t.Fatalf("spending = %+v", a.Spending)
	}
}

func TestAnalyzeEmpty(t *testing.T) {
	// No entries: spending empty, flow zero-filled across the window, no missing.
	a := Analyze(money.BRL, 2026, time.June, 2, nil)
	if len(a.Spending) != 0 || len(a.Missing) != 0 || len(a.Flow) != 2 {
		t.Fatalf("a = %+v", a)
	}
	if a.Flow[0].Month != time.May || a.Flow[1].Month != time.June {
		t.Fatalf("flow months = %+v", a.Flow)
	}
	for _, f := range a.Flow {
		if f.Income.String() != "0.0000 BRL" || f.Expense.String() != "0.0000 BRL" {
			t.Fatalf("non-zero flow = %+v", f)
		}
	}
}
