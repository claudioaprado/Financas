package domain

import (
	"errors"
	"sort"

	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/internal/money"
)

// ErrOversold means a sell's quantity exceeds the quantity held at that point in
// the chronological fold — a data inconsistency (e.g. a buy was deleted out from
// under a later sell). The Sell use-case guards against creating one; this guards
// the derivation itself (AD-2: the ledger is truth, correctness is on read).
var ErrOversold = errors.New("domain: sell exceeds holdings")

// TradeEvent is the holding-relevant projection of one investment ledger row,
// in the account's (== security's) native currency. Events MUST be passed
// chronologically (occurred_on ASC, then id ASC) — average cost is order
// sensitive. CashAmount is the dividend's credited cash (unused for buy/sell);
// quantity/price/fees are unused for dividends.
type TradeEvent struct {
	SecurityID int64
	Type       string // "buy" | "sell" | "dividend"
	Quantity   decimal.Decimal
	Price      decimal.Decimal
	Fees       decimal.Decimal
	CashAmount decimal.Decimal
}

// Holding is a derived position in one security (AD-2, never stored): the current
// quantity, the average-cost Cost Basis of the shares still held, and the
// cumulative realized Gain/Loss from sells. All money is in the native currency.
type Holding struct {
	SecurityID   int64
	Quantity     decimal.Decimal
	CostBasis    money.Money
	RealizedGain money.Money
}

// BasisSold is the single canonical cost-basis-of-shares-sold function (AD-2,
// AD-10) feeding BOTH remaining_basis (= basisBefore − BasisSold) and
// realized_gain (= proceeds − BasisSold), so a holding and its realized gain
// reconcile by construction.
//
// Selling the ENTIRE position (qtySold == qtyHeld) wipes the basis exactly
// (returns basisBefore, leaving remaining basis 0) — no proportional-rounding
// crumb that would become a phantom gain. Otherwise it is the average-cost
// share: basisBefore × (qtySold / qtyHeld), rounded once to the money scale with
// banker's rounding (intermediates carry decimal.DivisionPrecision, set to 12 in
// the money package). qtyHeld must be positive (guaranteed by the caller, which
// rejects an oversell before calling this).
func BasisSold(qtySold, qtyHeld, basisBefore decimal.Decimal) decimal.Decimal {
	if qtySold.Equal(qtyHeld) {
		return basisBefore
	}
	return basisBefore.Mul(qtySold.Div(qtyHeld)).RoundBank(money.MoneyScale)
}

// DeriveHoldings folds chronological trade events into per-security holdings
// using average cost (AD-2/AD-10). Buy adds quantity and basis (incl. fees);
// sell removes the proportional basis via BasisSold and accrues realized gain
// (proceeds = quantity×price − fees, fees reduce proceeds not basis); a dividend
// touches neither quantity nor basis (it is cash income, handled by
// AccountBalance). A sell exceeding the held quantity returns ErrOversold.
//
// Zero-quantity holdings are retained in the result (their realized gain is kept)
// so callers can surface cumulative realized G/L; callers hide qty==0 rows from
// the active-positions list. Results are ordered by security id.
func DeriveHoldings(currency money.Currency, events []TradeEvent) ([]Holding, error) {
	type acc struct {
		qty      decimal.Decimal
		basis    decimal.Decimal
		realized decimal.Decimal
	}
	bySecurity := make(map[int64]*acc)
	order := make([]int64, 0)
	get := func(id int64) *acc {
		a, ok := bySecurity[id]
		if !ok {
			a = &acc{}
			bySecurity[id] = a
			order = append(order, id)
		}
		return a
	}

	for _, e := range events {
		switch e.Type {
		case "buy":
			a := get(e.SecurityID)
			a.qty = a.qty.Add(e.Quantity)
			a.basis = a.basis.Add(e.Quantity.Mul(e.Price)).Add(e.Fees)
		case "sell":
			a := get(e.SecurityID)
			if e.Quantity.GreaterThan(a.qty) {
				return nil, ErrOversold
			}
			bs := BasisSold(e.Quantity, a.qty, a.basis)
			proceeds := e.Quantity.Mul(e.Price).Sub(e.Fees)
			a.realized = a.realized.Add(proceeds.Sub(bs))
			a.basis = a.basis.Sub(bs)
			a.qty = a.qty.Sub(e.Quantity)
		case "dividend":
			// Cash income only; no quantity/basis/realized-gain change. The cash
			// is derived by AccountBalance from the ledger's to_amount.
			get(e.SecurityID)
		}
	}

	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	out := make([]Holding, 0, len(order))
	for _, id := range order {
		a := bySecurity[id]
		out = append(out, Holding{
			SecurityID:   id,
			Quantity:     a.qty,
			CostBasis:    money.New(a.basis, currency),
			RealizedGain: money.New(a.realized, currency),
		})
	}
	return out, nil
}
