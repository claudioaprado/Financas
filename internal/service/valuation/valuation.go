// Package valuation is the cross-account portfolio & Net Worth use-case (FR-10).
// It is the first service that reads ACROSS accounts and entities: it loads the
// active accounts, the whole ledger, the latest prices, the securities, the FX
// rates, and the Display Currency, then composes the existing domain derivations
// to produce per-holding valuations (native) and the Display-Currency Portfolio
// total + Net Worth.
//
// It derives everything on read (AD-2) and reads exclusively through the store
// (store-not-service, AD-1): it deliberately re-derives balances and holdings via
// domain from store rows rather than calling service/transaction, preserving the
// single-direction import rule (service→service is forbidden). It reads but never
// writes — no DB transaction is needed. Conversion is convert-then-sum with
// banker's round-once (AD-12); a missing native→Display rate yields a partial
// total and a Missing entry, never an inversion or a guess (AD-6/Q5).
package valuation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/internal/domain"
	"github.com/claudioaprado/financas/internal/money"
	"github.com/claudioaprado/financas/internal/store"
)

// ErrOversold re-exports the domain error so the HTTP layer can warn on an
// inconsistent ledger (a sell exceeding the quantity held) without importing
// domain or service/transaction directly.
var ErrOversold = domain.ErrOversold

// HoldingValuation is one active (quantity > 0) holding valued in its native
// currency (same-currency-only, so there is no FX at the per-holding level — FX
// happens only in the cross-account Net Worth aggregation). The price-dependent
// fields (Valuation, UnrealizedGain) are zero when HasPrice is false: an unpriced
// holding cannot be valued and contributes 0 to the Portfolio value (AD-6).
type HoldingValuation struct {
	AccountID      int64
	AccountName    string
	SecurityID     int64
	Symbol         string
	Name           string
	Currency       money.Currency
	Quantity       decimal.Decimal
	HasPrice       bool
	Price          money.Money // latest price (native), valid only when HasPrice
	PriceDate      time.Time   // effective date of that price (staleness)
	Valuation      money.Money // market value (qty×price), native; zero when !HasPrice
	CostBasis      money.Money
	UnrealizedGain money.Money // native; zero when !HasPrice
}

// Portfolio is the read model behind the /investments page: per-holding native
// valuations across all active investment accounts, plus the Display-Currency
// Portfolio value and Net Worth (the canonical figures from domain.NetWorth).
// RealizedByCurrency is the cumulative realized G/L grouped per native currency
// (no FX — the owner decision for 4.4). Missing lists currencies excluded from
// the totals for lack of a rate; Unpriced lists held symbols with no price.
type Portfolio struct {
	Holdings           []HoldingValuation
	PortfolioValue     money.Money      // Display Currency
	NetWorth           money.Money      // Display Currency
	RealizedByCurrency []money.Money    // cumulative realized G/L per NATIVE currency
	Missing            []money.Currency // currencies excluded from the totals (no rate)
	Unpriced           []string         // symbols of held positions with no price
	Display            money.Currency
}

// Service composes the portfolio valuation from store rows.
type Service struct {
	pool *pgxpool.Pool
}

