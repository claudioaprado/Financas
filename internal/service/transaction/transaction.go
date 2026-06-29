// Package transaction is the use-case for recording cash income and expenses
// (FR-6). Transactions are the single source of truth (AD-2); the account
// balance is never stored — it is derived on read by domain.AccountBalance
// (AD-10). Each write goes through one DB transaction (AD-3). Amounts are
// non-negative magnitudes (AD-4); income credits an account, expense debits it,
// via the one-row from/to ledger shape (AD-9).
//
// Income/expense apply to cash and credit accounts (an expense on credit
// increases the balance owed; income/refund reduces it). Transfers (3.3),
// categories (3.4), and investment cash flows (Epic 4) extend this; investment
// accounts reject plain income/expense.
package transaction

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

// TxType is the kind of transaction. Story 3.1 supports income and expense.
type TxType string

const (
	// Income credits the account.
	Income TxType = "income"
	// Expense debits the account.
	Expense TxType = "expense"
	// Transfer moves value between two of the owner's accounts (one row, AD-9).
	Transfer TxType = "transfer"
	// Buy debits the investment account's cash and adds to a holding (Epic 4).
	Buy TxType = "buy"
	// Sell credits cash, reduces basis proportionally, realizes gain (Epic 4).
	Sell TxType = "sell"
	// Dividend credits cash; quantity and basis are unchanged (Epic 4).
	Dividend TxType = "dividend"
)

// IsValid reports whether t is an income or expense — the single-account types
// the Record/Edit path handles. Transfers go through the Transfer use-case and
// are deliberately excluded here (so Record/Edit reject them).
func (t TxType) IsValid() bool { return t == Income || t == Expense }

// Input/lookup errors. The service is the validation authority; DB constraints
// are the backstop.
var (
	ErrAccountNotFound = errors.New("transaction: account not found")
	// ErrUnsupportedAccountType is returned when income/expense are recorded on
	// an account type that does not take them (investment cash flow is Epic 4).
	ErrUnsupportedAccountType = errors.New("transaction: income/expense require a cash or credit account")
	ErrInvalidType            = errors.New("transaction: type must be income or expense")
	ErrNonPositiveAmount      = errors.New("transaction: amount must be positive")
	ErrTxNotFound             = errors.New("transaction: not found")
	// Transfer-specific errors.
	ErrSameAccount                   = errors.New("transaction: transfer source and destination must differ")
	ErrSameCurrencyAmountMismatch    = errors.New("transaction: same-currency transfer must have equal amounts")
	ErrCrossCurrencyToAmountRequired = errors.New("transaction: cross-currency transfer needs a destination amount")
	// Category errors (Story 3.4).
	ErrCategoryNotFound     = errors.New("transaction: category not found")
	ErrCategoryKindMismatch = errors.New("transaction: category kind must match the transaction type")
	// Investment-trade errors (Story 4.2).
	ErrNotInvestmentAccount  = errors.New("transaction: buy/sell/dividend require an investment account")
	ErrSecurityNotFound      = errors.New("transaction: security not found")
	ErrTradeCurrencyMismatch = errors.New("transaction: security quote currency must equal the account currency")
	ErrNonPositiveQuantity   = errors.New("transaction: quantity must be positive")
	ErrNonPositivePrice      = errors.New("transaction: price must be positive")
	ErrNegativeFees          = errors.New("transaction: fees must not be negative")
	ErrNegativeProceeds      = errors.New("transaction: fees exceed gross proceeds")
	// ErrOversold means a sell exceeds the quantity held (domain.ErrOversold).
	ErrOversold = errors.New("transaction: sell exceeds holdings")
)

// Transaction is one row formatted for a specific account's register. Amount is
// the non-negative magnitude from that account's perspective; Incoming is true
// when the row credits the account (income, or a transfer in). Counterparty is
// the other account's name for transfers (empty for income/expense). The sign is
// presentation derived from Incoming.
type Transaction struct {
	ID           int64
	Type         TxType
	AccountID    int64
	Amount       decimal.Decimal
	Incoming     bool
	Counterparty string
	CategoryID   int64  // 0 when uncategorized
	CategoryName string // resolved for display
	SecurityID   int64  // 0 when not a trade
	Security     string // resolved symbol for trade rows
	Quantity     decimal.Decimal
	Price        decimal.Decimal
	Date         time.Time
	Description  string
	CreatedAt    time.Time
}

