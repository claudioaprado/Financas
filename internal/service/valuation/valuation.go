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
	"sort"
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
	Cash               money.Money      // Display Currency (Σ converted cash assets)
	TotalGain          money.Money      // Display Currency (Σ converted unrealized G/L, signed)
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

// Portfolio derives the whole-portfolio valuation as of today: the current
// figures shown on /investments and the dashboard cards. It delegates to
// portfolioAsOf with the current time, so today's behaviour is unchanged.
func (s *Service) Portfolio(ctx context.Context) (Portfolio, error) {
	return s.portfolioAsOf(ctx, time.Now())
}

// portfolioAsOf derives the whole-portfolio valuation AS OF a given instant: it
// reads the active accounts, the ledger, the prices and rates effective on or
// before asOf, the securities and the Display Currency through the store,
// re-derives each account's balance and each investment account's holdings from
// the ledger restricted to occurred_on ≤ asOf, values priced holdings natively,
// looks up the exact native→Display rates effective ≤ asOf, and calls
// domain.NetWorth for the canonical Display-Currency figures. With asOf = now it
// is the current portfolio; with asOf = a prior sample date it is the
// period-change baseline (AD-11) — never recomputing history at today's rate.
func (s *Service) portfolioAsOf(ctx context.Context, asOf time.Time) (Portfolio, error) {
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

	// asOfDay is asOf normalized to its UTC calendar date — the single bound used
	// for the ledger cut AND the price/rate reads below, so all three agree even
	// when asOf is a local-zoned time.Now() near the UTC-midnight boundary (a raw
	// local time would otherwise resolve `$1::date` to the local date and skew the
	// as-of window by a day). Balance legs + per-account trade events are restricted
	// to rows occurring on or before asOfDay, compared DATE-to-DATE. ListTransactions is
	// occurred_on DESC, id DESC; the average-cost fold needs chronological ASC, so
	// each account's events are reversed below.
	asOfDay := dateOnlyUTC(asOf)
	allLegs := make([]domain.BalanceTxn, 0, len(rows))
	eventsDesc := make(map[int64][]domain.TradeEvent)
	for _, r := range rows {
		if dateOnlyUTC(r.OccurredOn).After(asOfDay) {
			continue // a future-dated leg/trade is not part of the as-of valuation
		}
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

	prices, err := latestPrices(ctx, q, asOfDay)
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
		unrealized  []money.Money // priced holdings' unrealized gain (native), for Total Gain/Loss
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
				unrealized = append(unrealized, unreal)
			} else {
				unpriced = append(unpriced, m.symbol)
			}
			holdings = append(holdings, row)
		}
	}

	rates := s.buildRates(ctx, q, display, asOfDay, cash, liabilities, holdingsMV, unrealized)
	v := domain.NetWorth(display, domain.ValuationInput{
		Cash:        cash,
		Liabilities: liabilities,
		Holdings:    holdingsMV,
		Unrealized:  unrealized,
	}, rates)

	return Portfolio{
		Holdings:           holdings,
		PortfolioValue:     v.PortfolioValue,
		NetWorth:           v.NetWorth,
		Cash:               v.Cash,
		TotalGain:          v.TotalGain,
		RealizedByCurrency: domain.SumByCurrency(allRealized),
		Missing:            v.Missing,
		Unpriced:           unpriced,
		Display:            display,
	}, nil
}

// KPI is one dashboard summary card's figure: a Display-Currency value plus its
// period-change delta against the prior-sample baseline (AD-11). Positive/Negative
// flag the value's own sign (used by the gain/loss card). DeltaUp/DeltaDown flag
// the delta's direction; HasDelta is false when no prior sample exists or the
// baseline is non-positive — the card then renders "—" (UX-DR8). All math is
// decimal (NFR-5); the handler only formats (AD-1).
type KPI struct {
	Value     money.Money
	Positive  bool
	Negative  bool
	DeltaPct  decimal.Decimal
	DeltaUp   bool
	DeltaDown bool
	HasDelta  bool
}

// Dashboard is the read model behind the KPI card row (Story 5.2, FR-11/UX-DR2):
// Net Worth, Portfolio Value, Total Gain/Loss and Cash in the Display Currency,
// each with a period-change delta vs the prior-sample-date valuation. Missing and
// Unpriced carry the same partial-total notices as Portfolio; PriorDate is the
// baseline's sample date (zero when no prior sample exists).
type Dashboard struct {
	NetWorth  KPI
	Portfolio KPI
	Cash      KPI
	GainLoss  KPI
	Display   money.Currency
	Missing   []money.Currency
	Unpriced  []string
	PriorDate time.Time
}

