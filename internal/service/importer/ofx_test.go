package importer

import (
	"testing"

	"github.com/shopspring/decimal"
)

// A small OFX 1.x SGML statement (unclosed leaf tags; aggregates keep close
// tags). Six STMTTRN: income w/ FITID, expense w/ FITID+MEMO, a no-FITID
// expense, a bad-date error, and two rows that share date+name+amount but carry
// different FITIDs (must both survive — no content dedup).
const sampleOFXSGML = `OFXHEADER:100
DATA:OFXSGML
VERSION:102
SECURITY:NONE
ENCODING:USASCII

<OFX>
<BANKMSGSRSV1><STMTTRNRS><STMTRS>
<CURDEF>USD
<BANKACCTFROM><ACCTID>123<ACCTTYPE>CHECKING</BANKACCTFROM>
<BANKTRANLIST>
<STMTTRN>
<TRNTYPE>CREDIT
<DTPOSTED>20240315120000.000[-3:BRT]
<TRNAMT>5000.00
<FITID>1001
<NAME>Salary
</STMTTRN>
<STMTTRN>
<TRNTYPE>DEBIT
<DTPOSTED>20240201
<TRNAMT>-1234.56
<FITID>1002
<NAME>Grocery
<MEMO>Supermarket
</STMTTRN>
<STMTTRN>
<DTPOSTED>20240202
<TRNAMT>-10.00
<NAME>NoFitidRow
</STMTTRN>
<STMTTRN>
<DTPOSTED>20240230
<TRNAMT>-5.00
<FITID>1003
<NAME>BadDate
</STMTTRN>
<STMTTRN>
<DTPOSTED>20240210
<TRNAMT>-42.00
<FITID>2001
<NAME>Coffee
</STMTTRN>
<STMTTRN>
<DTPOSTED>20240210
<TRNAMT>-42.00
<FITID>2002
<NAME>Coffee
</STMTTRN>
</BANKTRANLIST>
</STMTRS></STMTTRNRS></BANKMSGSRSV1>
</OFX>
`

func TestParseOFXSGML(t *testing.T) {
	rows := ParseOFX(sampleOFXSGML)
	if len(rows) != 6 {
		t.Fatalf("ParseOFX returned %d rows; want 6", len(rows))
	}

	// Row 0: income 5000 with FITID, date-only from a timestamped DTPOSTED.
	if r := rows[0]; !r.OK || r.Type != typeIncome ||
		!r.Amount.Equal(decimal.RequireFromString("5000.00")) ||
		r.FITID != "1001" || r.Description != "Salary" ||
		r.Date.Format("2006-01-02") != "2024-03-15" {
		t.Errorf("row0 = %+v", r)
	}

	// Row 1: expense 1234.56, description from NAME (MEMO present but NAME wins).
	if r := rows[1]; !r.OK || r.Type != typeExpense ||
		!r.Amount.Equal(decimal.RequireFromString("1234.56")) ||
		r.FITID != "1002" || r.Description != "Grocery" {
		t.Errorf("row1 = %+v", r)
	}

	// Row 2: no FITID — imported as OK/new (never an error, never content-deduped).
	if r := rows[2]; !r.OK || r.FITID != "" || r.Type != typeExpense ||
		!r.Amount.Equal(decimal.RequireFromString("10")) {
		t.Errorf("row2 (no FITID) = %+v", r)
	}

	// Row 3: 30 Feb → invalid date, flagged without aborting the batch.
	if r := rows[3]; r.OK || r.Reason == "" {
		t.Errorf("row3 should be a DTPOSTED error, got %+v", r)
	}

	// Rows 4 & 5: identical date+name+amount, different FITIDs → BOTH parsed.
	if !rows[4].OK || !rows[5].OK || rows[4].FITID != "2001" || rows[5].FITID != "2002" {
		t.Errorf("same-content/different-FITID rows must both parse: %+v / %+v", rows[4], rows[5])
	}
	if rows[4].Description != rows[5].Description || !rows[4].Amount.Equal(rows[5].Amount) {
		t.Errorf("rows 4/5 expected identical content, got %+v / %+v", rows[4], rows[5])
	}
}

