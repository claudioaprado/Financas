package web

import "strconv"

// accountID renders an account's numeric id as a string for use in form fields.
func accountID(id int64) string { return strconv.FormatInt(id, 10) }

// countLabel renders a usage count for display.
func countLabel(n int64) string { return strconv.FormatInt(n, 10) }

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

// TxRow is one transaction formatted for an account's register. Amount is the
// raw magnitude (for editing); Signed is the display string with a +/- sign and
// currency derived from the account's perspective. Incoming (a credit to this
// account) drives green/red styling. Counterparty names the other account for
// transfers; Editable is false for transfers (corrected via delete + recreate).
type TxRow struct {
	ID           int64
	Type         string // "income" | "expense" | "transfer"
	Date         string // YYYY-MM-DD
	Description  string
	Counterparty string // other account name (transfers only)
	Category     string // assigned category name (income/expense only)
	CategoryID   int64  // for pre-selecting on edit
	Amount       string // magnitude, for the edit form
	Signed       string // e.g. "+100.0000 USD" / "-30.0000 USD"
	Incoming     bool   // true when the row credits this account
	Editable     bool   // income/expense only
	Security     string // security symbol for trade rows (buy/sell/dividend)
	Quantity     string // shares for trade rows
	Price        string // per-share price for trade rows
}

// HoldingRow is one derived position on an investment account's detail page
// (Story 4.2): all figures pre-formatted; values are derived on read (AD-2).
// The price-dependent fields (Story 4.3) are populated only when HasPrice is
// true; otherwise the page renders "—" for them.
type HoldingRow struct {
	Symbol             string
	Name               string
	Quantity           string
	AvgCost            string
	CostBasis          string
	RealizedGain       string
	HasPrice           bool
	Price              string // latest price (native), e.g. "16.0000 BRL"
	PriceDate          string // effective date of that price, e.g. "2024-06-01"
	MarketValue        string // quantity × price (native)
	UnrealizedGain     string // market value − cost basis (native)
	UnrealizedNegative bool   // drives red styling when the unrealized G/L is a loss
}

// PriceRow is one effective-dated security price on the prices page (Story 4.3),
// pre-formatted for display.
type PriceRow struct {
	Symbol        string
	EffectiveDate string
	Price         string
}

// SecurityChoice is one option in an investment account's trade <select>
// (filtered to securities whose quote currency matches the account).
type SecurityChoice struct {
	ID     int64
	Symbol string
}

// TransferTarget is a destination account option in the transfer form.
type TransferTarget struct {
	ID       int64
	Name     string
	Currency string
}

// CategoryOption is a category choice in the income/expense form (kind groups
// the options so the owner picks a matching one).
type CategoryOption struct {
	ID   int64
	Name string
	Kind string // "income" | "expense"
}

// CategoryRow is one category on the categories page, with its usage count.
type CategoryRow struct {
	ID    int64
	Name  string
	Kind  string
	Count int64
}

// CategoryTxRow is one transaction in a category summary.
type CategoryTxRow struct {
	Account     string
	Date        string
	Description string
	Amount      string // formatted money, e.g. "30.0000 USD"
}

// ImportRow is one previewed import line (Story 3.6). For error rows Date/Type/
// Amount are empty and Reason explains why; Raw is the original line.
type ImportRow struct {
	Line        int
	Date        string
	Description string
	Type        string
	Amount      string
	Status      string // "new" | "duplicate" | "error"
	Reason      string
	Raw         string
}

// SecurityTypeOption is one security-type choice in the create form (value is
// stored lowercase, label is display-cased).
type SecurityTypeOption struct {
	Value string
	Label string
}

// SecurityRow is one security on the securities page.
type SecurityRow struct {
	Symbol        string
	Name          string
	TypeLabel     string
	QuoteCurrency string
}

// FilterOption is one <option> in a register filter dropdown.
type FilterOption struct {
	ID    int64
	Label string
}

// RegisterRow is one transaction in the cross-account register (UX-DR5). Amount
// is the composed display string (signed for income/expense, neutral legs for
// transfers); Incoming/IsTransfer drive the colour.
type RegisterRow struct {
	ID          int64
	Date        string
	Type        string
	Description string
	Category    string
	Security    string // security symbol for trade rows (buy/sell/dividend)
	Account     string
	Amount      string
	Incoming    bool
	IsTransfer  bool
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