// Service records, edits, deletes, lists, and derives balances for cash
// transactions.
type Service struct {
	pool *pgxpool.Pool
}

// New returns a transaction Service backed by the given pool.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// Record validates and appends an income or expense on a cash account, returning
// the stored row. It writes inside one transaction (AD-3).
func (s *Service) Record(ctx context.Context, accountID int64, typ TxType, amount decimal.Decimal, date time.Time, description string, categoryID int64) (Transaction, error) {
	if err := s.validate(ctx, accountID, typ, amount); err != nil {
		return Transaction{}, err
	}
	catID, err := s.resolveCategory(ctx, categoryID, typ)
	if err != nil {
		return Transaction{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Transaction{}, fmt.Errorf("transaction: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	from, to, fromAmt, toAmt := legs(accountID, typ, amount)
	row, err := store.New(tx).CreateTransaction(ctx, store.CreateTransactionParams{
		Type:          string(typ),
		FromAccountID: from,
		ToAccountID:   to,
		FromAmount:    fromAmt,
		ToAmount:      toAmt,
		OccurredOn:    date,
		Description:   description,
		CategoryID:    catID,
		SecurityID:    pgtype.Int8{},
		Quantity:      decimal.Zero,
		Price:         decimal.Zero,
		Fees:          decimal.Zero,
	})
	if err != nil {
		return Transaction{}, fmt.Errorf("transaction: insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Transaction{}, fmt.Errorf("transaction: commit: %w", err)
	}
	return toTransaction(accountID, row, nil, nil, nil), nil // income/expense: no counterpart
}

// Edit updates an existing income/expense on the given cash account. The from/to
// placement is recomputed from the new type. A missing id returns ErrTxNotFound.
func (s *Service) Edit(ctx context.Context, accountID, txID int64, typ TxType, amount decimal.Decimal, date time.Time, description string, categoryID int64) error {
	if err := s.validate(ctx, accountID, typ, amount); err != nil {
		return err
	}
	catID, err := s.resolveCategory(ctx, categoryID, typ)
	if err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("transaction: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	from, to, fromAmt, toAmt := legs(accountID, typ, amount)
	n, err := store.New(tx).UpdateTransaction(ctx, store.UpdateTransactionParams{
		ID:            txID,
		Type:          string(typ),
		FromAccountID: from,
		ToAccountID:   to,
		FromAmount:    fromAmt,
		ToAmount:      toAmt,
		OccurredOn:    date,
		Description:   description,
		CategoryID:    catID,
	})
	if err != nil {
		return fmt.Errorf("transaction: update: %w", err)
	}
	if n == 0 {
		return ErrTxNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("transaction: commit: %w", err)
	}
	return nil
}

// Delete removes a transaction. The balance re-derives on the next read. A
// missing id returns ErrTxNotFound.
func (s *Service) Delete(ctx context.Context, txID int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("transaction: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	n, err := store.New(tx).DeleteTransaction(ctx, txID)
	if err != nil {
		return fmt.Errorf("transaction: delete: %w", err)
	}
	if n == 0 {
		return ErrTxNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("transaction: commit: %w", err)
	}
	return nil
}

// Transfer moves value from one account to another as a single ledger row
// (AD-9): it debits from_account and credits to_account. Same-currency transfers
// have equal legs; cross-currency transfers record both legs (the rate is not
// stored). It writes inside one transaction (AD-3).
func (s *Service) Transfer(ctx context.Context, fromID, toID int64, fromAmount, toAmount decimal.Decimal, date time.Time, description string) error {
	if fromID == toID {
		return ErrSameAccount
	}
	if !fromAmount.IsPositive() {
		return ErrNonPositiveAmount
	}

	q := store.New(s.pool)
	fromAcct, err := q.GetAccount(ctx, fromID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAccountNotFound
	}
	if err != nil {
		return fmt.Errorf("transaction: get from account: %w", err)
	}
	toAcct, err := q.GetAccount(ctx, toID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAccountNotFound
	}
	if err != nil {
		return fmt.Errorf("transaction: get to account: %w", err)
	}

	// Resolve the destination leg by currency (AD-9).
	finalTo := toAmount
	if fromAcct.Currency == toAcct.Currency {
		if toAmount.IsPositive() && !toAmount.Equal(fromAmount) {
			return ErrSameCurrencyAmountMismatch
		}
		finalTo = fromAmount
	} else if !toAmount.IsPositive() {
		return ErrCrossCurrencyToAmountRequired
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("transaction: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := store.New(tx).CreateTransaction(ctx, store.CreateTransactionParams{
		Type:          string(Transfer),
		FromAccountID: idParam(fromID),
		ToAccountID:   idParam(toID),
		FromAmount:    fromAmount,
		ToAmount:      finalTo,
		OccurredOn:    date,
		Description:   description,
		CategoryID:    pgtype.Int8{}, // transfers are never categorized
		SecurityID:    pgtype.Int8{},
		Quantity:      decimal.Zero,
		Price:         decimal.Zero,
		Fees:          decimal.Zero,
	}); err != nil {
		return fmt.Errorf("transaction: insert transfer: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("transaction: commit: %w", err)
	}
	return nil
}

// validateTrade loads the account and security for a buy/sell/dividend and
// enforces the invariants shared by all three: the account must be an investment
// account, the security must exist, and (same-currency-only, Epic 4 decision) the
// security's quote currency must equal the account's currency. Securities are
// read via store (not service/security) per the store-not-service rule (AD-1).
func (s *Service) validateTrade(ctx context.Context, accountID, securityID int64) (store.Account, store.Security, error) {
	q := store.New(s.pool)
	acct, err := q.GetAccount(ctx, accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Account{}, store.Security{}, ErrAccountNotFound
	}
	if err != nil {
		return store.Account{}, store.Security{}, fmt.Errorf("transaction: get account: %w", err)
	}
	if acct.Type != "investment" {
		return store.Account{}, store.Security{}, ErrNotInvestmentAccount
	}
	sec, err := q.GetSecurity(ctx, securityID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Account{}, store.Security{}, ErrSecurityNotFound
	}
	if err != nil {
		return store.Account{}, store.Security{}, fmt.Errorf("transaction: get security: %w", err)
	}
	if sec.QuoteCurrency != acct.Currency {
		return store.Account{}, store.Security{}, ErrTradeCurrencyMismatch
	}
	return acct, sec, nil
}

// Buy records a purchase: it debits the account's cash by quantity×price + fees
// (one row, from-leg) and grows the holding's quantity and cost basis by the same
// (derived on read). It writes inside one transaction (AD-3).
func (s *Service) Buy(ctx context.Context, accountID, securityID int64, quantity, price, fees decimal.Decimal, date time.Time, description string) (Transaction, error) {
	if _, _, err := s.validateTrade(ctx, accountID, securityID); err != nil {
		return Transaction{}, err
	}
	if !quantity.IsPositive() {
		return Transaction{}, ErrNonPositiveQuantity
	}
	if !price.IsPositive() {
		return Transaction{}, ErrNonPositivePrice
	}
	if fees.IsNegative() {
		return Transaction{}, ErrNegativeFees
	}
	cost := quantity.Mul(price).Add(fees)
	row, err := s.insertTrade(ctx, store.CreateTransactionParams{
		Type:          string(Buy),
		FromAccountID: idParam(accountID),
		FromAmount:    cost,
		ToAmount:      decimal.Zero,
		OccurredOn:    date,
		Description:   description,
		SecurityID:    idParam(securityID),
		Quantity:      quantity,
		Price:         price,
		Fees:          fees,
	})
	if err != nil {
		return Transaction{}, err
	}
	return toTransaction(accountID, row, nil, nil, nil), nil
}

// Sell records a sale: it credits the account's cash by quantity×price − fees
// (fees reduce proceeds, not basis — Epic 4 decision), reduces the holding's
// basis proportionally, and realizes gain (all derived on read). It rejects an
// oversell (selling more than currently held). One transaction (AD-3).
func (s *Service) Sell(ctx context.Context, accountID, securityID int64, quantity, price, fees decimal.Decimal, date time.Time, description string) (Transaction, error) {
	if _, _, err := s.validateTrade(ctx, accountID, securityID); err != nil {
		return Transaction{}, err
	}
	if !quantity.IsPositive() {
		return Transaction{}, ErrNonPositiveQuantity
	}
	if !price.IsPositive() {
		return Transaction{}, ErrNonPositivePrice
	}
	if fees.IsNegative() {
		return Transaction{}, ErrNegativeFees
	}
	proceeds := quantity.Mul(price).Sub(fees)
	if proceeds.IsNegative() {
		return Transaction{}, ErrNegativeProceeds
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Transaction{}, fmt.Errorf("transaction: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	row, err := q.CreateTransaction(ctx, store.CreateTransactionParams{
		Type:        string(Sell),
		ToAccountID: idParam(accountID),
		FromAmount:  decimal.Zero,
		ToAmount:    proceeds,
		OccurredOn:  date,
		Description: description,
		CategoryID:  pgtype.Int8{},
		SecurityID:  idParam(securityID),
		Quantity:    quantity,
		Price:       price,
		Fees:        fees,
	})
	if err != nil {
		return Transaction{}, fmt.Errorf("transaction: insert sell: %w", err)
	}
	// Oversell guard: re-derive the resulting ledger ON THIS SAME TX. If inserting
	// this sell makes any position go negative at any chronological point — an
	// oversell, a back-dated sell, or a same-date sell recorded before its buy —
	// DeriveHoldings returns ErrOversold and the deferred Rollback undoes the
	// insert. This makes the guard identical to the read derivation and atomic
	// (no TOCTOU; exact NUMERIC(28,10) compare, no epsilon — Epic 4 decision).
	if _, _, derr := s.deriveHoldings(ctx, q, accountID); derr != nil {
		return Transaction{}, derr
	}
	if err := tx.Commit(ctx); err != nil {
		return Transaction{}, fmt.Errorf("transaction: commit: %w", err)
	}
	return toTransaction(accountID, row, nil, nil, nil), nil
}

// Dividend records a cash dividend: it credits the account's cash by the entered
// amount and leaves the holding's quantity and basis unchanged. One tx (AD-3).
func (s *Service) Dividend(ctx context.Context, accountID, securityID int64, amount decimal.Decimal, date time.Time, description string) (Transaction, error) {
	if _, _, err := s.validateTrade(ctx, accountID, securityID); err != nil {
		return Transaction{}, err
	}
	if !amount.IsPositive() {
		return Transaction{}, ErrNonPositiveAmount
	}
	row, err := s.insertTrade(ctx, store.CreateTransactionParams{
		Type:        string(Dividend),
		ToAccountID: idParam(accountID),
		FromAmount:  decimal.Zero,
		ToAmount:    amount,
		OccurredOn:  date,
		Description: description,
		SecurityID:  idParam(securityID),
		Quantity:    decimal.Zero,
		Price:       decimal.Zero,
		Fees:        decimal.Zero,
	})
	if err != nil {
		return Transaction{}, err
	}
	return toTransaction(accountID, row, nil, nil, nil), nil
}

// insertTrade writes one investment-transaction row in its own DB transaction.
func (s *Service) insertTrade(ctx context.Context, params store.CreateTransactionParams) (store.Transaction, error) {
	params.CategoryID = pgtype.Int8{} // trades are never categorized
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return store.Transaction{}, fmt.Errorf("transaction: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := store.New(tx).CreateTransaction(ctx, params)
	if err != nil {
		return store.Transaction{}, fmt.Errorf("transaction: insert trade: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Transaction{}, fmt.Errorf("transaction: commit: %w", err)
	}
	return row, nil
}

// HoldingView is one derived position formatted for display: quantity, average
// cost (basis ÷ quantity), cost basis, cumulative realized gain, and — when a
// Price exists (Story 4.3) — the market value, unrealized gain, the price used,
// and that price's effective date (for staleness). All money is in the account's
// native currency; same-currency-only means no FX here (Display-Currency
// aggregation is Story 4.4). HasPrice is false when no price exists for the
// security on or before today; callers render "—" for the price-dependent fields.
type HoldingView struct {
	SecurityID     int64
	Symbol         string
	Name           string
	Quantity       decimal.Decimal
	AvgCost        money.Money
	CostBasis      money.Money
	RealizedGain   money.Money
	HasPrice       bool
	Price          money.Money // latest price (native), valid only when HasPrice
	PriceDate      time.Time   // effective date of that price (staleness)
	MarketValue    money.Money // quantity × price (native), valid only when HasPrice
	UnrealizedGain money.Money // market value − cost basis, valid only when HasPrice
}

// Holdings derives the account's active holdings (quantity > 0) plus the
// cumulative realized Gain/Loss across all positions (including closed ones), in
// the account's native currency (AD-2/AD-10). It surfaces domain.ErrOversold when
// the ledger is inconsistent (e.g. a buy was deleted under a later sell).
func (s *Service) Holdings(ctx context.Context, accountID int64) ([]HoldingView, money.Money, error) {
	q := store.New(s.pool)
	acct, holdings, err := s.deriveHoldings(ctx, q, accountID)
	if err != nil {
		return nil, money.Money{}, err
	}
	cur := money.Currency(acct.Currency)
	meta, err := securityMeta(ctx, q)
	if err != nil {
		return nil, money.Money{}, err
	}
	// Latest price (effective <= today) per security, for market value /
	// unrealized gain (Story 4.3). Read directly from the store (store-not-service,
	// AD-1) — never service/price. Same-currency-only means the price is already in
	// the holding's currency, so there is no FX here (Display-Currency aggregation
	// is Story 4.4).
	latest, err := q.LatestPrices(ctx, time.Now())
	if err != nil {
		return nil, money.Money{}, fmt.Errorf("transaction: latest prices: %w", err)
	}
	prices := make(map[int64]store.LatestPricesRow, len(latest))
	for _, p := range latest {
		prices[p.SecurityID] = p
	}
	realized := decimal.Zero
	views := make([]HoldingView, 0, len(holdings))
	for _, h := range holdings {
		realized = realized.Add(h.RealizedGain.Amount())
		if !h.Quantity.IsPositive() {
			continue // closed position: hidden from the active list (AC#4)
		}
		m := meta[h.SecurityID]
		view := HoldingView{
			SecurityID:   h.SecurityID,
			Symbol:       m.symbol,
			Name:         m.name,
			Quantity:     h.Quantity,
			AvgCost:      money.New(h.CostBasis.Amount().Div(h.Quantity), cur).Rounded(),
			CostBasis:    h.CostBasis,
			RealizedGain: h.RealizedGain,
		}
		if p, ok := prices[h.SecurityID]; ok {
			market, unrealized := domain.ValueHolding(h, p.Price)
			view.HasPrice = true
			view.Price = money.New(p.Price, cur).Rounded()
			view.PriceDate = p.EffectiveDate
			view.MarketValue = market
			view.UnrealizedGain = unrealized
		}
		views = append(views, view)
	}
	return views, money.New(realized, cur), nil
}

// deriveHoldings loads the account and folds its investment ledger rows
// (chronological) into the canonical domain holdings. It is the single read path
// behind both Holdings and the Sell oversell guard. The caller passes the queries
// handle so the Sell guard can re-derive on the SAME transaction as its insert
// (making the guard identical to the read derivation, and atomic).
func (s *Service) deriveHoldings(ctx context.Context, q *store.Queries, accountID int64) (store.Account, []domain.Holding, error) {
	acct, err := q.GetAccount(ctx, accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Account{}, nil, ErrAccountNotFound
	}
	if err != nil {
		return store.Account{}, nil, fmt.Errorf("transaction: get account: %w", err)
	}
	rows, err := q.ListAccountTransactions(ctx, idParam(accountID))
	if err != nil {
		return store.Account{}, nil, fmt.Errorf("transaction: list: %w", err)
	}
	// ListAccountTransactions is occurred_on DESC, id DESC; reverse to get the
	// chronological (ASC) order the average-cost fold requires.
	events := make([]domain.TradeEvent, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		typ := TxType(r.Type)
		if typ != Buy && typ != Sell && typ != Dividend {
			continue
		}
		events = append(events, domain.TradeEvent{
			SecurityID: nullID(r.SecurityID),
			Type:       r.Type,
			Quantity:   r.Quantity,
			Price:      r.Price,
			Fees:       r.Fees,
			CashAmount: r.ToAmount,
		})
	}
	holdings, err := domain.DeriveHoldings(money.Currency(acct.Currency), events)
	if errors.Is(err, domain.ErrOversold) {
		return store.Account{}, nil, ErrOversold
	}
	if err != nil {
		return store.Account{}, nil, fmt.Errorf("transaction: derive holdings: %w", err)
	}
	return acct, holdings, nil
}

// secMeta is a security's display fields.
type secMeta struct {
	symbol string
	name   string
}

// securityMeta builds an id->{symbol,name} map for resolving trade-row labels.
func securityMeta(ctx context.Context, q *store.Queries) (map[int64]secMeta, error) {
	secs, err := q.ListSecurities(ctx)
	if err != nil {
		return nil, fmt.Errorf("transaction: list securities: %w", err)
	}
	m := make(map[int64]secMeta, len(secs))
	for _, sec := range secs {
		m[sec.ID] = secMeta{symbol: sec.Symbol, name: sec.Name}
	}
	return m, nil
}

// Balance derives the account's current balance from the ledger (AD-2, AD-10).
func (s *Service) Balance(ctx context.Context, accountID int64) (money.Money, error) {
	q := store.New(s.pool)
	acct, err := q.GetAccount(ctx, accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return money.Money{}, ErrAccountNotFound
	}
	if err != nil {
		return money.Money{}, fmt.Errorf("transaction: get account: %w", err)
	}
	rows, err := q.ListAccountTransactions(ctx, idParam(accountID))
	if err != nil {
		return money.Money{}, fmt.Errorf("transaction: list: %w", err)
	}
	legs := make([]domain.BalanceTxn, len(rows))
	for i, r := range rows {
		legs[i] = domain.BalanceTxn{
			FromAccountID: nullID(r.FromAccountID),
			FromAmount:    r.FromAmount,
			ToAccountID:   nullID(r.ToAccountID),
			ToAmount:      r.ToAmount,
		}
	}
	return domain.AccountBalance(accountID, money.Currency(acct.Currency), legs), nil
}

// List returns the account's transactions, newest-first, formatted relative to
// that account (a transfer shows as a debit on the source / credit on the
// destination, with the counterpart account named).
func (s *Service) List(ctx context.Context, accountID int64) ([]Transaction, error) {
	q := store.New(s.pool)
	rows, err := q.ListAccountTransactions(ctx, idParam(accountID))
	if err != nil {
		return nil, fmt.Errorf("transaction: list: %w", err)
	}
	names, err := accountNames(ctx, q)
	if err != nil {
		return nil, err
	}
	catNames, err := categoryNames(ctx, q)
	if err != nil {
		return nil, err
	}
	secNames, err := securitySymbols(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make([]Transaction, len(rows))
	for i, r := range rows {
		out[i] = toTransaction(accountID, r, names, catNames, secNames)
	}
	return out, nil
}

// securitySymbols builds an id->symbol map for resolving a trade row's label.
func securitySymbols(ctx context.Context, q *store.Queries) (map[int64]string, error) {
	secs, err := q.ListSecurities(ctx)
	if err != nil {
		return nil, fmt.Errorf("transaction: list securities: %w", err)
	}
	names := make(map[int64]string, len(secs))
	for _, sec := range secs {
		names[sec.ID] = sec.Symbol
	}
	return names, nil
}

// accountNames builds an id->name map over all accounts (incl. archived) so
// transfer counterparts always resolve.
func accountNames(ctx context.Context, q *store.Queries) (map[int64]string, error) {
	accts, err := q.ListAllAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("transaction: list accounts: %w", err)
	}
	names := make(map[int64]string, len(accts))
	for _, a := range accts {
		names[a.ID] = a.Name
	}
	return names, nil
}

// categoryNames builds an id->name map for resolving a row's category label.
func categoryNames(ctx context.Context, q *store.Queries) (map[int64]string, error) {
	cats, err := q.ListCategories(ctx)
	if err != nil {
		return nil, fmt.Errorf("transaction: list categories: %w", err)
	}
	names := make(map[int64]string, len(cats))
	for _, c := range cats {
		names[c.ID] = c.Name
	}
	return names, nil
}

// resolveCategory validates an optional category assignment (0 = none): the
// category must exist and its kind must match the transaction type. Returns the
// nullable id to store.
func (s *Service) resolveCategory(ctx context.Context, categoryID int64, typ TxType) (pgtype.Int8, error) {
	if categoryID == 0 {
		return pgtype.Int8{}, nil
	}
	cat, err := store.New(s.pool).GetCategory(ctx, categoryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return pgtype.Int8{}, ErrCategoryNotFound
	}
	if err != nil {
		return pgtype.Int8{}, fmt.Errorf("transaction: get category: %w", err)
	}
	if cat.Kind != string(typ) {
		return pgtype.Int8{}, ErrCategoryKindMismatch
	}
	return idParam(categoryID), nil
}

// CategoryTxn is one of a category's transactions, with its account and amount
// (in that account's native currency) for the category summary.
type CategoryTxn struct {
	ID          int64
	AccountID   int64
	AccountName string
	Date        time.Time
	Description string
	Amount      money.Money
}

// CategoryTransactions returns the transactions assigned to a category and their
// per-currency totals (no conversion — AD-12/Display-Currency totals are Epic 5).
func (s *Service) CategoryTransactions(ctx context.Context, categoryID int64) ([]CategoryTxn, []money.Money, error) {
	q := store.New(s.pool)
	rows, err := q.ListCategoryTransactions(ctx, idParam(categoryID))
	if err != nil {
		return nil, nil, fmt.Errorf("transaction: list category: %w", err)
	}
	accts, err := q.ListAllAccounts(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("transaction: list accounts: %w", err)
	}
	name := make(map[int64]string, len(accts))
	cur := make(map[int64]money.Currency, len(accts))
	for _, a := range accts {
		name[a.ID], cur[a.ID] = a.Name, money.Currency(a.Currency)
	}

	out := make([]CategoryTxn, 0, len(rows))
	amounts := make([]money.Money, 0, len(rows))
	for _, r := range rows {
		// A categorized row is income (credits to_account) or expense (debits
		// from_account); its amount is in that account's currency.
		acctID, amt := nullID(r.ToAccountID), r.ToAmount
		if TxType(r.Type) == Expense {
			acctID, amt = nullID(r.FromAccountID), r.FromAmount
		}
		m := money.New(amt, cur[acctID])
		out = append(out, CategoryTxn{ID: r.ID, AccountID: acctID, AccountName: name[acctID], Date: r.OccurredOn, Description: r.Description, Amount: m})
		amounts = append(amounts, m)
	}
	return out, domain.SumByCurrency(amounts), nil
}

// validate checks the account exists and is a cash account, and that the type
// and amount are valid.
func (s *Service) validate(ctx context.Context, accountID int64, typ TxType, amount decimal.Decimal) error {
	if !typ.IsValid() {
		return ErrInvalidType
	}
	if !amount.IsPositive() {
		return ErrNonPositiveAmount
	}
	acct, err := store.New(s.pool).GetAccount(ctx, accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAccountNotFound
	}
	if err != nil {
		return fmt.Errorf("transaction: get account: %w", err)
	}
	if acct.Type != "cash" && acct.Type != "credit" {
		return ErrUnsupportedAccountType
	}
	return nil
}

// legs maps an income/expense on accountID to the one-row from/to shape (AD-9):
// income credits (to), expense debits (from). The unused side is NULL/0.
func legs(accountID int64, typ TxType, amount decimal.Decimal) (from, to pgtype.Int8, fromAmt, toAmt decimal.Decimal) {
	if typ == Income {
		return pgtype.Int8{}, idParam(accountID), decimal.Zero, amount
	}
	return idParam(accountID), pgtype.Int8{}, amount, decimal.Zero
}

// toTransaction maps a stored row to the display shape from accountID's
// perspective: crediting the account (income, or transfer in) is Incoming with
// the to_amount; debiting it (expense, or transfer out) uses the from_amount.
// For transfers, Counterparty is the other account's name.
// RegisterFilter narrows the cross-account register. A zero AccountID/CategoryID
// or an empty Type means "all".
type RegisterFilter struct {
	AccountID  int64
	Type       TxType
	CategoryID int64
}

// RegisterRow is one transaction formatted for the cross-account register.
// Amount is the primary leg; ToAmount is the destination leg of a cross-currency
// transfer (zero otherwise). Incoming drives the income/expense sign + color;
// transfers are neutral.
type RegisterRow struct {
	ID            int64
	Date          time.Time
	Type          TxType
	Description   string
	Category      string
	Security      string // security symbol for trade rows (buy/sell/dividend)
	Account       string // account name (income/expense) or "From → To" (transfer)
	Amount        money.Money
	ToAmount      money.Money
	Incoming      bool
	IsTransfer    bool
	CrossCurrency bool
}

// Register returns the ledger newest-first, narrowed by the filter and enriched
// with account/category names for display. It reads only (AD-2); a transfer
// appears once (AD-9), never double-counted.
func (s *Service) Register(ctx context.Context, f RegisterFilter) ([]RegisterRow, error) {
	q := store.New(s.pool)
	rows, err := q.ListTransactions(ctx)
	if err != nil {
		return nil, fmt.Errorf("transaction: register: %w", err)
	}
	accts, err := q.ListAllAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("transaction: list accounts: %w", err)
	}
	name := make(map[int64]string, len(accts))
	cur := make(map[int64]money.Currency, len(accts))
	for _, a := range accts {
		name[a.ID], cur[a.ID] = a.Name, money.Currency(a.Currency)
	}
	catNames, err := categoryNames(ctx, q)
	if err != nil {
		return nil, err
	}
	secNames, err := securitySymbols(ctx, q)
	if err != nil {
		return nil, err
	}

	out := make([]RegisterRow, 0, len(rows))
	for _, r := range rows {
		fromID, toID := nullID(r.FromAccountID), nullID(r.ToAccountID)
		catID := nullID(r.CategoryID)
		secID := nullID(r.SecurityID)
		typ := TxType(r.Type)
		if f.Type != "" && typ != f.Type {
			continue
		}
		if f.CategoryID != 0 && catID != f.CategoryID {
			continue
		}
		if f.AccountID != 0 && fromID != f.AccountID && toID != f.AccountID {
			continue
		}
		row := RegisterRow{
			ID:          r.ID,
			Date:        r.OccurredOn,
			Type:        typ,
			Description: r.Description,
			Category:    catNames[catID],
			Security:    secNames[secID],
		}
		switch typ {
		case Income, Sell, Dividend:
			// Cash credited to the to-account (income, sale proceeds, dividend).
			row.Account = name[toID]
			row.Amount = money.New(r.ToAmount, cur[toID])
			row.Incoming = true
		case Transfer:
			row.Account = name[fromID] + " → " + name[toID]
			row.Amount = money.New(r.FromAmount, cur[fromID])
			row.IsTransfer = true
			if cur[fromID] != cur[toID] {
				row.CrossCurrency = true
				row.ToAmount = money.New(r.ToAmount, cur[toID])
			}
		default: // Expense, Buy — cash debited from the from-account.
			row.Account = name[fromID]
			row.Amount = money.New(r.FromAmount, cur[fromID])
			row.Incoming = false
		}
		out = append(out, row)
	}
	return out, nil
}

func toTransaction(accountID int64, r store.Transaction, names, catNames, secNames map[int64]string) Transaction {
	catID := nullID(r.CategoryID)
	secID := nullID(r.SecurityID)
	t := Transaction{
		ID:           r.ID,
		Type:         TxType(r.Type),
		AccountID:    accountID,
		CategoryID:   catID,
		CategoryName: catNames[catID],
		SecurityID:   secID,
		Security:     secNames[secID],
		Quantity:     r.Quantity,
		Price:        r.Price,
		Date:         r.OccurredOn,
		Description:  r.Description,
		CreatedAt:    r.CreatedAt.Time,
	}
	fromID, toID := nullID(r.FromAccountID), nullID(r.ToAccountID)
	if toID == accountID {
		t.Amount, t.Incoming = r.ToAmount, true
		if t.Type == Transfer {
			t.Counterparty = names[fromID]
		}
	} else { // fromID == accountID
		t.Amount, t.Incoming = r.FromAmount, false
		if t.Type == Transfer {
			t.Counterparty = names[toID]
		}
	}
	return t
}

// idParam wraps a non-zero account id as a valid pgtype.Int8.
func idParam(id int64) pgtype.Int8 { return pgtype.Int8{Int64: id, Valid: true} }

// nullID unwraps a nullable account id to int64 (0 when NULL).
func nullID(v pgtype.Int8) int64 {
	if v.Valid {
		return v.Int64
	}
	return 0
}
