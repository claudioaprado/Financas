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
