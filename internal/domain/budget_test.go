package domain

import (
	"testing"
	"time"

	"github.com/claudioaprado/financas/internal/money"
)

// btx builds a categorized income/expense txn already in the Display Currency
// (rate irrelevant).
func btx(catID int64, year int, m time.Month, amount money.Money) BudgetTxn {
	return BudgetTxn{CategoryID: catID, Year: year, Month: m, Amount: amount, HasRate: true}
}

func lineByCat(r BudgetReport, catID int64) (BudgetLine, bool) {
	for _, l := range r.Lines {
		if l.CategoryID == catID {
			return l, true
		}
	}
	return BudgetLine{}, false
}

func TestBudgetNoCarryoverFirstMonth(t *testing.T) {
	// The category's first activity is the selected month, so there is no prior
	// month and thus no carryover: planned == target.
	targets := []BudgetTarget{{CategoryID: 1, Name: "Food", Kind: "expense", Amount: dec("500")}}
	txns := []BudgetTxn{btx(1, 2026, time.June, brl("120")), btx(1, 2026, time.June, brl("80"))}

	r := Budget(money.BRL, 2026, time.June, targets, txns)
	l, ok := lineByCat(r, 1)
	if !ok {
		t.Fatal("missing line for category 1")
	}
	if l.Carryover.String() != "0.0000 BRL" || l.Planned.String() != "500.0000 BRL" ||
		l.Actual.String() != "200.0000 BRL" || l.Remaining.String() != "300.0000 BRL" {
		t.Fatalf("line = %+v", l)
	}
	if len(r.Missing) != 0 {
		t.Fatalf("Missing = %v, want empty", r.Missing)
	}
}

func TestBudgetCarryoverAccumulates(t *testing.T) {
	// Target 100/month. April spent 80 (banks +20), May spent 110 (over by 10 →
	// banks −10). June selected: carryover = 20 − 10 = 10, planned = 110.
	targets := []BudgetTarget{{CategoryID: 1, Name: "Food", Kind: "expense", Amount: dec("100")}}
	txns := []BudgetTxn{
		btx(1, 2026, time.April, brl("80")),
		btx(1, 2026, time.May, brl("110")),
		btx(1, 2026, time.June, brl("90")),
	}

	l, _ := lineByCat(Budget(money.BRL, 2026, time.June, targets, txns), 1)
	if l.Carryover.String() != "10.0000 BRL" || l.Planned.String() != "110.0000 BRL" ||
		l.Actual.String() != "90.0000 BRL" || l.Remaining.String() != "20.0000 BRL" {
		t.Fatalf("line = %+v", l)
	}
}

func TestBudgetEmptyPriorMonthBanksFullTarget(t *testing.T) {
	// Target 100. First activity April (spent 80 → banks +20). May has no txn →
	// banks the full +100. June selected, spent 0: carryover = 20 + 100 = 120,
	// planned = 220, remaining = 220.
	targets := []BudgetTarget{{CategoryID: 1, Name: "Food", Kind: "expense", Amount: dec("100")}}
	txns := []BudgetTxn{btx(1, 2026, time.April, brl("80"))}

	l, _ := lineByCat(Budget(money.BRL, 2026, time.June, targets, txns), 1)
	if l.Carryover.String() != "120.0000 BRL" || l.Planned.String() != "220.0000 BRL" ||
		l.Actual.String() != "0.0000 BRL" || l.Remaining.String() != "220.0000 BRL" {
		t.Fatalf("line = %+v", l)
	}
}

func TestBudgetConvertsAndFlagsMissingRate(t *testing.T) {
	// Display BRL. June actuals: a native BRL 50, a USD 20 @ rate 5 → 100 BRL, and
	// a EUR 10 with no rate → excluded and surfaced in Missing. actual = 150.
	targets := []BudgetTarget{{CategoryID: 1, Name: "Food", Kind: "expense", Amount: dec("500")}}
	txns := []BudgetTxn{
		{CategoryID: 1, Year: 2026, Month: time.June, Amount: brl("50"), HasRate: true},
		{CategoryID: 1, Year: 2026, Month: time.June, Amount: usd("20"), Rate: dec("5"), HasRate: true},
		{CategoryID: 1, Year: 2026, Month: time.June, Amount: money.New(dec("10"), money.Currency("EUR")), HasRate: false},
	}

	r := Budget(money.BRL, 2026, time.June, targets, txns)
	l, _ := lineByCat(r, 1)
	if l.Actual.String() != "150.0000 BRL" || l.Remaining.String() != "350.0000 BRL" {
		t.Fatalf("line = %+v", l)
	}
	if len(r.Missing) != 1 || r.Missing[0] != money.Currency("EUR") {
		t.Fatalf("Missing = %v, want [EUR]", r.Missing)
	}
}

func TestBudgetTargetWithoutTransactions(t *testing.T) {
	// A budgeted category with no transactions at all: no carryover, zero actual,
	// remaining equals the target.
	targets := []BudgetTarget{{CategoryID: 9, Name: "Gifts", Kind: "expense", Amount: dec("75")}}
	l, _ := lineByCat(Budget(money.BRL, 2026, time.June, targets, nil), 9)
	if l.Carryover.String() != "0.0000 BRL" || l.Planned.String() != "75.0000 BRL" ||
		l.Actual.String() != "0.0000 BRL" || l.Remaining.String() != "75.0000 BRL" {
		t.Fatalf("line = %+v", l)
	}
}

func TestBudgetIgnoresFutureTransactions(t *testing.T) {
	// A transaction dated after the selected month must not affect the view.
	targets := []BudgetTarget{{CategoryID: 1, Name: "Food", Kind: "expense", Amount: dec("100")}}
	txns := []BudgetTxn{
		btx(1, 2026, time.June, brl("40")),
		btx(1, 2026, time.July, brl("999")), // future → ignored
	}
	l, _ := lineByCat(Budget(money.BRL, 2026, time.June, targets, txns), 1)
	if l.Actual.String() != "40.0000 BRL" || l.Carryover.String() != "0.0000 BRL" {
		t.Fatalf("line = %+v", l)
	}
}

func TestBudgetIncomeKindCarriedThrough(t *testing.T) {
	// Income categories flow through the same math; the Kind is preserved for the
	// caller to colour. Target 3000, earned 3200 → remaining −200 (exceeded, good
	// for income; the domain stays sign-only).
	targets := []BudgetTarget{{CategoryID: 2, Name: "Salary", Kind: "income", Amount: dec("3000")}}
	txns := []BudgetTxn{btx(2, 2026, time.June, brl("3200"))}
	l, _ := lineByCat(Budget(money.BRL, 2026, time.June, targets, txns), 2)
	if l.Kind != "income" || l.Remaining.String() != "-200.0000 BRL" {
		t.Fatalf("line = %+v", l)
	}
}