// OFX 2.x is XML (closed tags, an XML declaration). The same parser must read it,
// including SGML/XML entity unescaping in NAME.
const sampleOFXXML = `<?xml version="1.0" encoding="UTF-8"?>
<?OFX OFXHEADER="200" VERSION="211" SECURITY="NONE"?>
<OFX>
  <BANKMSGSRSV1><STMTTRNRS><STMTRS>
    <CURDEF>BRL</CURDEF>
    <BANKTRANLIST>
      <STMTTRN>
        <TRNTYPE>CREDIT</TRNTYPE>
        <DTPOSTED>20240101</DTPOSTED>
        <TRNAMT>100.00</TRNAMT>
        <FITID>X1</FITID>
        <NAME>Ac&amp;me Corp</NAME>
      </STMTTRN>
      <STMTTRN>
        <TRNTYPE>DEBIT</TRNTYPE>
        <DTPOSTED>20240102</DTPOSTED>
        <TRNAMT>bogus</TRNAMT>
        <FITID>X2</FITID>
        <NAME>BadAmount</NAME>
      </STMTTRN>
    </BANKTRANLIST>
  </STMTRS></STMTTRNRS></BANKMSGSRSV1>
</OFX>
`

func TestParseOFXXML(t *testing.T) {
	rows := ParseOFX(sampleOFXXML)
	if len(rows) != 2 {
		t.Fatalf("ParseOFX(xml) returned %d rows; want 2", len(rows))
	}
	if r := rows[0]; !r.OK || r.Type != typeIncome ||
		!r.Amount.Equal(decimal.RequireFromString("100")) ||
		r.FITID != "X1" || r.Description != "Ac&me Corp" {
		t.Errorf("xml row0 = %+v (entity should unescape to 'Ac&me Corp')", r)
	}
	if r := rows[1]; r.OK || r.Reason == "" {
		t.Errorf("xml row1 should be a TRNAMT error, got %+v", r)
	}
}

func TestParseOFXEmpty(t *testing.T) {
	if rows := ParseOFX("not an ofx file at all"); len(rows) != 0 {
		t.Errorf("ParseOFX(non-ofx) = %d rows; want 0", len(rows))
	}
}

// Lenient SGML: some banks omit the </STMTTRN> close tag. Each transaction must
// still be parsed separately (delimited by the next <STMTTRN> / </BANKTRANLIST>),
// not swallowed into one giant block.
func TestParseOFXUnclosedAggregates(t *testing.T) {
	const s = `<OFX><BANKTRANLIST>
<STMTTRN>
<DTPOSTED>20240101
<TRNAMT>100.00
<FITID>U1
<NAME>First
<STMTTRN>
<DTPOSTED>20240102
<TRNAMT>-50.00
<FITID>U2
<NAME>Second
</BANKTRANLIST></OFX>`
	rows := ParseOFX(s)
	if len(rows) != 2 {
		t.Fatalf("unclosed aggregates: %d rows; want 2", len(rows))
	}
	if !rows[0].OK || rows[0].FITID != "U1" || rows[0].Description != "First" ||
		!rows[0].Amount.Equal(decimal.RequireFromString("100")) || rows[0].Type != typeIncome {
		t.Errorf("row0 = %+v", rows[0])
	}
	if !rows[1].OK || rows[1].FITID != "U2" || rows[1].Description != "Second" ||
		!rows[1].Amount.Equal(decimal.RequireFromString("50")) || rows[1].Type != typeExpense {
		t.Errorf("row1 = %+v", rows[1])
	}
}

// A zero TRNAMT is rejected (a transaction must move money), flagged per-row.
func TestParseOFXZeroAmount(t *testing.T) {
	const s = `<OFX><BANKTRANLIST>
<STMTTRN><DTPOSTED>20240101<TRNAMT>0.00<FITID>Z<NAME>Nothing</STMTTRN>
</BANKTRANLIST></OFX>`
	rows := ParseOFX(s)
	if len(rows) != 1 || rows[0].OK || rows[0].Reason == "" {
		t.Fatalf("zero TRNAMT should be an error row, got %+v", rows)
	}
}

// When NAME is absent, the description falls back to MEMO.
func TestParseOFXMemoFallback(t *testing.T) {
	const s = `<OFX><BANKTRANLIST>
<STMTTRN><DTPOSTED>20240101<TRNAMT>25.00<FITID>M1<MEMO>Only a memo</STMTTRN>
</BANKTRANLIST></OFX>`
	rows := ParseOFX(s)
	if len(rows) != 1 || !rows[0].OK || rows[0].Description != "Only a memo" {
		t.Fatalf("MEMO fallback: got %+v", rows)
	}
}
