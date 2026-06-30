package domain

import (
	"sort"

	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/internal/money"
)

// AllocItem is one priced holding's contribution to the invested-value
// allocation (Story 5.4): a grouping key (label) and its native market value.
// Callers (service) supply only PRICED holdings — unpriced positions are not
// invested value and must not appear here.
type AllocItem struct {
	Key   string      // grouping label: a security symbol OR an account name
	Value money.Money // native market value (qty × price)
}

// AllocSlice is one group's share of the invested value: the converted
// Display-Currency value (rounded once for display, AD-12) and the reconciled
// integer Percent. Across all slices the Percent values sum to EXACTLY 100.
type AllocSlice struct {
	Key     string
	Value   money.Money // Display Currency, round-once
	Percent int         // reconciled; Σ Percent == 100 (largest-remainder)
}

// Allocation is the invested-value breakdown in the Display Currency (the single
// canonical home for allocation, AD-10). Total is the round-once converted
// invested value — equal to NetWorth's PortfolioValue for the same inputs/rates
// (D4). Missing lists native currencies excluded for lack of a rate (partial
// total, AD-6/Q5). Slices are sorted by value descending, then key ascending.
type Allocation struct {
	Slices  []AllocSlice
	Total   money.Money
	Missing []money.Currency
}

// otherKey is the label for the aggregated tail when the group count exceeds the
// legibility cap (D5).
const otherKey = "Other"

// Allocate is the canonical allocation derivation (AD-10): it converts each
// native item to the Display Currency (convert-then-sum at full precision,
// AD-12), groups by key, and computes the per-group percentages from the
// UNROUNDED converted values, reconciling them to EXACTLY 100% via the
// largest-remainder method. A non-zero item in a currency with no rate is
// excluded and its currency recorded in Missing (never inverted/guessed, AD-6) —
// mirroring NetWorth's convert-then-sum. When topN > 0 and there are more than
// topN groups, the smallest groups are folded into a single "Other" slice BEFORE
// reconciliation, so the displayed percents still sum to 100 (D5). All arithmetic
// is decimal — no float (NFR-5).
func Allocate(display money.Currency, items []AllocItem, rates map[money.Currency]decimal.Decimal, topN int) Allocation {
	missing := make(map[money.Currency]bool)

	// Convert each item to display at full precision and accumulate per key.
	// firstSeen preserves a stable order for keys that tie on value below.
	totals := make(map[string]decimal.Decimal)
	firstSeen := make(map[string]int)
	order := 0
	for _, it := range items {
		cur := it.Value.Currency()
		var conv decimal.Decimal
		switch {
		case cur == display:
			conv = it.Value.Amount()
		case hasRate(rates, cur):
			conv = money.Convert(it.Value, rates[cur], display).Amount()
		default:
			if !it.Value.Amount().IsZero() {
				missing[cur] = true
			}
			continue
		}
		if _, ok := totals[it.Key]; !ok {
			firstSeen[it.Key] = order
			order++
		}
		totals[it.Key] = totals[it.Key].Add(conv)
	}

	miss := sortedCurrencies(missing)

	// group is a key's unrounded converted total, awaiting reconciliation.
	type group struct {
		key string
		val decimal.Decimal
	}
	groups := make([]group, 0, len(totals))
	for k, v := range totals {
		groups = append(groups, group{key: k, val: v})
	}
	// Deterministic order: value desc, then first-seen, then key — so the donut,
	// legend, Other-cap and remainder distribution are all stable.
	sort.Slice(groups, func(i, j int) bool {
		if !groups[i].val.Equal(groups[j].val) {
			return groups[i].val.GreaterThan(groups[j].val)
		}
		if firstSeen[groups[i].key] != firstSeen[groups[j].key] {
			return firstSeen[groups[i].key] < firstSeen[groups[j].key]
		}
		return groups[i].key < groups[j].key
	})

	totalUnrounded := decimal.Zero
	for _, g := range groups {
		totalUnrounded = totalUnrounded.Add(g.val)
	}
	total := money.New(totalUnrounded, display).Rounded()

	if len(groups) == 0 || !totalUnrounded.IsPositive() {
		return Allocation{Total: total, Missing: miss}
	}

	// D5 — legibility cap: fold the tail beyond topN into a single "Other" group
	// (already sorted value desc) before computing percents.
	if topN > 0 && len(groups) > topN {
		tail := decimal.Zero
		for _, g := range groups[topN:] {
			tail = tail.Add(g.val)
		}
		groups = append(groups[:topN:topN], group{key: otherKey, val: tail})
	}

	vals := make([]decimal.Decimal, len(groups))
	for i, g := range groups {
		vals[i] = g.val
	}
	percents := largestRemainder(vals)

	slices := make([]AllocSlice, len(groups))
	for i, g := range groups {
		slices[i] = AllocSlice{
			Key:     g.key,
			Value:   money.New(g.val, display).Rounded(),
			Percent: percents[i],
		}
	}
	return Allocation{Slices: slices, Total: total, Missing: miss}
}

// largestRemainder distributes 100 integer percentage points across the given
// (positive) values in proportion to their share of the total, reconciling to
// EXACTLY 100 (AD-12): each share is floored, then the leftover whole points are
// handed to the largest fractional remainders (ties broken by larger value, then
// by earlier index for determinism). Returns one percent per input value, in the
// same order. The input order is assumed already deterministic.
func largestRemainder(vals []decimal.Decimal) []int {
	n := len(vals)
	out := make([]int, n)
	if n == 0 {
		return out
	}
	total := decimal.Zero
	for _, v := range vals {
		total = total.Add(v)
	}
	if !total.IsPositive() {
		return out
	}

	hundred := decimal.NewFromInt(100)
	type rem struct {
		idx  int
		frac decimal.Decimal
		val  decimal.Decimal
	}
	rems := make([]rem, n)
	assigned := 0
	for i, v := range vals {
		share := v.Mul(hundred).Div(total) // full precision (DivisionPrecision)
		floor := share.Floor()
		out[i] = int(floor.IntPart())
		assigned += out[i]
		rems[i] = rem{idx: i, frac: share.Sub(floor), val: v}
	}

	remaining := 100 - assigned
	if remaining <= 0 {
		return out
	}
	sort.SliceStable(rems, func(a, b int) bool {
		if !rems[a].frac.Equal(rems[b].frac) {
			return rems[a].frac.GreaterThan(rems[b].frac)
		}
		if !rems[a].val.Equal(rems[b].val) {
			return rems[a].val.GreaterThan(rems[b].val)
		}
		return rems[a].idx < rems[b].idx
	})
	for i := 0; i < remaining && i < n; i++ {
		out[rems[i].idx]++
	}
	return out
}

// sortedCurrencies returns the currencies present in the set, deduplicated and
// ordered by code — matching NetWorth's Missing ordering.
func sortedCurrencies(set map[money.Currency]bool) []money.Currency {
	codes := make([]string, 0, len(set))
	for c := range set {
		codes = append(codes, string(c))
	}
	sort.Strings(codes)
	out := make([]money.Currency, 0, len(codes))
	for _, c := range codes {
		out = append(out, money.Currency(c))
	}
	return out
}
