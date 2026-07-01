package importer

import "testing"

func TestSuggestCategory(t *testing.T) {
	rules := []Rule{
		{MatchText: "uber", CategoryID: 10, Kind: "expense"},
		{MatchText: "market", CategoryID: 11, Kind: "expense"},
		{MatchText: "salary", CategoryID: 20, Kind: "income"},
		{MatchText: "e", CategoryID: 12, Kind: "expense"}, // broad; must lose to earlier matches
	}

	cases := []struct {
		name string
		desc string
		kind string
		want int64
	}{
		{"case-insensitive substring", "UBER trip downtown", "expense", 10},
		{"first match wins", "market run", "expense", 11},                                 // "market" (id order) before the broad "e"
		{"broad rule wins only when earlier ones miss", "expensive item", "expense", 12},  // "e" is the only match
		{"kind scoping — income rule not applied to expense row", "salary", "expense", 0}, // "salary" (income) skipped; no 'e'
		{"income row matches income rule", "monthly SALARY", "income", 20},
		{"no rule matches", "xyz", "income", 0},
		{"expense rule never suggested on income row", "uber", "income", 0},
	}
	for _, c := range cases {
		if got := SuggestCategory(c.desc, c.kind, rules); got != c.want {
			t.Errorf("%s: SuggestCategory(%q,%q) = %d; want %d", c.name, c.desc, c.kind, got, c.want)
		}
	}

	if got := SuggestCategory("anything", "expense", nil); got != 0 {
		t.Errorf("no rules should suggest 0, got %d", got)
	}
}