// New returns a valuation Service backed by the given pool.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// Portfolio derives the whole-portfolio valuation: it reads the active accounts,
// the full ledger, the latest prices, the securities and the Display Currency
// through the store, re-derives each account's balance and each investment
// account's holdings via domain, values priced holdings natively, looks up the
// exact native→Display rates, and calls domain.NetWorth for the canonical
// Display-Currency Portfolio value + Net Worth.
func (s *Service) Portfolio(ctx context.Context) (Portfolio, error) {
	q := store.New(s.pool)

	displayCode, err := q.GetDisplayCurrency(ctx)
	if err != nil {
		return Portfolio{}, fmt.Errorf("valuation: display currency: %w", err)
	}
	display := money.Currency(displayCode)

	accounts, err := q.ListActiveAccounts(ctx) // non-archived only → archived excluded from Net Worth (AC2)
	if err != nil {
		return Portfolio{}, fmt.Errorf("valuation: list active accounts: %w", err)
	}

	rows, err := q.ListTransactions(ctx) // whole ledger, once
	if err != nil {
		return Portfolio{}, fmt.Errorf("valuation: list transactions: %w", err)
	}

	// Balance legs (all rows) + per-account trade events. ListTransactions is
	// occurred_on DESC, id DESC; the average-cost fold needs chronological ASC,
	// so each account's events are reversed below.
	allLegs := make([]domain.BalanceTxn, 0, len(rows))
	eventsDesc := make(map[int64][]domain.TradeEvent)
	for _, r := range rows {
		allLegs = append(allLegs, domain.BalanceTxn{
			FromAccountID: nullID(r.FromAccountID),
			FromAmount:    r.FromAmount,
			ToAccountID:   nullID(r.ToAccountID),
			ToAmount:      r.ToAmount,
		})
		if !isTrade(r.Type) {
			continue
		}
		acctID := nullID(r.FromAccountID) // buy debits the investment account
		if acctID == 0 {
			acctID = nullID(r.ToAccountID) // sell/dividend credit it
		}
		eventsDesc[acctID] = append(eventsDesc[acctID], domain.TradeEvent{
			SecurityID: nullID(r.SecurityID),
			Type:       r.Type,
			Quantity:   r.Quantity,
			Price:      r.Price,
			Fees:       r.Fees,
			CashAmount: r.ToAmount,
		})
	}

	prices, err := latestPrices(ctx, q)
	if err != nil {
		return Portfolio{}, err
	}
	meta, err := securityMeta(ctx, q)
	if err != nil {
		return Portfolio{}, err
	}

	var (
		cash        []money.Money
		liabilities []money.Money
		holdingsMV  []money.Money // priced holdings' market value (native), for Net Worth
		allRealized []money.Money
		holdings    []HoldingValuation
		unpriced    []string
	)

	for _, acct := range accounts {
		cur := money.Currency(acct.Currency)
		balance := domain.AccountBalance(acct.ID, cur, allLegs)
		switch account := acct.Type; account {
		case "credit":
			liabilities = append(liabilities, domain.AmountOwed(balance))
		default: // cash / investment balances are assets
			cash = append(cash, balance)
		}

		if acct.Type != "investment" {
			continue
		}

		events := reverseEvents(eventsDesc[acct.ID])
		derived, dErr := domain.DeriveHoldings(cur, events)
		if errors.Is(dErr, domain.ErrOversold) {
			return Portfolio{}, ErrOversold
		}
		if dErr != nil {
			return Portfolio{}, fmt.Errorf("valuation: derive holdings: %w", dErr)
		}
		for _, h := range derived {
			allRealized = append(allRealized, h.RealizedGain)
			if !h.Quantity.IsPositive() {
				continue // closed position: realized already captured above
			}
			m := meta[h.SecurityID]
			row := HoldingValuation{
				AccountID:      acct.ID,
				AccountName:    acct.Name,
				SecurityID:     h.SecurityID,
				Symbol:         m.symbol,
				Name:           m.name,
				Currency:       cur,
				Quantity:       h.Quantity,
				CostBasis:      h.CostBasis,
				Price:          money.New(decimal.Zero, cur),
				Valuation:      money.New(decimal.Zero, cur),
				UnrealizedGain: money.New(decimal.Zero, cur),
			}
			if p, ok := prices[h.SecurityID]; ok {
				market, unreal := domain.ValueHolding(h, p.Price)
				row.HasPrice = true
				row.Price = money.New(p.Price, cur).Rounded()
				row.PriceDate = p.EffectiveDate
				row.Valuation = market
				row.UnrealizedGain = unreal
				holdingsMV = append(holdingsMV, market)
			} else {
				unpriced = append(unpriced, m.symbol)
			}
			holdings = append(holdings, row)
		}
	}

	rates := s.buildRates(ctx, q, display, cash, liabilities, holdingsMV)
	v := domain.NetWorth(display, domain.ValuationInput{
		Cash:        cash,
		Liabilities: liabilities,
		Holdings:    holdingsMV,
	}, rates)

	return Portfolio{
		Holdings:           holdings,
		PortfolioValue:     v.PortfolioValue,
		NetWorth:           v.NetWorth,
		RealizedByCurrency: domain.SumByCurrency(allRealized),
		Missing:            v.Missing,
		Unpriced:           unpriced,
		Display:            display,
	}, nil
}

// buildRates looks up the exact native→Display rate (effective today) for every
// distinct native currency present in the inputs, except the Display Currency
// itself. A missing pair is left absent (domain.NetWorth then reports it in
// Missing); the rate is never inverted (AD-6).
func (s *Service) buildRates(ctx context.Context, q *store.Queries, display money.Currency, groups ...[]money.Money) map[money.Currency]decimal.Decimal {
	rates := make(map[money.Currency]decimal.Decimal)
	today := time.Now()
	for _, g := range groups {
		for _, m := range g {
			c := m.Currency()
			if c == display {
				continue
			}
			if _, done := rates[c]; done {
				continue
			}
			r, err := q.RateEffectiveAt(ctx, store.RateEffectiveAtParams{
				FromCurrency:  string(c),
				ToCurrency:    string(display),
				EffectiveDate: today,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				continue // no direct rate → Missing (never invert/guess)
			}
			if err != nil {
				continue // a transient read error also yields a partial total, never a guess
			}
			rates[c] = r
		}
	}
	return rates
}

// secMeta is a security's display fields.
type secMeta struct {
	symbol string
	name   string
}

// securityMeta builds an id->{symbol,name} map for labelling holding rows.
func securityMeta(ctx context.Context, q *store.Queries) (map[int64]secMeta, error) {
	secs, err := q.ListSecurities(ctx)
	if err != nil {
		return nil, fmt.Errorf("valuation: list securities: %w", err)
	}
	m := make(map[int64]secMeta, len(secs))
	for _, sec := range secs {
		m[sec.ID] = secMeta{symbol: sec.Symbol, name: sec.Name}
	}
	return m, nil
}

// latestPrices builds a securityID->latest-price map (effective on or before
// today). Reads via the store (store-not-service, AD-1).
func latestPrices(ctx context.Context, q *store.Queries) (map[int64]store.LatestPricesRow, error) {
	latest, err := q.LatestPrices(ctx, time.Now())
	if err != nil {
		return nil, fmt.Errorf("valuation: latest prices: %w", err)
	}
	prices := make(map[int64]store.LatestPricesRow, len(latest))
	for _, p := range latest {
		prices[p.SecurityID] = p
	}
	return prices, nil
}

// isTrade reports whether a ledger row type is an investment trade.
func isTrade(typ string) bool {
	return typ == "buy" || typ == "sell" || typ == "dividend"
}

// reverseEvents returns the events in chronological (ASC) order, as the
// average-cost fold requires (ListTransactions yields DESC).
func reverseEvents(desc []domain.TradeEvent) []domain.TradeEvent {
	asc := make([]domain.TradeEvent, len(desc))
	for i, e := range desc {
		asc[len(desc)-1-i] = e
	}
	return asc
}

// nullID unwraps a nullable account/security id to int64 (0 when NULL).
func nullID(v pgtype.Int8) int64 {
	if v.Valid {
		return v.Int64
	}
	return 0
}
