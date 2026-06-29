package importer

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestParse(t *testing.T) {
	content := "01/02/24\tGrocery\t-1.234,56\n" + // dd/mm/yy expense 1234.56
		"15/03/2024\tSalary\t5.000,00\n" + // dd/mm/yyyy income 5000.00
		"10/10/70\tOld\t100\n" + // pivot: 1970, income 100
		"31/02/24\tBadDate\t10,00\n" + // invalid calendar date
		"05/05/24\tBadValue\tabc\n" + // non-numeric value
		"06/06/24\tZero\t0,00\n" + // zero value
		"only-two\tfields\n" + // wrong field count
		"\n" + // blank line skipped
		"20/12/24\tRefund\t1.000\n" // BR thousands w/o decimal -> 1000 income

	rows := Parse(content)
	if len(rows) != 8 {
		t.Fatalf("got %d rows, want 8 (blank skipped)", len(rows))
	}

	mustOK := func(r ParsedRow, typ, amount string) {
		t.Helper()
		if !r.OK {
			t.Errorf("line %d (%q) not OK: %s", r.Line, r.Raw, r.Reason)
			return
		}
		if r.Type != typ || !r.Amount.Equal(decimal.RequireFromString(amount)) {
			t.Errorf("line %d = %s/%s, want %s/%s", r.Line, r.Type, r.Amount, typ, amount)
		}
	}

	mustOK(rows[0], "expense", "1234.56")
	if rows[0].Date.Year() != 2024 || rows[0].Date.Month() != 2 || rows[0].Date.Day() != 1 {
		t.Errorf("row0 date = %v, want 2024-02-01", rows[0].Date)
	}
	mustOK(rows[1], "income", "5000")
	mustOK(rows[2], "income", "100")
	if rows[2].Date.Year() != 1970 {
		t.Errorf("pivot: 70 should be 1970, got %d", rows[2].Date.Year())
	}
	if rows[3].OK || rows[3].Reason == "" {
		t.Errorf("31/02 should be an invalid date")
	}
	if rows[4].OK || rows[4].Reason == "" {
		t.Errorf("abc should be an invalid value")
	}
	if rows[5].OK || rows[5].Reason == "" {
		t.Errorf("zero value should be rejected")
	}
	if rows[6].OK || rows[6].Reason == "" {
		t.Errorf("two fields should be rejected")
	}
	mustOK(rows[7], "income", "1000") // 1.000 -> 1000

	// Line numbers are 1-based source lines (blank line 8 skipped, Refund is line 9).
	if rows[7].Line != 9 {
		t.Errorf("Refund line = %d, want 9", rows[7].Line)
	}
}

func TestPivotBoundary(t *testing.T) {
	for _, c := range []struct {
		in   string
		year int
	}{
		{"01/01/69", 2069},
		{"01/01/70", 1970},
		{"01/01/00", 2000},
		{"01/01/99", 1999},
	} {
		got, err := parseBRDate(c.in)
		if err != nil || got.Year() != c.year {
			t.Errorf("parseBRDate(%q) = %v, %v; want year %d", c.in, got.Year(), err, c.year)
		}
	}
}
