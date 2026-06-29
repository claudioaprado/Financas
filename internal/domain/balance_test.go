package domain

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/internal/money"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestAccountBalance(t *testing.T) {
	const acct int64 = 1
	const other int64 = 2

	txns := []BalanceTxn{
		{ToAccountID: acct, ToAmount: dec("100")},                                  // income +100
		{FromAccountID: acct, FromAmount: dec("30")},                               // expense -30
		{FromAccountID: acct, FromAmount: dec("20.50")},                            // expense -20.50
		{ToAccountID: other, ToAmount: dec("999")},                                 // unrelated account, ignored
		{FromAccountID: acct, FromAmount: dec("40"), ToAccountID: other, ToAmount: dec("40")}, // transfer out -40
	}

	got := AccountBalance(acct, money.USD, txns)
	if !got.Amount().Equal(dec("9.50")) { // 100 - 30 - 20.50 - 40
		t.Errorf("AccountBalance = %s, want 9.50 USD", got.Amount())
	}
	if got.Currency() != money.USD {
		t.Errorf("currency = %s, want USD", got.Currency())
	}

	// The transfer credits `other`; its balance is +999 + 40 = 1039.
	if b := AccountBalance(other, money.BRL, txns); !b.Amount().Equal(dec("1039")) {
		t.Errorf("other balance = %s, want 1039", b.Amount())
	}

	// No legs -> zero in the given currency.
	if b := AccountBalance(acct, money.USD, nil); !b.Amount().IsZero() {
		t.Errorf("empty balance = %s, want 0", b.Amount())
	}
}
