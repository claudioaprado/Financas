package domain

import (
	"sort"

	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/internal/money"
)

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
//
// The rounded proportional share is clamped to never exceed basisBefore: when the
// basis carries sub-scale digits and the ratio is near 1, banker's rounding could
// otherwise push the result just past basisBefore and leave a sub-cent NEGATIVE
// remaining basis. Clamping guarantees remaining basis (basisBefore − result) is
// never negative for any partial sell.
func BasisSold(qtySold, qtyHeld, basisBefore decimal.Decimal) decimal.Decimal {
	if qtySold.Equal(qtyHeld) {
		return basisBefore
	}
	bs := basisBefore.Mul(qtySold.Div(qtyHeld)).RoundBank(money.MoneyScale)
	if bs.GreaterThan(basisBefore) {
		return basisBefore
	}
	return bs
}

// DeriveHoldings folds chronological trade events into per-security holdings
// using average cost (AD-2/AD-10). Buy adds quantity and basis (incl. fees);
// sell removes the proportional basis via BasisSold and accrues realized gain
// (proceeds = quantity×price − fees, fees reduce proceeds not basis); a dividend
// touches neither quantity nor basis (it is cash income, handled by
// AccountBalance).
//
// A sell exceeding the held quantity marks that security OVERSOLD — an
// inconsistent position (e.g. a buy was deleted under a later sell). The
// oversold security is EXCLUDED from the returned holdings and its id is returned
// in the (sorted) second result, so one broken position never hides or blocks the
// others (per-security isolation; the ledger is truth, correctness is on read).
// Once a security is oversold its remaining events are ignored — none of its
// derived figures can be trusted, so callers exclude it from valuation and warn.
//
// Zero-quantity (closed) holdings are retained in the result (their realized gain
// is kept) so callers can surface cumulative realized G/L; callers hide qty==0
// rows from the active-positions list. Holdings and oversold ids are ordered by
// security id.
func DeriveHoldings(currency money.Currency, events []TradeEvent) ([]Holding, []int64) {
	type acc struct {
		qty      decimal.Decimal
		basis    decimal.Decimal
		realized decimal.Decimal
	}
	bySecurity := make(map[int64]*acc)
	order := make([]int64, 0)
	oversold := make(map[int64]bool)
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
		if oversold[e.SecurityID] {
			continue // security already inconsistent; ignore its remaining events
		}
		switch e.Type {
		case "buy":
			a := get(e.SecurityID)
			a.qty = a.qty.Add(e.Quantity)
			a.basis = a.basis.Add(e.Quantity.Mul(e.Price)).Add(e.Fees)
		case "sell":
			a := get(e.SecurityID)
			if e.Quantity.GreaterThan(a.qty) {
				oversold[e.SecurityID] = true
				continue
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

	oversoldIDs := make([]int64, 0, len(oversold))
	for id := range oversold {
		oversoldIDs = append(oversoldIDs, id)
	}
	sort.Slice(oversoldIDs, func(i, j int) bool { return oversoldIDs[i] < oversoldIDs[j] })

	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	out := make([]Holding, 0, len(order))
	for _, id := range order {
		if oversold[id] {
			continue // inconsistent position — excluded from holdings (reported via oversoldIDs)
		}
		a := bySecurity[id]
		out = append(out, Holding{
			SecurityID:   id,
			Quantity:     a.qty,
			CostBasis:    money.New(a.basis, currency),
			RealizedGain: money.New(a.realized, currency),
		})
	}
	return out, oversoldIDs
}
