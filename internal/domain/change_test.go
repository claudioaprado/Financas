package domain

import "testing"

func TestPercentChangeUp(t *testing.T) {
	pct, ok := PercentChange(brl("103.2000"), brl("100.0000"))
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if got, want := pct.StringFixed(1), "3.2"; got != want {
		t.Errorf("pct = %s, want %s", got, want)
	}
}

func TestPercentChangeDown(t *testing.T) {
	pct, ok := PercentChange(brl("98.9000"), brl("100.0000"))
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if got, want := pct.StringFixed(1), "-1.1"; got != want {
		t.Errorf("pct = %s, want %s", got, want)
	}
}

func TestPercentChangeFlat(t *testing.T) {
	pct, ok := PercentChange(brl("100.0000"), brl("100.0000"))
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if got, want := pct.StringFixed(1), "0.0"; got != want {
		t.Errorf("pct = %s, want %s", got, want)
	}
}

func TestPercentChangeZeroBaseUndefined(t *testing.T) {
	if _, ok := PercentChange(brl("100.0000"), brl("0.0000")); ok {
		t.Error("ok = true for zero base, want false (undefined)")
	}
}

func TestPercentChangeNegativeBaseUndefined(t *testing.T) {
	// A negative baseline (e.g. liabilities exceeded assets) makes % sign
	// ambiguous — undefined, render "—".
	if _, ok := PercentChange(brl("10.0000"), brl("-50.0000")); ok {
		t.Error("ok = true for negative base, want false (undefined)")
	}
}

func TestPercentChangeMismatchedCurrencyUndefined(t *testing.T) {
	if _, ok := PercentChange(usd("110.0000"), brl("100.0000")); ok {
		t.Error("ok = true for mismatched currency, want false")
	}
}

func TestPercentChangeRoundsHalfEven(t *testing.T) {
	// 0.05% rounds to 0.0 (even); 0.15% rounds to 0.2 (even) — banker's, 1 dp.
	if pct, _ := PercentChange(brl("100050.0000"), brl("100000.0000")); pct.StringFixed(1) != "0.0" {
		t.Errorf("0.05%% rounded = %s, want 0.0 (banker's)", pct.StringFixed(1))
	}
	if pct, _ := PercentChange(brl("100150.0000"), brl("100000.0000")); pct.StringFixed(1) != "0.2" {
		t.Errorf("0.15%% rounded = %s, want 0.2 (banker's)", pct.StringFixed(1))
	}
}
