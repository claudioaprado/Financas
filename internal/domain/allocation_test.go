package domain

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/internal/money"
)

// sumPercents is a tiny helper: the reconciliation invariant (Σ Percent == 100)
// is the load-bearing AD-12 guarantee, asserted in every non-empty case.
func sumPercents(slices []AllocSlice) int {
	total := 0
	for _, s := range slices {
		total += s.Percent
	}
	return total
}

func TestAllocateSameCurrencyTwoGroups(t *testing.T) {
	// 30 / 70 split, same currency as display: exact percents, sorted value desc.
	items := []AllocItem{
		{Key: "A", Value: brl("30.0000")},
		{Key: "B", Value: brl("70.0000")},
	}
	a := Allocate(money.BRL, items, nil, 0)

	if got := sumPercents(a.Slices); got != 100 {
		t.Fatalf("Σ percent = %d, want 100", got)
	}
	if len(a.Slices) != 2 {
		t.Fatalf("len slices = %d, want 2", len(a.Slices))
	}
	// Sorted by value descending: B (70) first.
	if a.Slices[0].Key != "B" || a.Slices[0].Percent != 70 {
		t.Errorf("slice[0] = {%s, %d}, want {B, 70}", a.Slices[0].Key, a.Slices[0].Percent)
	}
	if a.Slices[1].Key != "A" || a.Slices[1].Percent != 30 {
		t.Errorf("slice[1] = {%s, %d}, want {A, 30}", a.Slices[1].Key, a.Slices[1].Percent)
	}
	if got, want := a.Total.String(), "100.0000 BRL"; got != want {
		t.Errorf("Total = %s, want %s", got, want)
	}
}

func TestAllocateLargestRemainderThirds(t *testing.T) {
	// Three equal shares: naive rounding gives 33/33/33 = 99. Largest-remainder
	// must reconcile to exactly 100 — the leftover unit goes to the first by the
	// deterministic tie-break (equal value+frac → key ascending → "A").
	items := []AllocItem{
		{Key: "A", Value: brl("1.0000")},
		{Key: "B", Value: brl("1.0000")},
		{Key: "C", Value: brl("1.0000")},
	}
	a := Allocate(money.BRL, items, nil, 0)

	if got := sumPercents(a.Slices); got != 100 {
		t.Fatalf("Σ percent = %d, want 100 (largest-remainder)", got)
	}
	want := map[string]int{"A": 34, "B": 33, "C": 33}
	for _, s := range a.Slices {
		if want[s.Key] != s.Percent {
			t.Errorf("slice %s = %d, want %d", s.Key, s.Percent, want[s.Key])
		}
	}
}

func TestAllocateLargestRemainderMixed(t *testing.T) {
	// Values whose floored shares sum to 99; the extra unit must land on the
	// largest fractional remainder. 10/20/30/15 (total 75):
	//   shares 13.33, 26.66, 40.0, 20.0 → floors 13,26,40,20 = 99, remaining 1.
	//   fracs .33, .66, 0, 0 → the 20-value (.66) wins → 27.
	items := []AllocItem{
		{Key: "ten", Value: brl("10.0000")},
		{Key: "twenty", Value: brl("20.0000")},
		{Key: "thirty", Value: brl("30.0000")},
		{Key: "fifteen", Value: brl("15.0000")},
	}
	a := Allocate(money.BRL, items, nil, 0)
	if got := sumPercents(a.Slices); got != 100 {
		t.Fatalf("Σ percent = %d, want 100", got)
	}
	got := map[string]int{}
	for _, s := range a.Slices {
		got[s.Key] = s.Percent
	}
	want := map[string]int{"thirty": 40, "twenty": 27, "fifteen": 20, "ten": 13}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("percent[%s] = %d, want %d", k, got[k], w)
		}
	}
}

