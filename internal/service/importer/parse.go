// Package importer parses tab-delimited statement files and imports them as
// Income/Expense transactions (FR-13). Parsing is pure (no I/O); Preview/Commit
// (importer.go) add account validation, idempotency, and the single-transaction
// write. The package is named "importer" because "import" is a Go keyword.
package importer

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/internal/money"
)

// Transaction types an imported row maps to (sign → type). Stored directly as
// the transaction `type`; no dependency on service/transaction (AD-1).
const (
	typeIncome  = "income"
	typeExpense = "expense"
)

// ParsedRow is one line of an import file after parsing. OK is false when the
// line could not be parsed, with Reason explaining why (the batch continues).
type ParsedRow struct {
	Line        int    // 1-based source line number
	Raw         string // the original line
	Date        time.Time
	Description string
	Amount      decimal.Decimal // non-negative magnitude
	Type        string          // "income" | "expense"
	OK          bool
	Reason      string
}

// Parse splits content into rows on newlines and each row into
// date⇥description⇥value, applying the dd/mm/yy[yy] date rule (with the
// two-digit-year pivot) and Brazilian number format. Blank lines are skipped;
// an unparseable line is returned with OK=false and a Reason without aborting
// the rest.
func Parse(content string) []ParsedRow {
	var out []ParsedRow
	for i, raw := range strings.Split(content, "\n") {
		line := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		row := ParsedRow{Line: i + 1, Raw: line}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			row.Reason = "expected 3 tab-separated fields (date, description, value)"
			out = append(out, row)
			continue
		}
		date, err := parseBRDate(strings.TrimSpace(fields[0]))
		if err != nil {
			row.Reason = err.Error()
			out = append(out, row)
			continue
		}
		val, err := parseBRDecimal(strings.TrimSpace(fields[2]))
		if err != nil {
			row.Reason = err.Error()
			out = append(out, row)
			continue
		}
		if val.IsZero() {
			row.Reason = "value must be non-zero"
			out = append(out, row)
			continue
		}
		row.Date = date
		row.Description = strings.TrimSpace(fields[1])
		row.Amount = val.Abs()
		if val.IsNegative() {
			row.Type = typeExpense
		} else {
			row.Type = typeIncome
		}
		row.OK = true
		out = append(out, row)
	}
	return out
}

// parseBRDate parses dd/mm/yy or dd/mm/yyyy. A two-digit year pivots 00–69 →
// 2000s, 70–99 → 1900s. Invalid calendar dates (e.g. 31/02) are rejected.
func parseBRDate(s string) (time.Time, error) {
	bad := errors.New("invalid date (expected dd/mm/yy or dd/mm/yyyy)")
	parts := strings.Split(s, "/")
	if len(parts) != 3 {
		return time.Time{}, bad
	}
	d, e1 := strconv.Atoi(parts[0])
	m, e2 := strconv.Atoi(parts[1])
	y, e3 := strconv.Atoi(parts[2])
	if e1 != nil || e2 != nil || e3 != nil {
		return time.Time{}, bad
	}
	switch len(parts[2]) {
	case 2:
		if y <= 69 {
			y += 2000
		} else {
			y += 1900
		}
	case 4:
		// full year as given
	default:
		return time.Time{}, bad
	}
	if m < 1 || m > 12 || d < 1 || d > 31 {
		return time.Time{}, bad
	}
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	if t.Day() != d || int(t.Month()) != m || t.Year() != y {
		return time.Time{}, bad // normalization changed it ⇒ not a real date
	}
	return t, nil
}

// parseBRDecimal parses a Brazilian-formatted number: '.' is the thousands
// separator and ',' the decimal separator. A leading '-' is kept. It delegates to
// money.ParseDecimal so file import and manual entry share one convention; the
// row-level "invalid value" reason is preserved for the import preview. No float
// is used (NFR-5).
func parseBRDecimal(s string) (decimal.Decimal, error) {
	v, err := money.ParseDecimal(s)
	if err != nil {
		return decimal.Decimal{}, errors.New("invalid value")
	}
	return v, nil
}
