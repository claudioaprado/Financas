package web

import "strconv"

// accountID renders an account's numeric id as a string for use in form fields.
func accountID(id int64) string { return strconv.FormatInt(id, 10) }

// itoa renders an int for an SVG coordinate attribute (Story 5.3 chart).
func itoa(n int) string { return strconv.Itoa(n) }

// countLabel renders a usage count for display.
func countLabel(n int64) string { return strconv.FormatInt(n, 10) }

// BadgeVariant selects a Badge's semantic colour (Story 5.1, UX-DR7 palette).
type BadgeVariant string

const (
	BadgeNeutral BadgeVariant = "neutral"
	BadgeGain    BadgeVariant = "gain"
	BadgeLoss    BadgeVariant = "loss"
	BadgeAccent  BadgeVariant = "accent"
)

// badgeClass maps a BadgeVariant to its token-driven colour classes.
func badgeClass(v BadgeVariant) string {
	switch v {
	case BadgeGain:
		return "bg-gain/10 text-gain"
	case BadgeLoss:
		return "bg-loss/10 text-loss"
	case BadgeAccent:
		return "bg-accent/10 text-accent"
	default:
		return "bg-black/5 text-muted"
	}
}

// AmountSize selects the type-scale size of an Amount (Story 5.1, UX-DR7 scale):
// AmountHero is the Net Worth / portfolio hero number, AmountStat a KPI-card
// figure, AmountInline a value inside running text or a chip.
type AmountSize string

const (
	AmountHero   AmountSize = "hero"
	AmountStat   AmountSize = "stat"
	AmountInline AmountSize = "inline"
)

// amountClass maps an AmountSize to its type-scale size + weight classes.
func amountClass(s AmountSize) string {
	switch s {
	case AmountHero:
		return "text-hero font-bold"
	case AmountStat:
		return "text-stat font-semibold"
	default:
		return "text-base font-medium"
	}
}

// MoneyText is a pre-formatted monetary value for the Amount primitive (UX-DR8).
// Display is the already-formatted string (e.g. "1234.5000 BRL" from
// money.Money.String()); Positive/Negative are the gain/loss flags the handler
// computes from the rounded amount (the 4.3/4.4 convention) — the web layer does
// no math (AD-1). When neither flag is set the amount is neutral (no sign, no
// colour); the sign Amount renders conveys gain/loss without relying on colour
// alone (NFR-4).
type MoneyText struct {
	Display  string
	Positive bool
	Negative bool
}

// DeltaText is a KPI card's pre-formatted period-change delta (Story 5.2). Display
// is the magnitude percentage (e.g. "2.0%") — the Up/Down arrow carries the
// direction, so the value never relies on colour alone (NFR-4). None renders the
// "—" empty state shown until a comparable prior sample exists (UX-DR8).
type DeltaText struct {
	Display string
	Up      bool
	Down    bool
	None    bool
}

// KPICardView is one dashboard summary card (UX-DR2): an icon chip, a muted
// label, the large number (rendered via the Amount primitive with currency +
// sign), and the period-change delta. Icon is a kind key the template maps to a
// small inline glyph. Amount carries the pre-formatted money + gain/loss flags.
type KPICardView struct {
	Label  string
	Icon   string
	Amount MoneyText
	Delta  DeltaText
}

// ChartPoint is one plotted point in the trend chart, in viewBox coordinates,
// with its formatted date + value for a native SVG <title> hover (Story 5.3).
type ChartPoint struct {
	X, Y  int
	Date  string
	Value string
}

// ChartRange is one range-toggle option — a server-reload link (?range=key) with
// Active marking the current window (Story 5.3).
type ChartRange struct {
	Key    string
	Label  string
	Href   string
	Active bool
}

// ChartView is the dashboard's value-over-time trend chart (Story 5.3): the
// pre-computed SVG geometry (Line polyline + Area path + Points), axis labels,
// the range toggle, and a partial-total note. HasData is false when there are
// fewer than two points — Empty then holds the empty-state copy. All geometry is
// computed by the handler (presentation, AD-1); the templ only emits the <svg>.
type ChartView struct {
	HasData    bool
	Line       string // polyline points: "x,y x,y ..."
	Area       string // filled-area path d
	Points     []ChartPoint
	MinLabel   string
	MaxLabel   string
	StartLabel string
	EndLabel   string
	Display    string
	Range      string
	Ranges     []ChartRange
	Partial    bool
	Empty      string
}

// AllocSliceView is one allocation donut slice (Story 5.4): the precomputed
// stroke-dasharray geometry, the colour-token classes (Stroke for the arc, Swatch
// for the legend chip), and the legend text. All geometry is computed by the
// handler (presentation, AD-1).
type AllocSliceView struct {
	DashArray  string // "arc gap" stroke-dasharray
	DashOffset string // stroke-dashoffset (negative, cumulative)
	Stroke     string // arc colour utility, e.g. "stroke-alloc-1"
	Swatch     string // legend chip colour utility, e.g. "bg-alloc-1"
	Key        string // group label (security symbol or account name)
	Percent    int    // reconciled integer percent (slices sum to 100)
	Value      string // Display-Currency value, e.g. "800.0000 BRL"
}