// Dashboard composes the KPI card row: the current valuation (as of now) for the
// four figures, plus a per-card period-change delta computed against the
// portfolio value at the prior sample date — the sample before the latest one
// the current value reflects (AD-11, see priorSampleDate). When no prior sample
// exists every delta is absent (HasDelta false) and the cards render "—".
func (s *Service) Dashboard(ctx context.Context) (Dashboard, error) {
	now := time.Now()
	cur, err := s.portfolioAsOf(ctx, now)
	if err != nil {
		return Dashboard{}, err
	}

	dash := Dashboard{
		NetWorth:  KPI{Value: cur.NetWorth},
		Portfolio: KPI{Value: cur.PortfolioValue},
		Cash:      KPI{Value: cur.Cash},
		GainLoss:  signedKPI(cur.TotalGain),
		Display:   cur.Display,
		Missing:   cur.Missing,
		Unpriced:  cur.Unpriced,
	}

	prior, ok, err := s.priorSampleDate(ctx, now)
	if err != nil {
		return Dashboard{}, err
	}
	if ok {
		base, err := s.portfolioAsOf(ctx, prior)
		if err != nil {
			return Dashboard{}, err
		}
		dash.PriorDate = prior
		setDelta(&dash.NetWorth, cur.NetWorth, base.NetWorth)
		setDelta(&dash.Portfolio, cur.PortfolioValue, base.PortfolioValue)
		setDelta(&dash.Cash, cur.Cash, base.Cash)
		setDelta(&dash.GainLoss, cur.TotalGain, base.TotalGain)
	}
	return dash, nil
}

// signedKPI builds a KPI whose value carries its own gain/loss sign (the gain/loss
// card colours and signs the figure itself, not just its delta).
func signedKPI(v money.Money) KPI {
	return KPI{Value: v, Positive: v.Amount().IsPositive(), Negative: v.Amount().IsNegative()}
}

// setDelta fills a KPI's period-change fields from the canonical domain figure,
// leaving HasDelta false (→ "—") when the change is undefined (no prior sample
// value, or a non-positive baseline).
func setDelta(k *KPI, now, base money.Money) {
	pct, ok := domain.PercentChange(now, base)
	if !ok {
		return
	}
	k.DeltaPct = pct
	k.DeltaUp = pct.IsPositive()
	k.DeltaDown = pct.IsNegative()
	k.HasDelta = true
}

// sampleDates returns the distinct Price/ExchangeRate effective dates on or
// before today (now's date), UTC-date-normalized and sorted ASCENDING. These are
// the "sample" dates the value-over-time series and the period-change baseline
// are derived from (AD-11): owner-entered history is small, so it scans in Go —
// no snapshot table and no new query. Future-effective samples are excluded
// (they are not part of any value as of today).
func (s *Service) sampleDates(ctx context.Context, now time.Time) ([]time.Time, error) {
	q := store.New(s.pool)
	today := dateOnlyUTC(now)

	seen := make(map[time.Time]bool)
	add := func(eff time.Time) {
		d := dateOnlyUTC(eff)
		if !d.After(today) { // ignore future-effective samples
			seen[d] = true
		}
	}

	prices, err := q.ListPrices(ctx)
	if err != nil {
		return nil, fmt.Errorf("valuation: list prices: %w", err)
	}
	for _, p := range prices {
		add(p.EffectiveDate)
	}
	rates, err := q.ListExchangeRates(ctx)
	if err != nil {
		return nil, fmt.Errorf("valuation: list exchange rates: %w", err)
	}
	for _, r := range rates {
		add(r.EffectiveDate)
	}

	dates := make([]time.Time, 0, len(seen))
	for d := range seen {
		dates = append(dates, d)
	}
	sort.Slice(dates, func(i, j int) bool { return dates[i].Before(dates[j]) }) // ascending
	return dates, nil
}

// priorSampleDate returns the period-change baseline date (AD-11): the
// SECOND-most-recent distinct sample date on or before today (see sampleDates).
// The most recent sample is the one the CURRENT valuation reflects, so the
// baseline is the one before it. ok is false when fewer than two distinct sample
// dates exist (the day-one "—" state): a single sample has nothing prior to
// compare against.
func (s *Service) priorSampleDate(ctx context.Context, now time.Time) (time.Time, bool, error) {
	dates, err := s.sampleDates(ctx, now)
	if err != nil {
		return time.Time{}, false, err
	}
	if len(dates) < 2 {
		return time.Time{}, false, nil
	}
	return dates[len(dates)-2], true, nil // ascending → penultimate is second-most-recent
}

