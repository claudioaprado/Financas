package web

import (
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

func renderToString(t *testing.T, c templ.Component) string {
	t.Helper()
	var sb strings.Builder
	if err := c.Render(context.Background(), &sb); err != nil {
		t.Fatalf("render: %v", err)
	}
	return sb.String()
}

func TestDashboardPageRendersShell(t *testing.T) {
	html := renderToString(t, DashboardPage(ShellData{OwnerName: "Ada", Active: "dashboard"}, DashboardView{}))
	for _, want := range []string{
		"Welcome back", "Ada",
		"Dashboard", "Investments", "Transactions", "Accounts", "Analytics",
		`href="/investments"`, `action="/logout"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("dashboard shell missing %q", want)
		}
	}
}

func TestAllocationCardRendersDonutAndLegend(t *testing.T) {
	view := AllocationView{
		HasData: true,
		Total:   "5000.0000 BRL",
		Display: "BRL",
		By:      "security",
		Bys: []AllocBy{
			{Key: "security", Label: "Security", Href: "/?range=1y&by=security", Active: true},
			{Key: "account", Label: "Account", Href: "/?range=1y&by=account"},
		},
		Partial:      true,
		MissingCodes: "USD",
		Slices: []AllocSliceView{
			{DashArray: "351.858 87.965", DashOffset: "-0.000", Stroke: "stroke-alloc-1", Swatch: "bg-alloc-1", Key: "AAPL", Percent: 80, Value: "4000.0000 BRL"},
			{DashArray: "87.965 351.858", DashOffset: "-351.858", Stroke: "stroke-alloc-2", Swatch: "bg-alloc-2", Key: "PETR4", Percent: 20, Value: "1000.0000 BRL"},
		},
	}
	html := renderToString(t, allocationCard(view))
	for _, want := range []string{
		"Allocation", "(", "BRL", "Portfolio allocation", "<svg",
		"stroke-dasharray", "351.858 87.965", "stroke-alloc-1", "bg-alloc-1",
		"AAPL", "80%", "4000.0000 BRL", "PETR4", "20%", "1000.0000 BRL",
		"Total", "5000.0000 BRL",
		"Security", "Account", `aria-current="true"`,
		"Allocation excludes", "USD", "/exchange-rates",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("allocation card missing %q", want)
		}
	}
}

func TestAllocationCardEmptyState(t *testing.T) {
	html := renderToString(t, allocationCard(AllocationView{By: "security", Empty: "No invested holdings to allocate yet."}))
	if !strings.Contains(html, "No invested holdings to allocate yet.") {
		t.Errorf("empty allocation should render the empty copy")
	}
	if strings.Contains(html, "stroke-dasharray") {
		t.Errorf("empty allocation should not render donut arcs")
	}
}

func TestDashboardPageRendersKPICards(t *testing.T) {
	view := DashboardView{
		Cards: []KPICardView{
			{Label: "Net worth", Icon: "networth",
				Amount: MoneyText{Display: "1234.5000 BRL"},
				Delta:  DeltaText{Display: "2.0%", Up: true}},
			{Label: "Total gain/loss", Icon: "gainloss",
				Amount: MoneyText{Display: "100.0000 BRL", Positive: true},
				Delta:  DeltaText{Display: "1.1%", Down: true}},
			{Label: "Cash", Icon: "cash",
				Amount: MoneyText{Display: "434.5000 BRL"},
				Delta:  DeltaText{None: true}},
		},
	}
	html := renderToString(t, DashboardPage(ShellData{OwnerName: "Ada", Active: "dashboard"}, view))

	// Labels + figures.
	for _, want := range []string{"Net worth", "1234.5000 BRL", "Total gain/loss", "100.0000 BRL", "Cash", "434.5000 BRL"} {
		if !strings.Contains(html, want) {
			t.Errorf("KPI card missing %q", want)
		}
	}
	// Gain value carries a sign + gain colour (NFR-4, via Amount): "+" and text-gain.
	if !strings.Contains(html, "text-gain") || !strings.Contains(html, "+") {
		t.Error("gain figure should show a sign and gain colour")
	}
	// Up delta: ▲ with sr-only "up" and gain colour; down delta: ▼ with loss colour.
	for _, want := range []string{"▲", "up", "2.0%", "▼", "down", "1.1%", "text-loss"} {
		if !strings.Contains(html, want) {
			t.Errorf("delta rendering missing %q", want)
		}
	}
	// No-prior-sample card renders the "—" empty state.
	if !strings.Contains(html, "—") {
		t.Error(`delta empty state "—" missing`)
	}
}

func TestDashboardPageRendersTrendChart(t *testing.T) {
	view := DashboardView{
		Chart: ChartView{
			HasData:    true,
			Line:       "24,250 500,120 976,24",
			Area:       "M24,276 L24,250 L500,120 L976,24 L976,276 Z",
			Points:     []ChartPoint{{X: 24, Y: 250, Date: "2026-06-01", Value: "5000.0000 BRL"}},
			MinLabel:   "5000.0000 BRL",
			MaxLabel:   "5300.0000 BRL",
			StartLabel: "2026-06-01",
			EndLabel:   "2026-06-20",
			Display:    "BRL",
			Range:      "1y",
			Partial:    true,
			Ranges: []ChartRange{
				{Key: "1m", Label: "1M", Href: "/?range=1m"},
				{Key: "1y", Label: "1Y", Href: "/?range=1y", Active: true},
			},
		},
	}
	html := renderToString(t, DashboardPage(ShellData{Active: "dashboard"}, view))
	for _, want := range []string{
		"Net worth over time", "BRL",
		"<svg", "<polyline", "24,250 500,120 976,24",
		"5000.0000 BRL", "5300.0000 BRL", "2026-06-01", "2026-06-20",
		"<title>2026-06-01: 5000.0000 BRL</title>",
		`href="/?range=1m"`, "Some points are partial",
		`aria-current="true">1Y`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("trend chart missing %q", want)
		}
	}
}

func TestDashboardPageChartEmptyState(t *testing.T) {
	view := DashboardView{Chart: ChartView{HasData: false, Empty: "Not enough history yet — soon!"}}
	html := renderToString(t, DashboardPage(ShellData{Active: "dashboard"}, view))
	if !strings.Contains(html, "Not enough history yet") {
		t.Error("empty chart should render the empty-state copy")
	}
	if strings.Contains(html, "<polyline") {
		t.Error("empty chart should not render a line")
	}
}

func TestDashboardPageErrorBanner(t *testing.T) {
	html := renderToString(t, DashboardPage(ShellData{Active: "dashboard"}, DashboardView{ErrMsg: "boom"}))
	if !strings.Contains(html, "boom") || !strings.Contains(html, "text-loss") {
		t.Error("error banner should render the message with loss styling")
	}
	if strings.Contains(html, "Net worth") {
		t.Error("error banner should replace the KPI row, not render cards")
	}
}

func TestActiveNavMarked(t *testing.T) {
	html := renderToString(t, ComingSoon(ShellData{OwnerName: "Ada", Active: "accounts"}, "Accounts"))
	if !strings.Contains(html, `aria-current="page"`) {
		t.Error("active nav section should set aria-current=page")
	}
	if !strings.Contains(html, "Coming soon") {
		t.Error("ComingSoon body missing")
	}
}