// AllocBy is one breakdown-dimension toggle option — a server-reload link
// (?by=key, range-preserving) with Active marking the current dimension (5.4).
type AllocBy struct {
	Key    string
	Label  string
	Href   string
	Active bool
}

// AllocationView is the dashboard's invested-value allocation card (Story 5.4):
// the precomputed donut slices + legend, the Display currency, the Security/
// Account toggle, and a partial-total note. HasData is false when there are no
// priced holdings — Empty then holds the empty/error copy. Geometry is computed
// by the handler (AD-1); the templ only emits the <svg>.
type AllocationView struct {
	HasData      bool
	Slices       []AllocSliceView
	Total        string
	Display      string
	By           string
	Bys          []AllocBy
	Partial      bool
	MissingCodes string
	Empty        string
}

// InsightView is the dashboard's bold accent call-out (Story 5.5, UX-DR6): a
// single derived insight — the month-over-month Net Worth change — framed as a
// sentence by the handler (the % is the canonical domain figure, AD-1/AD-10).
// HasData is false when no comparable month-start baseline exists; Empty then
// holds the calm fallback copy. Partial flags a partial-total figure.
type InsightView struct {
	HasData  bool
	Text     string // e.g. "Your net worth is up 4.0% this month"
	NetWorth string // current Net Worth (Display Currency), for context
	Up       bool
	Down     bool
	Partial  bool
	Empty    string
}

// DashboardView is the read model for the dashboard (Story 5.2–5.5). Cards is the
// KPI row; Chart is the value-over-time trend; Allocation is the invested-value
// breakdown; Insight is the bold accent call-out; Recent is the recent-activity
// widget (the newest ledger rows, reusing RegisterRow). MissingCodes /
// UnpricedSymbols carry the partial-total notices (same as /investments). When
// ErrMsg is set only the error banner renders.
type DashboardView struct {
	Cards           []KPICardView
	Chart           ChartView
	Allocation      AllocationView
	Insight         InsightView
	Recent          []RegisterRow
	MissingCodes    string
	UnpricedSymbols string
	OversoldSymbols string // joined symbols of inconsistent (oversold) positions, excluded from totals; empty when none
	ErrMsg          string
}

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
	Date         string // display date, dd/mm/aaaa (shown in tables)
	EditDate     string // ISO YYYY-MM-DD, for the edit form <input type=date> (must round-trip through the parser)
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
	UnrealizedPositive bool   // drives green styling when the unrealized G/L is a gain
	UnrealizedNegative bool   // drives red styling when the unrealized G/L is a loss (zero = neither, neutral)
}

// InvestmentsView is the portfolio page read model (Story 4.4): the
// Display-Currency Net Worth + Portfolio value, the per-currency realized G/L
// chips, the cross-account holdings table (each row in its native currency), and
// the partial-total notices. All money is pre-formatted in the handler (the view
// does no math, AD-10/AD-1). When ErrMsg is set the page renders only the error
// banner (a top-level page must surface a load failure, not swallow it).
type InvestmentsView struct {
	NetWorth        string         // Display Currency
	PortfolioValue  string         // Display Currency
	Display         string         // ISO-4217 code of the Display Currency
	Realized        []RealizedChip // cumulative realized G/L, one chip per native currency
	MissingCodes    string         // joined codes excluded from the totals (no rate); empty when none
	UnpricedSymbols string         // joined held symbols with no price; empty when none
	OversoldSymbols string         // joined symbols of inconsistent (oversold) positions, excluded from totals; empty when none
	Holdings        []PortfolioHoldingRow
	ErrMsg          string // when set, the page renders only this error banner
}

// RealizedChip is one cumulative-realized-G/L chip (per native currency). The
// colour flags mirror the 4.3 convention (gain green / loss red / zero neutral).
type RealizedChip struct {
	Amount   string
	Positive bool
	Negative bool
}

// PortfolioHoldingRow is one holding on the cross-account portfolio table, in
// its native currency (same-currency-only — only the page totals are converted).
// The price-dependent fields are populated only when HasPrice is true.
type PortfolioHoldingRow struct {
	Account            string
	Symbol             string
	Name               string
	Currency           string
	Quantity           string
	HasPrice           bool
	Price              string // latest price (native), e.g. "110.0000 BRL"
	PriceDate          string // effective date of that price, e.g. "2026-06-01"
	Valuation          string // market value (qty × price), native
	CostBasis          string // native
	UnrealizedGain     string // market value − cost basis, native
	UnrealizedPositive bool   // gain → green
	UnrealizedNegative bool   // loss → red (zero = neither, neutral)
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
	{Label: "Painel", Href: "/", Key: "dashboard"},
	{Label: "Investimentos", Href: "/investments", Key: "investments"},
	{Label: "Transações", Href: "/transactions", Key: "transactions"},
	{Label: "Contas", Href: "/accounts", Key: "accounts"},
	{Label: "Análises", Href: "/analytics", Key: "analytics"},
}
