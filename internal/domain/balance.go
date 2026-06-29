package domain

import (
	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/internal/money"
)

// BalanceTxn is the balance-relevant projection of one ledger transaction: the
// accounts it debits (From) and credits (To) and the magnitudes involved. An
// account id of 0 means "no account on that side" (e.g. a plain income has no
// From side, a plain expense no To side). Amounts are non-negative magnitudes
// (AD-4); direction comes from which side an account sits on.
type BalanceTxn struct {
	FromAccountID int64
	FromAmount    decimal.Decimal
	ToAccountID   int64
	ToAmount      decimal.Decimal
}

// AccountBalance is the single canonical derivation of an account's balance
// (AD-10): the sum of amounts crediting the account minus the amounts debiting
// it, over the ledger (AD-2). The result is exact (full precision); rounding to
// the money scale happens once at the display boundary (AD-12). All legs are in
// the account's native currency (AD-5), so the result carries that currency.
func AccountBalance(accountID int64, currency money.Currency, txns []BalanceTxn) money.Money {
	net := decimal.Zero
	for _, t := range txns {
		if t.ToAccountID == accountID {
			net = net.Add(t.ToAmount)
		}
		if t.FromAccountID == accountID {
			net = net.Sub(t.FromAmount)
		}
	}
	return money.New(net, currency)
}
