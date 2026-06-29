package web

import (
	"strings"
	"testing"
)

func TestCardRendersTokensAndPassthrough(t *testing.T) {
	html := renderToString(t, Card("max-w-md"))
	for _, want := range []string{"<section", "rounded-card", "bg-white", "p-6", "shadow-card", "max-w-md"} {
		if !strings.Contains(html, want) {
			t.Errorf("Card missing %q in %s", want, html)
		}
	}
}

func TestBadgeVariantsRenderLabelAndColour(t *testing.T) {
	cases := []struct {
		variant BadgeVariant
		colour  string
	}{
		{BadgeGain, "text-gain"},
		{BadgeLoss, "text-loss"},
		{BadgeAccent, "text-accent"},
		{BadgeNeutral, "text-muted"},
	}
	for _, c := range cases {
		html := renderToString(t, Badge("LABEL", c.variant))
		if !strings.Contains(html, "LABEL") {
			t.Errorf("badge %q missing label: %s", c.variant, html)
		}
		if !strings.Contains(html, c.colour) {
			t.Errorf("badge %q missing colour token %q: %s", c.variant, c.colour, html)
		}
	}
}

func TestAmountGainShowsSignAndColour(t *testing.T) {
	html := renderToString(t, Amount(MoneyText{Display: "80.0000 BRL", Positive: true}, AmountHero))
	// Value, gain colour, hero size, a non-colour sign (+), and an accessible label.
	for _, want := range []string{"80.0000 BRL", "text-gain", "text-hero", "+", "gain"} {
		if !strings.Contains(html, want) {
			t.Errorf("gain amount missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, "text-loss") {
		t.Errorf("gain amount must not be loss-coloured: %s", html)
	}
}

func TestAmountLossShowsSignAndColour(t *testing.T) {
	html := renderToString(t, Amount(MoneyText{Display: "30.0000 BRL", Negative: true}, AmountStat))
	for _, want := range []string{"30.0000 BRL", "text-loss", "text-stat", "−", "loss"} {
		if !strings.Contains(html, want) {
			t.Errorf("loss amount missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, "text-gain") {
		t.Errorf("loss amount must not be gain-coloured: %s", html)
	}
}

func TestAmountNeutralNoSignNoColour(t *testing.T) {
	html := renderToString(t, Amount(MoneyText{Display: "1234.5000 BRL"}, AmountHero))
	if !strings.Contains(html, "1234.5000 BRL") {
		t.Errorf("neutral amount missing value: %s", html)
	}
	if strings.Contains(html, "text-gain") || strings.Contains(html, "text-loss") {
		t.Errorf("neutral amount must not be coloured: %s", html)
	}
	if strings.Contains(html, "−") {
		t.Errorf("neutral amount must not show a sign: %s", html)
	}
}
