package web

// ShellData carries the chrome state for the authenticated app shell.
type ShellData struct {
	OwnerName       string // shown in the greeting header
	Active          string // Key of the active nav section
	DisplayCurrency string // ISO-4217 code shown in the header (Story 2.1)
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