// SeriesPoint is one plotted Net Worth value at a historical date, valued AS OF
// that date — using the prices/rates effective on or before it (never today's
// rate, AD-11). Partial is set when a held currency had no rate at that date, so
// the Net Worth there is a partial total (the unrated currency excluded, AD-6).
type SeriesPoint struct {
	Date    time.Time
	Value   money.Money // Display Currency Net Worth as of Date
	Partial bool
}

// ValueSeries returns the Display-Currency Net Worth series for the window
// [from, today] (FR-12, AD-11): sampled at each Price/ExchangeRate effective date
// in the window, plus the window boundary `from` and today, each valued AS OF its
// own date via portfolioAsOf (then-current prices/rates — never retroactively
// repriced at today's rate). A zero `from` means "all history" (start at the
// earliest sample). Points are de-duplicated by date and sorted ascending. With
// no history the series may have a single point (today); callers decide a line
// needs ≥2. ErrOversold is propagated like Portfolio.
func (s *Service) ValueSeries(ctx context.Context, from time.Time) ([]SeriesPoint, error) {
	now := time.Now()
	today := dateOnlyUTC(now)

	samples, err := s.sampleDates(ctx, now)
	if err != nil {
		return nil, err
	}

	set := make(map[time.Time]bool)
	if from.IsZero() {
		for _, d := range samples {
			set[d] = true
		}
	} else {
		fromDay := dateOnlyUTC(from)
		// Add a boundary point at the window start ONLY when it falls within history
		// (a sample exists on or before it) — so the line starts at the correct
		// as-of value. If the window predates all history, skip it: a boundary there
		// would value to zero and draw a long flat-zero lead-in; let the line start
		// at the earliest in-window sample instead.
		hasEarlier := len(samples) > 0 && !samples[0].After(fromDay)
		if hasEarlier && !fromDay.After(today) {
			set[fromDay] = true
		}
		for _, d := range samples {
			if !d.Before(fromDay) { // d >= fromDay (samples are already ≤ today)
				set[d] = true
			}
		}
	}
	set[today] = true // always end at the current value

	dates := make([]time.Time, 0, len(set))
	for d := range set {
		dates = append(dates, d)
	}
	sort.Slice(dates, func(i, j int) bool { return dates[i].Before(dates[j]) })

	points := make([]SeriesPoint, 0, len(dates))
	for _, d := range dates {
		p, err := s.portfolioAsOf(ctx, d)
		if err != nil {
			return nil, err // ErrOversold and read failures bubble up
		}
		points = append(points, SeriesPoint{
			Date:    d,
			Value:   p.NetWorth,
			Partial: len(p.Missing) > 0,
		})
	}
	return points, nil
}

// allocTopN caps the number of named allocation slices for legibility; the
// smaller tail is folded into a single "Other" slice (Story 5.4, D5).
const allocTopN = 8

// AllocationGroup is one allocation slice formatted for the read model: the
// grouping key (security symbol or account name), the reconciled integer
// percent, and the Display-Currency value (round-once).
type AllocationGroup struct {
	Key     string
	Percent int
	Value   money.Money // Display Currency
}

// Allocation is the invested-value breakdown (Story 5.4, FR-12/UX-DR4): the
// current portfolio's PRICED holdings grouped by Security or Account, converted
// to the Display Currency and reconciled to exactly 100% (AD-12). Total is the
// canonical Portfolio Value (== Portfolio().PortfolioValue). Missing lists the
// holding currencies excluded for lack of a rate (partial total, AD-6); By is
// the active dimension ("security" | "account"). There is deliberately no
// Unpriced field: held symbols with no price are surfaced by the dashboard's
// own UnpricedSymbols notice (from Dashboard()), so the allocation card need not
// repeat them.
type Allocation struct {
	Groups  []AllocationGroup
	Total   money.Money
	Display money.Currency
	By      string
	Missing []money.Currency
}

// AllocBy normalizes the breakdown-dimension query value to a supported key,
// defaulting to "security".
func AllocBy(by string) string {
	if by == "account" {
		return "account"
	}
	return "security"
}

