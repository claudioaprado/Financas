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
	html := renderToString(t, DashboardPage(ShellData{OwnerName: "Ada", Active: "dashboard"}))
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

func TestActiveNavMarked(t *testing.T) {
	html := renderToString(t, ComingSoon(ShellData{OwnerName: "Ada", Active: "accounts"}, "Accounts"))
	if !strings.Contains(html, `aria-current="page"`) {
		t.Error("active nav section should set aria-current=page")
	}
	if !strings.Contains(html, "Coming soon") {
		t.Error("ComingSoon body missing")
	}
}
