package importer

import "strings"

// Rule is one auto-categorization rule (FR-17): a case-insensitive substring
// (MatchText) that suggests CategoryID for rows of a given Kind ("income" or
// "expense"). The service loads rules via store.ListCategoryRules in id order.
type Rule struct {
	MatchText  string
	CategoryID int64
	Kind       string
}

// SuggestCategory returns the category id suggested for a row with the given
// description and kind: the FIRST rule (in slice order) whose Kind matches and
// whose MatchText is a case-insensitive substring of description. Returns 0 when
// no rule matches (the row stays uncategorized). Pure, no I/O.
func SuggestCategory(description, kind string, rules []Rule) int64 {
	desc := strings.ToLower(description)
	for _, r := range rules {
		if r.Kind != kind || r.MatchText == "" {
			continue
		}
		if strings.Contains(desc, strings.ToLower(r.MatchText)) {
			return r.CategoryID
		}
	}
	return 0
}