func TestAllocateConvertThenSumMultiCurrency(t *testing.T) {
	// Display BRL; a USD holding converts at USD→BRL = 5 (→ 100 BRL) plus a BRL
	// holding of 40. Percents come from the UNROUNDED converted values and the
	// Total equals the round-once converted sum — the same figure NetWorth would
	// report for PortfolioValue over these holdings/rates (D4 reconciliation).
	rates := map[money.Currency]decimal.Decimal{money.USD: dec("5")}
	items := []AllocItem{
		{Key: "US", Value: usd("20.0000")}, // 100 BRL
		{Key: "BR", Value: brl("40.0000")}, // 40 BRL
	}
	a := Allocate(money.BRL, items, rates, 0)

	if got := sumPercents(a.Slices); got != 100 {
		t.Fatalf("Σ percent = %d, want 100", got)
	}
	if got, want := a.Total.String(), "140.0000 BRL"; got != want {
		t.Errorf("Total = %s, want %s", got, want)
	}
	// Cross-check D4: NetWorth's PortfolioValue over the same holdings/rates.
	nw := NetWorth(money.BRL, ValuationInput{Holdings: []money.Money{usd("20.0000"), brl("40.0000")}}, rates)
	if a.Total.String() != nw.PortfolioValue.String() {
		t.Errorf("Total %s != NetWorth.PortfolioValue %s (D4 reconciliation)", a.Total, nw.PortfolioValue)
	}
	got := map[string]int{}
	for _, s := range a.Slices {
		got[s.Key] = s.Percent
	}
	// 100/140 = 71.43→71, 40/140 = 28.57→28, remaining 1 to the larger frac (.57, BR) → 29.
	if got["US"] != 71 || got["BR"] != 29 {
		t.Errorf("percents = US:%d BR:%d, want US:71 BR:29", got["US"], got["BR"])
	}
}

func TestAllocateMissingRateExcluded(t *testing.T) {
	// No USD→BRL rate: the USD holding is excluded (partial) and USD is recorded
	// in Missing; the BRL slice carries 100% of the allocatable value.
	items := []AllocItem{
		{Key: "US", Value: usd("10.0000")},
		{Key: "BR", Value: brl("40.0000")},
	}
	a := Allocate(money.BRL, items, nil, 0)

	if len(a.Slices) != 1 || a.Slices[0].Key != "BR" || a.Slices[0].Percent != 100 {
		t.Fatalf("slices = %+v, want a single BR=100", a.Slices)
	}
	if len(a.Missing) != 1 || a.Missing[0] != money.USD {
		t.Errorf("Missing = %v, want [USD]", a.Missing)
	}
	if got, want := a.Total.String(), "40.0000 BRL"; got != want {
		t.Errorf("Total = %s, want %s", got, want)
	}
}

func TestAllocateGroupsByKey(t *testing.T) {
	// Two items with the same key sum into one slice (e.g. a security held in two
	// accounts) — proves the convert-then-sum-per-key grouping.
	items := []AllocItem{
		{Key: "X", Value: brl("30.0000")},
		{Key: "X", Value: brl("70.0000")},
	}
	a := Allocate(money.BRL, items, nil, 0)
	if len(a.Slices) != 1 || a.Slices[0].Key != "X" || a.Slices[0].Percent != 100 {
		t.Fatalf("slices = %+v, want a single X=100", a.Slices)
	}
	if got, want := a.Total.String(), "100.0000 BRL"; got != want {
		t.Errorf("Total = %s, want %s", got, want)
	}
}

func TestAllocateEmpty(t *testing.T) {
	a := Allocate(money.BRL, nil, nil, 0)
	if len(a.Slices) != 0 {
		t.Errorf("slices = %+v, want none", a.Slices)
	}
	if got, want := a.Total.String(), "0.0000 BRL"; got != want {
		t.Errorf("Total = %s, want %s", got, want)
	}
	if len(a.Missing) != 0 {
		t.Errorf("Missing = %v, want empty", a.Missing)
	}
}

func TestAllocateTopNCap(t *testing.T) {
	// topN = 2 with 5 groups: the largest two are named, the tail folds into
	// "Other", and the reconciled percents still sum to exactly 100.
	items := []AllocItem{
		{Key: "A", Value: brl("50.0000")},
		{Key: "B", Value: brl("30.0000")},
		{Key: "C", Value: brl("10.0000")},
		{Key: "D", Value: brl("6.0000")},
		{Key: "E", Value: brl("4.0000")},
	}
	a := Allocate(money.BRL, items, nil, 2)

	if len(a.Slices) != 3 {
		t.Fatalf("len slices = %d, want 3 (A, B, Other)", len(a.Slices))
	}
	if got := sumPercents(a.Slices); got != 100 {
		t.Fatalf("Σ percent = %d, want 100", got)
	}
	last := a.Slices[len(a.Slices)-1]
	if last.Key != "Other" || last.Percent != 20 {
		t.Errorf("Other slice = {%s, %d}, want {Other, 20}", last.Key, last.Percent)
	}
}
