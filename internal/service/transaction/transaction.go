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
	})
	if err != nil {
		return Transaction{}, fmt.Errorf("transaction: insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Transaction{}, fmt.Errorf("transaction: commit: %w", err)
	}
	return toTransaction(accountID, row, nil, nil), nil // income/expense: no counterpart
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
	}); err != nil {
		return fmt.Errorf("transaction: insert transfer: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("transaction: commit: %w", err)
	}
	return nil
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
	out := make([]Transaction, len(rows))
	for i, r := range rows {
		out[i] = toTransaction(accountID, r, names, catNames)
	}
	return out, nil
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

	out := make([]RegisterRow, 0, len(rows))
	for _, r := range rows {
		fromID, toID := nullID(r.FromAccountID), nullID(r.ToAccountID)
		catID := nullID(r.CategoryID)
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
		}
		switch typ {
		case Income:
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
		default: // Expense
			row.Account = name[fromID]
			row.Amount = money.New(r.FromAmount, cur[fromID])
			row.Incoming = false
		}
		out = append(out, row)
	}
	return out, nil
}

func toTransaction(accountID int64, r store.Transaction, names, catNames map[int64]string) Transaction {
	catID := nullID(r.CategoryID)
	t := Transaction{
		ID:           r.ID,
		Type:         TxType(r.Type),
		AccountID:    accountID,
		CategoryID:   catID,
		CategoryName: catNames[catID],
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
