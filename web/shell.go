package web

import "strconv"

// accountID renders an account's numeric id as a string for use in form fields.
func accountID(id int64) string { return strconv.FormatInt(id, 10) }

// ShellData carries the chrome state for the authenticated app shell.
type ShellData struct {
	OwnerName       string // shown in the greeting header
	Active          string // Key of the active nav section
	DisplayCurrency string // ISO-4217 code shown in the header (Story 2.1)
}

// RateRow is one exchange-rate row formatted for display.
type RateRow struct {
	From          string
	To            string
	EffectiveDate string
	Rate          string
}

// AccountRow is one account formatted for display. BalanceLabel names the
// balance the account's type carries ("Cash balance" for cash/investment,
// "Balance owed" for credit); the value itself is derived from the transaction
// ledger in later epics (AD-2), so it renders as a placeholder for now.
type AccountRow struct {
	ID           int64
	Name         string
	Type         string
	Currency     string
	BalanceLabel string
	Archived     bool
}

// NavItem is one top-navigation entry.
type NavItem struct {
	Label string
	Href  string
	Key   string
}

// NavItems is the ordered top navigation (UX-DR1). Targets beyond Dashboard are
// built in later epics; Story 1.4 ships navigable placeholders.
var NavItems = []NavItem{
	{Label: "Dashboard", Href: "/", Key: "dashboard"},
	{Label: "Investments", Href: "/investments", Key: "investments"},
	{Label: "Transactions", Href: "/transactions", Key: "transactions"},
	{Label: "Accounts", Href: "/accounts", Key: "accounts"},
	{Label: "Analytics", Href: "/analytics", Key: "analytics"},
}
