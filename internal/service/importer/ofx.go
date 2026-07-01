package importer

import (
	"errors"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// ParseOFX parses an OFX bank/credit-card statement into import rows (FR-16). It
// is pure (no I/O) and tolerant of both OFX flavors:
//
//   - OFX 1.x SGML — leaf tags are unclosed (a value runs to end-of-line or the
//     next tag); aggregates like STMTTRN still carry a closing tag.
//   - OFX 2.x XML — every tag is closed (<TAG>value</TAG>).
//
// Each <STMTTRN>…</STMTTRN> becomes one ParsedRow: TRNAMT sign → type (negative
// ⇒ expense, positive ⇒ income), DTPOSTED → date, NAME (else MEMO) → description,
// FITID → the dedup key (Preview/Commit dedup by (account, FITID) ONLY — never by
// content). A row with no FITID is OK (imported as new); a malformed record is
// flagged OK=false with a Reason and does not abort the batch. No float (NFR-5).
func ParseOFX(content string) []ParsedRow {
	var out []ParsedRow
	upper := strings.ToUpper(content) // case-insensitive tag search; ASCII tags keep byte offsets aligned
	const openTag, closeTag, listEnd = "<STMTTRN>", "</STMTTRN>", "</BANKTRANLIST>"

	for from := 0; ; {
		rel := strings.Index(upper[from:], openTag)
		if rel < 0 {
			break
		}
		open := from + rel
		body := open + len(openTag)
		// A block ends at the EARLIEST delimiter after the opening tag: its own
		// close tag, the next STMTTRN, or the end of the transaction list. Using
		// the next opening tag as a fallback boundary tolerates lenient SGML that
		// omits the </STMTTRN> close, without swallowing following transactions.
		end, next := len(content), len(content)
		if i := strings.Index(upper[body:], closeTag); i >= 0 {
			end, next = body+i, body+i+len(closeTag)
		}
		if i := strings.Index(upper[body:], openTag); i >= 0 && body+i < end {
			end, next = body+i, body+i
		}
		if i := strings.Index(upper[body:], listEnd); i >= 0 && body+i < end {
			end, next = body+i, body+i
		}
		line := 1 + strings.Count(content[:open], "\n")
		out = append(out, parseSTMTTRN(line, content[open:end]))
		from = next
	}
	return out
}

// parseSTMTTRN reads one transaction aggregate into a ParsedRow.
func parseSTMTTRN(line int, block string) ParsedRow {
	row := ParsedRow{Line: line, Raw: collapseWS(block)}

	amtRaw := ofxTag(block, "TRNAMT")
	dtRaw := ofxTag(block, "DTPOSTED")
	switch {
	case amtRaw == "":
		row.Reason = "missing TRNAMT"
		return row
	case dtRaw == "":
		row.Reason = "missing DTPOSTED"
		return row
	}

	date, err := parseOFXDate(dtRaw)
	if err != nil {
		row.Reason = err.Error()
		return row
	}
	val, err := parseOFXAmount(amtRaw)
	if err != nil {
		row.Reason = err.Error()
		return row
	}
	if val.IsZero() {
		row.Reason = "value must be non-zero"
		return row
	}

	desc := ofxTag(block, "NAME")
	if desc == "" {
		desc = ofxTag(block, "MEMO")
	}

	row.Date = date
	row.Description = desc
	row.Amount = val.Abs()
	if val.IsNegative() {
		row.Type = typeExpense
	} else {
		row.Type = typeIncome
	}
	row.FITID = ofxTag(block, "FITID")
	row.OK = true
	return row
}

// ofxTag returns the value of the first <TAG> in block. The value runs from just
// after the opening tag to the first newline or '<' (so it works for both an
// unclosed SGML leaf and a closed XML element), trimmed and entity-unescaped.
func ofxTag(block, tag string) string {
	open := "<" + tag + ">"
	i := strings.Index(strings.ToUpper(block), open)
	if i < 0 {
		return ""
	}
	rest := block[i+len(open):]
	end := len(rest)
	for j := 0; j < len(rest); j++ {
		if c := rest[j]; c == '\n' || c == '\r' || c == '<' {
			end = j
			break
		}
	}
	return html.UnescapeString(strings.TrimSpace(rest[:end]))
}

// parseOFXDate reads the date from an OFX DTPOSTED, which is
// YYYYMMDD[HHMMSS[.XXX]][[±HH:tz]] — only the leading 8-digit date is used (UTC).
// An impossible calendar date (e.g. 20240230) is rejected.
func parseOFXDate(s string) (time.Time, error) {
	bad := errors.New("invalid DTPOSTED")
	if len(s) < 8 {
		return time.Time{}, bad
	}
	y, e1 := strconv.Atoi(s[0:4])
	m, e2 := strconv.Atoi(s[4:6])
	d, e3 := strconv.Atoi(s[6:8])
	if e1 != nil || e2 != nil || e3 != nil {
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

// parseOFXAmount parses an OFX TRNAMT: an optional leading '-' and a '.'-decimal
// number (the OFX spec uses '.' as the decimal separator with no thousands
// grouping). It never uses float (NFR-5).
func parseOFXAmount(s string) (decimal.Decimal, error) {
	v, err := decimal.NewFromString(strings.TrimSpace(s))
	if err != nil {
		return decimal.Decimal{}, errors.New("invalid TRNAMT")
	}
	return v, nil
}

// collapseWS folds any run of whitespace (incl. newlines) into single spaces so a
// multi-line SGML block renders as a compact one-line Raw for the preview.
func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