// Allocation derives the invested-value breakdown as of now for the given
// dimension ("security" default, or "account"). It reuses the portfolio
// valuation (priced holdings, native market value) and the SAME native→Display
// rates Net Worth used (effective today), then delegates the grouping +
// 100%-reconciliation to the canonical domain.Allocate (AD-10). Because the rates
// and the convert-then-sum/round-once policy match Net Worth's, Total equals
// Portfolio().PortfolioValue (D4). ErrOversold propagates via Portfolio.
func (s *Service) Allocation(ctx context.Context, by string) (Allocation, error) {
	by = AllocBy(by)
	p, err := s.Portfolio(ctx)
	if err != nil {
		return Allocation{}, err // ErrOversold and read failures bubble up
	}

	items := make([]domain.AllocItem, 0, len(p.Holdings))
	natives := make([]money.Money, 0, len(p.Holdings))
	for _, h := range p.Holdings {
		if !h.HasPrice {
			continue // unpriced holdings are not invested value (consistent with PortfolioValue)
		}
		key := h.Symbol
		if by == "account" {
			key = h.AccountName
		}
		items = append(items, domain.AllocItem{Key: key, Value: h.Valuation})
		natives = append(natives, h.Valuation)
	}

	// The same effective-today rates Net Worth used → Total reconciles to
	// PortfolioValue. A second buildRates pass is a negligible owner-scale,
	// read-only cost (mirrors Dashboard/ValueSeries re-calling portfolioAsOf).
	rates := s.buildRates(ctx, store.New(s.pool), p.Display, dateOnlyUTC(time.Now()), natives)
	alloc := domain.Allocate(p.Display, items, rates, allocTopN)

	groups := make([]AllocationGroup, len(alloc.Slices))
	for i, sl := range alloc.Slices {
		groups[i] = AllocationGroup{Key: sl.Key, Percent: sl.Percent, Value: sl.Value}
	}
	return Allocation{
		Groups:  groups,
		Total:   alloc.Total,
		Display: p.Display,
		By:      by,
		Missing: alloc.Missing, // holdings-specific partial set (the allocation's own exclusions)
	}, nil
}

// Insight is the dashboard's single derived call-out (Story 5.5, UX-DR6): the
// percentage change of Net Worth from the first of the current month to now, in
// the Display Currency. Pct is the canonical domain.PercentChange figure (signed,
// 1 dp). HasData is false when no comparable month-start baseline exists (a
// non-positive baseline / currency mismatch) — the card then shows a calm
// fallback. Partial is set when either valuation excluded a currency for lack of
// a rate, so the figure is a partial total (AD-6/AD-12).
type Insight struct {
	Pct      decimal.Decimal // signed % change this month (1 dp)
	Up       bool
	Down     bool
	HasData  bool
	Partial  bool
	NetWorth money.Money // current Net Worth (Display Currency)
	Display  money.Currency
}

// Insight derives the month-over-month Net Worth change (Story 5.5): the
// portfolio valued AS OF now vs AS OF the first of the current month (UTC),
// reconciled by the canonical domain.PercentChange (AD-10) — the same %-change
// home the KPI deltas use. It reuses the portfolioAsOf as-of seam (AD-11, no
// snapshot table). ErrOversold propagates like Portfolio/Dashboard.
func (s *Service) Insight(ctx context.Context) (Insight, error) {
	now := time.Now()
	cur, err := s.portfolioAsOf(ctx, now)
	if err != nil {
		return Insight{}, err
	}
	base, err := s.portfolioAsOf(ctx, firstOfMonthUTC(now))
	if err != nil {
		return Insight{}, err
	}
	pct, ok := domain.PercentChange(cur.NetWorth, base.NetWorth)
	return Insight{
		Pct:      pct,
		Up:       ok && pct.IsPositive(),
		Down:     ok && pct.IsNegative(),
		HasData:  ok,
		Partial:  len(cur.Missing) > 0 || len(base.Missing) > 0,
		NetWorth: cur.NetWorth,
		Display:  cur.Display,
	}, nil
}

// firstOfMonthUTC returns the first day of t's month at UTC midnight — the
// month-start baseline for the Insight, consistent with the as-of UTC date
// convention used across the valuation service (the DB session is UTC, AD-8).
func firstOfMonthUTC(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// dateOnlyUTC normalizes a timestamp to UTC midnight so price/rate effective
// dates (stored as DATE → UTC midnight) and "today" compare by calendar date.
func dateOnlyUTC(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// buildRates looks up the exact native→Display rate (effective ≤ asOf) for every
// distinct native currency present in the inputs, except the Display Currency
// itself. A missing pair is left absent (domain.NetWorth then reports it in
// Missing); the rate is never inverted (AD-6).
func (s *Service) buildRates(ctx context.Context, q *store.Queries, display money.Currency, asOf time.Time, groups ...[]money.Money) map[money.Currency]decimal.Decimal {
	rates := make(map[money.Currency]decimal.Decimal)
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
				EffectiveDate: asOf,
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
// asOf). Reads via the store (store-not-service, AD-1).
func latestPrices(ctx context.Context, q *store.Queries, asOf time.Time) (map[int64]store.LatestPricesRow, error) {
	latest, err := q.LatestPrices(ctx, asOf)
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
