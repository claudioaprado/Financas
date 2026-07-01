package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/internal/domain"
	"github.com/claudioaprado/financas/internal/money"
	"github.com/claudioaprado/financas/internal/service/account"
	"github.com/claudioaprado/financas/internal/service/backup"
	"github.com/claudioaprado/financas/internal/service/budget"
	"github.com/claudioaprado/financas/internal/service/category"
	"github.com/claudioaprado/financas/internal/service/categoryrule"
	"github.com/claudioaprado/financas/internal/service/exchangerate"
	"github.com/claudioaprado/financas/internal/service/importer"
	"github.com/claudioaprado/financas/internal/service/price"
	"github.com/claudioaprado/financas/internal/service/security"
	"github.com/claudioaprado/financas/internal/service/transaction"
	"github.com/claudioaprado/financas/internal/service/valuation"
)

type stubAuth struct{ ok bool }

func (s stubAuth) Authenticate(_ context.Context, _, _ string) error {
	if s.ok {
		return nil
	}
	return errors.New("invalid")
}

// stubSettings is an in-memory Settings for handler tests.
type stubSettings struct{ current money.Currency }

func (s *stubSettings) DisplayCurrency(context.Context) (money.Currency, error) {
	if s.current == "" {
		return money.USD, nil
	}
	return s.current, nil
}

func (s *stubSettings) SetDisplayCurrency(_ context.Context, c money.Currency) error {
	if !money.IsSupported(c) {
		return errors.New("unsupported")
	}
	s.current = c
	return nil
}

func (s *stubSettings) ListCurrencies(context.Context) ([]money.Currency, error) {
	return money.Supported(), nil
}

// stubExchangeRates is an in-memory ExchangeRates for handler tests.
type stubExchangeRates struct {
	rates   []exchangerate.Rate
	listErr error
}

func (s *stubExchangeRates) Add(_ context.Context, from, to money.Currency, eff time.Time, rate decimal.Decimal) (exchangerate.Rate, error) {
	if from == to {
		return exchangerate.Rate{}, exchangerate.ErrSameCurrency
	}
	if !money.IsSupported(from) || !money.IsSupported(to) {
		return exchangerate.Rate{}, exchangerate.ErrUnsupportedCurrency
	}
	if !rate.IsPositive() {
		return exchangerate.Rate{}, exchangerate.ErrNonPositiveRate
	}
	r := exchangerate.Rate{ID: int64(len(s.rates) + 1), From: from, To: to, EffectiveDate: eff, Rate: rate}
	s.rates = append(s.rates, r)
	return r, nil
}

func (s *stubExchangeRates) List(context.Context) ([]exchangerate.Rate, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.rates, nil
}

// stubAccounts is an in-memory Accounts for handler tests.
type stubAccounts struct {
	accts   []account.Account
	nextID  int64
	listErr error
	getErr  error // a non-ErrNotFound failure (e.g. a DB outage) from Get
}

func (s *stubAccounts) Create(_ context.Context, name string, typ account.AccountType, cur money.Currency) (account.Account, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return account.Account{}, account.ErrEmptyName
	}
	if !typ.IsValid() {
		return account.Account{}, account.ErrInvalidType
	}
	if !money.IsSupported(cur) {
		return account.Account{}, account.ErrUnsupportedCurrency
	}
	s.nextID++
	a := account.Account{ID: s.nextID, Name: name, Type: typ, Currency: cur}
	s.accts = append(s.accts, a)
	return a, nil
}

func (s *stubAccounts) Rename(_ context.Context, id int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return account.ErrEmptyName
	}
	for i := range s.accts {
		if s.accts[i].ID == id {
			s.accts[i].Name = name
			return nil
		}
	}
	return account.ErrNotFound
}

func (s *stubAccounts) SetArchived(_ context.Context, id int64, archived bool) error {
	for i := range s.accts {
		if s.accts[i].ID == id {
			s.accts[i].Archived = archived
			return nil
		}
	}
	return account.ErrNotFound
}

func (s *stubAccounts) Get(_ context.Context, id int64) (account.Account, error) {
	if s.getErr != nil {
		return account.Account{}, s.getErr
	}
	for _, a := range s.accts {
		if a.ID == id {
			return a, nil
		}
	}
	return account.Account{}, account.ErrNotFound
}

func (s *stubAccounts) List(_ context.Context, includeArchived bool) ([]account.Account, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	out := []account.Account{}
	for _, a := range s.accts {
		if includeArchived || !a.Archived {
			out = append(out, a)
		}
	}
	return out, nil
}

// stubTransactions is an in-memory Transactions for handler tests. Rows are
// stored account-relatively (Incoming = credits that account); a transfer is two
// rows sharing an id (one per account), so List/Balance stay account-relative
// exactly like the real service. Balances are computed in USD.
type stubTransactions struct {
	rows        []transaction.Transaction
	nextID      int64
	held        map[int64]*stubHolding // by security id
	listErr     error
	registerErr error
	oversold    []string // symbols returned as inconsistent (oversold) by Holdings
}

type stubHolding struct {
	qty, basis, realized decimal.Decimal
	price                decimal.Decimal // zero = no price (renders "—")
}

func (s *stubTransactions) hold(securityID int64) *stubHolding {
	if s.held == nil {
		s.held = map[int64]*stubHolding{}
	}
	h, ok := s.held[securityID]
	if !ok {
		h = &stubHolding{}
		s.held[securityID] = h
	}
	return h
}

func (s *stubTransactions) Buy(_ context.Context, accountID, securityID int64, quantity, price, fees decimal.Decimal, date time.Time, desc string) (transaction.Transaction, error) {
	if !quantity.IsPositive() {
		return transaction.Transaction{}, transaction.ErrNonPositiveQuantity
	}
	cost := quantity.Mul(price).Add(fees)
	s.nextID++
	t := transaction.Transaction{ID: s.nextID, Type: transaction.Buy, AccountID: accountID, Amount: cost, Incoming: false, SecurityID: securityID, Security: fmt.Sprintf("S%d", securityID), Quantity: quantity, Price: price, Date: date, Description: desc}
	s.rows = append(s.rows, t)
	h := s.hold(securityID)
	h.qty = h.qty.Add(quantity)
	h.basis = h.basis.Add(cost)
	return t, nil
}

func (s *stubTransactions) Sell(_ context.Context, accountID, securityID int64, quantity, price, fees decimal.Decimal, date time.Time, desc string) (transaction.Transaction, error) {
	if !quantity.IsPositive() {
		return transaction.Transaction{}, transaction.ErrNonPositiveQuantity
	}
	h := s.hold(securityID)
	if quantity.GreaterThan(h.qty) {
		return transaction.Transaction{}, transaction.ErrOversold
	}
	bs := h.basis
	if !quantity.Equal(h.qty) {
		bs = h.basis.Mul(quantity.Div(h.qty)).RoundBank(money.MoneyScale)
	}
	proceeds := quantity.Mul(price).Sub(fees)
	h.realized = h.realized.Add(proceeds.Sub(bs))
	h.basis = h.basis.Sub(bs)
	h.qty = h.qty.Sub(quantity)
	s.nextID++
	t := transaction.Transaction{ID: s.nextID, Type: transaction.Sell, AccountID: accountID, Amount: proceeds, Incoming: true, SecurityID: securityID, Security: fmt.Sprintf("S%d", securityID), Quantity: quantity, Price: price, Date: date, Description: desc}
	s.rows = append(s.rows, t)
	return t, nil
}

func (s *stubTransactions) Dividend(_ context.Context, accountID, securityID int64, amount decimal.Decimal, date time.Time, desc string) (transaction.Transaction, error) {
	if !amount.IsPositive() {
		return transaction.Transaction{}, transaction.ErrNonPositiveAmount
	}
	s.nextID++
	t := transaction.Transaction{ID: s.nextID, Type: transaction.Dividend, AccountID: accountID, Amount: amount, Incoming: true, SecurityID: securityID, Security: fmt.Sprintf("S%d", securityID), Date: date, Description: desc}
	s.rows = append(s.rows, t)
	return t, nil
}

func (s *stubTransactions) Holdings(_ context.Context, _ int64) ([]transaction.HoldingView, money.Money, []string, error) {
	realized := decimal.Zero
	var out []transaction.HoldingView
	for id, h := range s.held {
		realized = realized.Add(h.realized)
		if !h.qty.IsPositive() {
			continue
		}
		view := transaction.HoldingView{
			SecurityID:   id,
			Symbol:       fmt.Sprintf("S%d", id),
			Quantity:     h.qty,
			AvgCost:      money.New(h.basis.Div(h.qty), money.USD).Rounded(),
			CostBasis:    money.New(h.basis, money.USD),
			RealizedGain: money.New(h.realized, money.USD),
		}
		if h.price.IsPositive() {
			market := money.New(h.qty.Mul(h.price), money.USD).Rounded()
			view.HasPrice = true
			view.Price = money.New(h.price, money.USD).Rounded()
			view.PriceDate = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
			view.MarketValue = market
			view.UnrealizedGain = money.New(market.Amount().Sub(h.basis), money.USD)
		}
		out = append(out, view)
	}
	return out, money.New(realized, money.USD), s.oversold, nil
}

func (s *stubTransactions) Record(_ context.Context, accountID int64, typ transaction.TxType, amount decimal.Decimal, date time.Time, desc string, categoryID int64) (transaction.Transaction, error) {
	if !typ.IsValid() {
		return transaction.Transaction{}, transaction.ErrInvalidType
	}
	if !amount.IsPositive() {
		return transaction.Transaction{}, transaction.ErrNonPositiveAmount
	}
	s.nextID++
	t := transaction.Transaction{ID: s.nextID, Type: typ, AccountID: accountID, Amount: amount, Incoming: typ == transaction.Income, CategoryID: categoryID, Date: date, Description: desc}
	s.rows = append(s.rows, t)
	return t, nil
}

func (s *stubTransactions) Edit(_ context.Context, _ int64, txID int64, typ transaction.TxType, amount decimal.Decimal, date time.Time, desc string, categoryID int64) error {
	if !typ.IsValid() {
		return transaction.ErrInvalidType
	}
	if !amount.IsPositive() {
		return transaction.ErrNonPositiveAmount
	}
	for i := range s.rows {
		if s.rows[i].ID == txID {
			s.rows[i].Type, s.rows[i].Amount, s.rows[i].Incoming = typ, amount, typ == transaction.Income
			s.rows[i].Date, s.rows[i].Description, s.rows[i].CategoryID = date, desc, categoryID
			return nil
		}
	}
	return transaction.ErrTxNotFound
}

func (s *stubTransactions) CategoryTransactions(_ context.Context, categoryID int64) ([]transaction.CategoryTxn, []money.Money, error) {
	var out []transaction.CategoryTxn
	var amts []money.Money
	for _, r := range s.rows {
		if r.CategoryID != categoryID {
			continue
		}
		m := money.New(r.Amount, money.USD)
		out = append(out, transaction.CategoryTxn{ID: r.ID, AccountID: r.AccountID, Date: r.Date, Description: r.Description, Amount: m})
		amts = append(amts, m)
	}
	return out, amts, nil
}

func (s *stubTransactions) Delete(_ context.Context, txID int64) error {
	kept := s.rows[:0]
	found := false
	for _, r := range s.rows {
		if r.ID == txID {
			found = true
			continue
		}
		kept = append(kept, r)
	}
	if !found {
		return transaction.ErrTxNotFound
	}
	s.rows = kept
	return nil
}

func (s *stubTransactions) Transfer(_ context.Context, fromID, toID int64, fromAmount, toAmount decimal.Decimal, date time.Time, desc string) error {
	if fromID == toID {
		return transaction.ErrSameAccount
	}
	if !fromAmount.IsPositive() {
		return transaction.ErrNonPositiveAmount
	}
	to := toAmount
	if to.IsZero() {
		to = fromAmount // stub assumes same currency unless a received amount is given
	}
	s.nextID++
	id := s.nextID
	s.rows = append(s.rows,
		transaction.Transaction{ID: id, Type: transaction.Transfer, AccountID: fromID, Amount: fromAmount, Incoming: false, Counterparty: fmt.Sprintf("acct%d", toID), Date: date, Description: desc},
		transaction.Transaction{ID: id, Type: transaction.Transfer, AccountID: toID, Amount: to, Incoming: true, Counterparty: fmt.Sprintf("acct%d", fromID), Date: date, Description: desc},
	)
	return nil
}

func (s *stubTransactions) Balance(_ context.Context, accountID int64) (money.Money, error) {
	net := decimal.Zero
	for _, r := range s.rows {
		if r.AccountID != accountID {
			continue
		}
		if r.Incoming {
			net = net.Add(r.Amount)
		} else {
			net = net.Sub(r.Amount)
		}
	}
	return money.New(net, money.USD), nil
}

func (s *stubTransactions) List(_ context.Context, accountID int64) ([]transaction.Transaction, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	out := []transaction.Transaction{}
	for _, r := range s.rows {
		if r.AccountID == accountID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *stubTransactions) Register(_ context.Context, f transaction.RegisterFilter) ([]transaction.RegisterRow, error) {
	if s.registerErr != nil {
		return nil, s.registerErr
	}
	seen := map[int64]bool{}
	var out []transaction.RegisterRow
	for _, r := range s.rows {
		if seen[r.ID] {
			continue
		}
		if f.Type != "" && r.Type != f.Type {
			continue
		}
		if f.CategoryID != 0 && r.CategoryID != f.CategoryID {
			continue
		}
		if f.AccountID != 0 {
			match := false
			for _, rr := range s.rows {
				if rr.ID == r.ID && rr.AccountID == f.AccountID {
					match = true
				}
			}
			if !match {
				continue
			}
		}
		seen[r.ID] = true
		out = append(out, transaction.RegisterRow{
			ID:          r.ID,
			Date:        r.Date,
			Type:        r.Type,
			Description: r.Description,
			Category:    r.CategoryName,
			Account:     fmt.Sprintf("acct%d", r.AccountID),
			Amount:      money.New(r.Amount, money.USD),
			Incoming:    r.Incoming,
			IsTransfer:  r.Type == transaction.Transfer,
		})
	}
	return out, nil
}

// stubCategories is an in-memory Categories for handler tests.
type stubCategories struct {
	cats    []category.Category
	usage   map[int64]int64
	nextID  int64
	listErr error
}

func (s *stubCategories) Create(_ context.Context, name string, kind category.Kind) (category.Category, error) {
	if strings.TrimSpace(name) == "" {
		return category.Category{}, category.ErrEmptyName
	}
	if !kind.IsValid() {
		return category.Category{}, category.ErrInvalidKind
	}
	s.nextID++
	c := category.Category{ID: s.nextID, Name: name, Kind: kind}
	s.cats = append(s.cats, c)
	return c, nil
}

func (s *stubCategories) List(_ context.Context) ([]category.Category, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.cats, nil
}

func (s *stubCategories) ListWithUsage(_ context.Context) ([]category.CategoryUsage, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	out := make([]category.CategoryUsage, 0, len(s.cats))
	for _, c := range s.cats {
		out = append(out, category.CategoryUsage{Category: c, Count: s.usage[c.ID]})
	}
	return out, nil
}

func (s *stubCategories) Delete(_ context.Context, id int64, force bool) error {
	if s.usage[id] > 0 && !force {
		return category.ErrCategoryInUse
	}
	for i := range s.cats {
		if s.cats[i].ID == id {
			s.cats = append(s.cats[:i], s.cats[i+1:]...)
			delete(s.usage, id)
			return nil
		}
	}
	return category.ErrNotFound
}

// stubImports is an in-memory Imports for handler tests. It uses the real (pure)
// importer.Parse / importer.ParseOFX and records committed content per format;
// every OK row counts as "new" (OFX rows without a FITID also carry a warning).
type stubImports struct {
	committed     []string
	committedOFX  []string
	committedCats map[int]int64 // last cats map received by a Commit/CommitOFX
}

func (s *stubImports) Preview(_ context.Context, _ int64, content string) (importer.Result, error) {
	return stubImportResult(content), nil
}

func (s *stubImports) Commit(_ context.Context, _ int64, content string, cats map[int]int64) (importer.Result, error) {
	s.committed = append(s.committed, content)
	s.committedCats = cats
	return stubImportResult(content), nil
}

func (s *stubImports) PreviewOFX(_ context.Context, _ int64, content string) (importer.Result, error) {
	return stubImportResultOFX(content), nil
}

func (s *stubImports) CommitOFX(_ context.Context, _ int64, content string, cats map[int]int64) (importer.Result, error) {
	s.committedOFX = append(s.committedOFX, content)
	s.committedCats = cats
	return stubImportResultOFX(content), nil
}

// stubCategoryRules is an in-memory CategoryRules for handler tests.
type stubCategoryRules struct {
	rules   []categoryrule.Rule
	nextID  int64
	listErr error
}

func (s *stubCategoryRules) List(_ context.Context) ([]categoryrule.Rule, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.rules, nil
}

func (s *stubCategoryRules) Add(_ context.Context, matchText string, categoryID int64) (categoryrule.Rule, error) {
	if strings.TrimSpace(matchText) == "" {
		return categoryrule.Rule{}, categoryrule.ErrEmptyMatch
	}
	s.nextID++
	r := categoryrule.Rule{ID: s.nextID, MatchText: matchText, CategoryID: categoryID, Kind: "expense"}
	s.rules = append(s.rules, r)
	return r, nil
}

func (s *stubCategoryRules) Delete(_ context.Context, id int64) error {
	for i := range s.rules {
		if s.rules[i].ID == id {
			s.rules = append(s.rules[:i], s.rules[i+1:]...)
			return nil
		}
	}
	return categoryrule.ErrNotFound
}

// stubBudgets is an in-memory Budgets for handler tests. Report projects each
// stored target into a trivial line (no carryover, zero actual) in BRL — enough
// to exercise the page's rendering and management flow.
type stubBudgets struct {
	budgets   []budget.Budget
	reportErr error
}

func (s *stubBudgets) Report(_ context.Context, _ int, _ time.Month) (domain.BudgetReport, error) {
	if s.reportErr != nil {
		return domain.BudgetReport{}, s.reportErr
	}
	zero := money.New(decimal.Zero, money.BRL)
	var lines []domain.BudgetLine
	for _, b := range s.budgets {
		amt := money.New(b.Amount, money.BRL)
		lines = append(lines, domain.BudgetLine{
			CategoryID: b.CategoryID, Name: b.CategoryName, Kind: b.Kind,
			Target: amt, Carryover: zero, Planned: amt, Actual: zero, Remaining: amt,
		})
	}
	return domain.BudgetReport{Lines: lines}, nil
}

func (s *stubBudgets) Set(_ context.Context, categoryID int64, amount decimal.Decimal) error {
	if amount.Sign() <= 0 {
		return budget.ErrNonPositiveAmount
	}
	for i := range s.budgets {
		if s.budgets[i].CategoryID == categoryID {
			s.budgets[i].Amount = amount
			return nil
		}
	}
	s.budgets = append(s.budgets, budget.Budget{CategoryID: categoryID, CategoryName: "Cat", Kind: "expense", Amount: amount})
	return nil
}

func (s *stubBudgets) Delete(_ context.Context, categoryID int64) error {
	for i := range s.budgets {
		if s.budgets[i].CategoryID == categoryID {
			s.budgets = append(s.budgets[:i], s.budgets[i+1:]...)
			return nil
		}
	}
	return budget.ErrNotFound
}

// stubAnalytics is an in-memory Analytics for handler tests.
type stubAnalytics struct {
	report domain.Analytics
	err    error
}

func (s *stubAnalytics) Report(_ context.Context, _ int) (domain.Analytics, error) {
	return s.report, s.err
}

func cannedAnalytics() domain.Analytics {
	brl := func(s string) money.Money { return money.New(decimal.RequireFromString(s), money.BRL) }
	return domain.Analytics{
		Spending: []domain.CategorySpend{
			{Category: "Rent", Total: brl("600"), Percent: 60},
			{Category: "Food", Total: brl("400"), Percent: 40},
		},
		Flow: []domain.MonthFlow{
			{Year: 2026, Month: time.May, Income: brl("0"), Expense: brl("0")},
			{Year: 2026, Month: time.June, Income: brl("2000"), Expense: brl("1000")},
		},
	}
}

func stubImportResult(content string) importer.Result {
	res := importer.Result{AccountName: "Acc", Currency: "USD"}
	for _, p := range importer.Parse(content) {
		pr := importer.PreviewRow{ParsedRow: p}
		if p.OK {
			pr.Status = "new"
			res.New++
		} else {
			pr.Status = "error"
			res.Errors++
		}
		res.Rows = append(res.Rows, pr)
	}
	return res
}

func stubImportResultOFX(content string) importer.Result {
	res := importer.Result{AccountName: "Acc", Currency: "USD"}
	for _, p := range importer.ParseOFX(content) {
		pr := importer.PreviewRow{ParsedRow: p}
		switch {
		case !p.OK:
			pr.Status = "error"
			res.Errors++
		case p.FITID == "":
			pr.Status = "new"
			pr.Warning = "no FITID"
			res.New++
		default:
			pr.Status = "new"
			res.New++
		}
		res.Rows = append(res.Rows, pr)
	}
	return res
}

// stubSecurities is an in-memory Securities for handler tests. It normalizes the
// symbol and rejects duplicates case-insensitively, mirroring the real service.
type stubSecurities struct {
	secs    []security.Security
	nextID  int64
	listErr error
}

func (s *stubSecurities) Create(_ context.Context, symbol, name string, typ security.SecurityType, quote money.Currency) (security.Security, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	name = strings.TrimSpace(name)
	if symbol == "" {
		return security.Security{}, security.ErrEmptySymbol
	}
	if name == "" {
		return security.Security{}, security.ErrEmptyName
	}
	if !typ.IsValid() {
		return security.Security{}, security.ErrInvalidType
	}
	if !money.IsSupported(quote) {
		return security.Security{}, security.ErrUnsupportedCurrency
	}
	for _, existing := range s.secs {
		if existing.Symbol == symbol {
			return security.Security{}, security.ErrDuplicateSymbol
		}
	}
	s.nextID++
	sec := security.Security{ID: s.nextID, Symbol: symbol, Name: name, Type: typ, QuoteCurrency: quote}
	s.secs = append(s.secs, sec)
	return sec, nil
}

func (s *stubSecurities) List(_ context.Context) ([]security.Security, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.secs, nil
}

// stubPrices is an in-memory Prices for handler tests. It rejects non-positive
// prices, mirroring the real service.
type stubPrices struct {
	prices  []price.Price
	nextID  int64
	listErr error
}

func (s *stubPrices) Add(_ context.Context, securityID int64, effective time.Time, p decimal.Decimal) (price.Price, error) {
	if !p.IsPositive() {
		return price.Price{}, price.ErrNonPositivePrice
	}
	s.nextID++
	row := price.Price{ID: s.nextID, SecurityID: securityID, Symbol: fmt.Sprintf("S%d", securityID), Currency: money.BRL, EffectiveDate: effective, Price: p}
	s.prices = append(s.prices, row)
	return row, nil
}

func (s *stubPrices) List(_ context.Context) ([]price.Price, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.prices, nil
}

// stubValuation is an in-memory Valuation for handler tests. It returns a canned
// Portfolio (or err when set).
type stubValuation struct {
	portfolio  valuation.Portfolio
	dashboard  valuation.Dashboard
	series     []valuation.SeriesPoint
	seriesErr  error
	allocation valuation.Allocation
	allocErr   error
	insight    valuation.Insight
	insightErr error
	err        error
}

func (s *stubValuation) Portfolio(context.Context) (valuation.Portfolio, error) {
	return s.portfolio, s.err
}

func (s *stubValuation) Dashboard(context.Context) (valuation.Dashboard, error) {
	return s.dashboard, s.err
}

func (s *stubValuation) ValueSeries(context.Context, time.Time) ([]valuation.SeriesPoint, error) {
	return s.series, s.seriesErr
}

func (s *stubValuation) Allocation(_ context.Context, by string) (valuation.Allocation, error) {
	a := s.allocation
	if a.By == "" {
		a.By = valuation.AllocBy(by)
	}
	return a, s.allocErr
}

func (s *stubValuation) Insight(context.Context) (valuation.Insight, error) {
	return s.insight, s.insightErr
}

// cannedInsight is the default dashboard insight served in handler tests: net
// worth up 4.0% this month.
func cannedInsight() valuation.Insight {
	return valuation.Insight{
		Pct:      decimal.RequireFromString("4.0"),
		Up:       true,
		HasData:  true,
		NetWorth: money.New(decimal.RequireFromString("5200.0000"), money.BRL),
		Display:  money.BRL,
	}
}

// cannedAllocation is the default allocation served in handler tests: a two-slice
// breakdown (80 / 20) with a USD currency excluded for lack of a rate (partial).
func cannedAllocation() valuation.Allocation {
	return valuation.Allocation{
		By:      "security",
		Display: money.BRL,
		Total:   money.New(decimal.RequireFromString("5000.0000"), money.BRL),
		Missing: []money.Currency{money.USD},
		Groups: []valuation.AllocationGroup{
			{Key: "AAPL", Percent: 80, Value: money.New(decimal.RequireFromString("4000.0000"), money.BRL)},
			{Key: "PETR4", Percent: 20, Value: money.New(decimal.RequireFromString("1000.0000"), money.BRL)},
		},
	}
}

// cannedSeries is the default Net Worth trend served in handler tests: three
// ascending points, the middle one partial (a held currency had no rate).
func cannedSeries() []valuation.SeriesPoint {
	return []valuation.SeriesPoint{
		{Date: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), Value: money.New(decimal.RequireFromString("5000.0000"), money.BRL)},
		{Date: time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC), Value: money.New(decimal.RequireFromString("5100.0000"), money.BRL), Partial: true},
		{Date: time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC), Value: money.New(decimal.RequireFromString("5300.0000"), money.BRL)},
	}
}

// cannedDashboard is the default KPI row served in handler tests: a gain with no
// prior sample (→ "—"), and value cards with up / down / flat deltas.
func cannedDashboard() valuation.Dashboard {
	return valuation.Dashboard{
		Display: money.BRL,
		NetWorth: valuation.KPI{
			Value:    money.New(decimal.RequireFromString("1234.5000"), money.BRL),
			DeltaPct: decimal.RequireFromString("2.0"), DeltaUp: true, HasDelta: true,
		},
		Portfolio: valuation.KPI{
			Value:    money.New(decimal.RequireFromString("800.0000"), money.BRL),
			DeltaPct: decimal.RequireFromString("-1.1"), DeltaDown: true, HasDelta: true,
		},
		GainLoss: valuation.KPI{
			Value:    money.New(decimal.RequireFromString("100.0000"), money.BRL),
			Positive: true, // HasDelta false → "—"
		},
		Cash: valuation.KPI{
			Value:    money.New(decimal.RequireFromString("434.5000"), money.BRL),
			DeltaPct: decimal.RequireFromString("0.0"), HasDelta: true,
		},
	}
}

// cannedPortfolio is the default portfolio served in handler tests: one priced
// BRL holding, Display-Currency Net Worth + Portfolio value, no warnings.
func cannedPortfolio() valuation.Portfolio {
	return valuation.Portfolio{
		Display:        money.BRL,
		NetWorth:       money.New(decimal.RequireFromString("1234.5000"), money.BRL),
		PortfolioValue: money.New(decimal.RequireFromString("800.0000"), money.BRL),
		RealizedByCurrency: []money.Money{
			money.New(decimal.RequireFromString("80.0000"), money.BRL),
		},
		Holdings: []valuation.HoldingValuation{{
			AccountName:    "Broker",
			Symbol:         "PETR4",
			Name:           "Petrobras",
			Currency:       money.BRL,
			Quantity:       decimal.RequireFromString("10"),
			HasPrice:       true,
			Price:          money.New(decimal.RequireFromString("80.0000"), money.BRL),
			PriceDate:      time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			Valuation:      money.New(decimal.RequireFromString("800.0000"), money.BRL),
			CostBasis:      money.New(decimal.RequireFromString("700.0000"), money.BRL),
			UnrealizedGain: money.New(decimal.RequireFromString("100.0000"), money.BRL),
		}},
	}
}

// testDeps builds Deps with a fresh in-memory session manager (so each router
// instance has an isolated store) and stubs for the services.
// stubBackup is an in-memory Backup for handler tests: it returns a canned
// Export, or err when set, and records the bytes passed to Restore.
type stubBackup struct {
	export         backup.Export
	err            error
	restoreSummary backup.RestoreSummary
	restoreErr     error
	restoredBytes  []byte
	restoreCalled  bool
}

func (s *stubBackup) Export(context.Context) (backup.Export, error) {
	return s.export, s.err
}

func (s *stubBackup) Restore(_ context.Context, raw []byte) (backup.RestoreSummary, error) {
	s.restoreCalled = true
	s.restoredBytes = raw
	return s.restoreSummary, s.restoreErr
}

// cannedExport is a small but representative authored-data snapshot for the
// /export handler tests.
func cannedExport() backup.Export {
	catID := int64(7)
	toAcct := int64(1)
	return backup.Export{
		Schema:          backup.ExportSchema,
		Version:         backup.ExportVersion,
		ExportedAt:      "2026-06-30T00:00:00Z",
		DisplayCurrency: "BRL",
		Accounts:        []backup.AccountDTO{{ID: 1, Name: "CashUSD", Type: "cash", Currency: "USD", CreatedAt: "2026-06-01T00:00:00Z"}},
		Categories:      []backup.CategoryDTO{{ID: 7, Name: "Salary", Kind: "income", CreatedAt: "2026-06-01T00:00:00Z"}},
		Securities:      []backup.SecurityDTO{},
		ExchangeRates:   []backup.ExchangeRateDTO{},
		Prices:          []backup.PriceDTO{},
		Transactions: []backup.TransactionDTO{{
			ID: 1, Type: "income", ToAccountID: &toAcct, CategoryID: &catID,
			FromAmount: "0", ToAmount: "1000", OccurredOn: "2026-06-03",
			Description: "pay", Quantity: "0", Price: "0", Fees: "0",
		}},
	}
}

func testDeps(authOK bool, ready ReadyCheck) Deps {
	return Deps{
		Sessions:      scs.New(),
		Auth:          stubAuth{ok: authOK},
		Ready:         ready,
		Settings:      &stubSettings{},
		ExchangeRates: &stubExchangeRates{},
		Prices:        &stubPrices{},
		Accounts:      &stubAccounts{},
		Transactions:  &stubTransactions{},
		Categories:    &stubCategories{usage: map[int64]int64{}},
		CategoryRules: &stubCategoryRules{},
		Budgets:       &stubBudgets{},
		Analytics:     &stubAnalytics{report: cannedAnalytics()},
		Securities:    &stubSecurities{},
		Imports:       &stubImports{},
		Valuation:     &stubValuation{portfolio: cannedPortfolio(), dashboard: cannedDashboard(), series: cannedSeries(), allocation: cannedAllocation(), insight: cannedInsight()},
		Backup:        &stubBackup{export: cannedExport()},
		OwnerName:     "TestOwner",
	}
}

func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	NewRouter(testDeps(false, nil)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("healthz = %d %q, want 200 ok", rec.Code, rec.Body.String())
	}
}

func TestReadyz(t *testing.T) {
	t.Run("no check -> 503", func(t *testing.T) {
		rec := httptest.NewRecorder()
		NewRouter(testDeps(false, nil)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
	})
	t.Run("ok -> 200", func(t *testing.T) {
		rec := httptest.NewRecorder()
		deps := testDeps(false, func(context.Context) error { return nil })
		NewRouter(deps).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if rec.Code != http.StatusOK || rec.Body.String() != "ready" {
			t.Fatalf("readyz = %d %q, want 200 ready", rec.Code, rec.Body.String())
		}
	})
}

func TestRequireAuthRedirect(t *testing.T) {
	rec := httptest.NewRecorder()
	NewRouter(testDeps(false, nil)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("unauth GET / = %d -> %q, want 303 -> /login", rec.Code, rec.Header().Get("Location"))
	}
}

func TestLoginBadCredentials(t *testing.T) {
	rec := httptest.NewRecorder()
	NewRouter(testDeps(false, nil)).ServeHTTP(rec, loginPost("owner", "wrong"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad login = %d, want 401", rec.Code)
	}
}

func TestLoginLogoutFlow(t *testing.T) {
	router := NewRouter(testDeps(true, nil)) // one instance -> shared memstore

	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	if recLogin.Code != http.StatusSeeOther || recLogin.Header().Get("Location") != "/" {
		t.Fatalf("good login = %d -> %q, want 303 -> /", recLogin.Code, recLogin.Header().Get("Location"))
	}
	cookie := sessionCookie(t, recLogin)

	// Authenticated request reaches the protected area.
	recHome := httptest.NewRecorder()
	router.ServeHTTP(recHome, withCookie(httptest.NewRequest(http.MethodGet, "/", nil), cookie))
	if recHome.Code != http.StatusOK {
		t.Fatalf("authed GET / = %d, want 200", recHome.Code)
	}

	// Logout destroys the session.
	recOut := httptest.NewRecorder()
	router.ServeHTTP(recOut, withCookie(httptest.NewRequest(http.MethodPost, "/logout", nil), cookie))
	if recOut.Code != http.StatusSeeOther {
		t.Fatalf("logout = %d, want 303", recOut.Code)
	}

	// The old cookie no longer authenticates.
	recAfter := httptest.NewRecorder()
	router.ServeHTTP(recAfter, withCookie(httptest.NewRequest(http.MethodGet, "/", nil), cookie))
	if recAfter.Code != http.StatusSeeOther {
		t.Fatalf("post-logout GET / = %d, want 303 redirect to login", recAfter.Code)
	}
}

func TestShellRenderedAfterLogin(t *testing.T) {
	router := NewRouter(testDeps(true, nil))

	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withCookie(httptest.NewRequest(http.MethodGet, "/", nil), cookie))
	if rec.Code != http.StatusOK {
		t.Fatalf("authed GET / = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Bem-vindo(a) de volta", "TestOwner", "Painel", "Investimentos", "Transações", "Contas", "Análises", "/logout"} {
		if !strings.Contains(body, want) {
			t.Errorf("shell missing %q", want)
		}
	}
}

func TestNavTargetRequiresAuth(t *testing.T) {
	for _, path := range []string{"/investments", "/transactions", "/accounts", "/analytics"} {
		rec := httptest.NewRecorder()
		NewRouter(testDeps(false, nil)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
			t.Errorf("unauth %s = %d -> %q, want 303 -> /login", path, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestNavTargetAuthed(t *testing.T) {
	router := NewRouter(testDeps(true, nil))
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withCookie(httptest.NewRequest(http.MethodGet, "/investments", nil), cookie))
	if rec.Code != http.StatusOK {
		t.Fatalf("authed GET /investments = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "Investimentos") || !strings.Contains(body, "Patrimônio líquido") {
		t.Errorf("/investments page missing expected content")
	}
}

func TestInvestmentsPageRendersPortfolio(t *testing.T) {
	router := NewRouter(testDeps(true, nil))
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withCookie(httptest.NewRequest(http.MethodGet, "/investments", nil), cookie))
	if rec.Code != http.StatusOK {
		t.Fatalf("authed GET /investments = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	// Net Worth + Portfolio value (Display Currency) and a per-holding row.
	for _, want := range []string{"Patrimônio líquido", "1.234,50 BRL", "Valor da carteira", "800,00 BRL", "PETR4"} {
		if !strings.Contains(body, want) {
			t.Errorf("/investments page missing %q", want)
		}
	}
}

func TestInvestmentsPageWarnings(t *testing.T) {
	deps := testDeps(true, nil)
	deps.Valuation = &stubValuation{portfolio: valuation.Portfolio{
		Display:        money.BRL,
		NetWorth:       money.New(decimal.RequireFromString("500.0000"), money.BRL),
		PortfolioValue: money.New(decimal.RequireFromString("300.0000"), money.BRL),
		Missing:        []money.Currency{money.USD},
		Unpriced:       []string{"VOO", "QQQ"},
	}}
	router := NewRouter(deps)
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withCookie(httptest.NewRequest(http.MethodGet, "/investments", nil), cookie))
	if rec.Code != http.StatusOK {
		t.Fatalf("authed GET /investments = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	// Missing-rate warning links to /exchange-rates; unpriced note links to /prices.
	for _, want := range []string{"exclui", "USD", "/exchange-rates", "VOO", "QQQ", "/prices"} {
		if !strings.Contains(body, want) {
			t.Errorf("/investments warnings missing %q", want)
		}
	}
}

func TestDashboardRendersKPIs(t *testing.T) {
	router := NewRouter(testDeps(true, nil))
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withCookie(httptest.NewRequest(http.MethodGet, "/", nil), cookie))
	if rec.Code != http.StatusOK {
		t.Fatalf("authed GET / = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	// Four KPI labels + their Display-Currency figures.
	for _, want := range []string{
		"Patrimônio líquido", "1.234,50 BRL",
		"Valor da carteira", "800,00 BRL",
		"Ganho/perda total", "100,00 BRL",
		"Caixa", "434,50 BRL",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	// Per-card deltas: up arrow + magnitude, down arrow + magnitude, and a "—" for
	// the gain card (no prior sample).
	for _, want := range []string{"▲", "2,0%", "▼", "1,1%", "0,0%", "—"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard delta missing %q", want)
		}
	}
}

func TestDashboardLossCardSingleMinus(t *testing.T) {
	// A negative Total Gain/Loss must render exactly one minus: the Amount
	// primitive supplies the "−" glyph, so the figure is the magnitude — never a
	// double sign ("−-100.0000 BRL").
	deps := testDeps(true, nil)
	d := cannedDashboard()
	d.GainLoss = valuation.KPI{
		Value:    money.New(decimal.RequireFromString("-100.0000"), money.BRL),
		Negative: true,
	}
	deps.Valuation = &stubValuation{portfolio: cannedPortfolio(), dashboard: d}
	router := NewRouter(deps)
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withCookie(httptest.NewRequest(http.MethodGet, "/", nil), cookie))
	body := rec.Body.String()
	for _, bad := range []string{"−-100.0000", "- -100.0000", "−  -100.0000"} {
		if strings.Contains(body, bad) {
			t.Errorf("loss card double-renders the sign (%q present)", bad)
		}
	}
	// Magnitude + loss colour + the single − sign from the primitive.
	for _, want := range []string{"100,00 BRL", "text-loss", "−"} {
		if !strings.Contains(body, want) {
			t.Errorf("loss card missing %q", want)
		}
	}
}

func TestDashboardErrorBanner(t *testing.T) {
	deps := testDeps(true, nil)
	deps.Valuation = &stubValuation{err: errors.New("db down")}
	router := NewRouter(deps)
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withCookie(httptest.NewRequest(http.MethodGet, "/", nil), cookie))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("dashboard (load error) = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Não foi possível carregar seu painel") {
		t.Errorf("dashboard error banner missing the load-error message")
	}
}

// An oversold position is now a partial-total NOTICE, not a hard failure: the
// dashboard still renders 200 with the KPIs and names the offending symbol.
func TestDashboardOversoldIsANoticeNot500(t *testing.T) {
	deps := testDeps(true, nil)
	d := cannedDashboard()
	d.Oversold = []string{"ACME"}
	deps.Valuation = &stubValuation{portfolio: cannedPortfolio(), dashboard: d, series: cannedSeries(), allocation: cannedAllocation(), insight: cannedInsight()}
	body := dashboardBody(t, deps, "/") // dashboardBody asserts 200
	for _, want := range []string{"venda excede a quantidade mantida", "ACME", "Patrimônio líquido"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard oversold notice missing %q", want)
		}
	}
}

func dashboardBody(t *testing.T, deps Deps, path string) string {
	t.Helper()
	router := NewRouter(deps)
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withCookie(httptest.NewRequest(http.MethodGet, path, nil), cookie))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", path, rec.Code)
	}
	return rec.Body.String()
}

func TestDashboardRendersTrendChart(t *testing.T) {
	body := dashboardBody(t, testDeps(true, nil), "/")
	// The SVG trend, min/max + date labels, range toggle, and partial note.
	for _, want := range []string{
		"Patrimônio ao longo do tempo", "<svg", "<polyline", "<path",
		"5.000,00 BRL", "5.300,00 BRL", // min / max labels
		"01/06/2026", "20/06/2026", // start / end dates
		"1M", "3M", "1Y", "All", "/?range=1m", "/?range=all",
		"Alguns pontos são parciais", // a partial point present
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard chart missing %q", want)
		}
	}
	// Default range is 1Y → that link is current.
	if !strings.Contains(body, `aria-current="true">1Y`) {
		t.Errorf("default range 1Y should be marked current")
	}
}

func TestDashboardChartRangeActive(t *testing.T) {
	body := dashboardBody(t, testDeps(true, nil), "/?range=1m")
	if !strings.Contains(body, `aria-current="true">1M`) {
		t.Errorf("?range=1m should mark the 1M link current")
	}
	if strings.Contains(body, `aria-current="true">1Y`) {
		t.Errorf("1Y should not be current when range=1m")
	}
}

func TestDashboardChartEmptyState(t *testing.T) {
	deps := testDeps(true, nil)
	deps.Valuation = &stubValuation{
		portfolio: cannedPortfolio(),
		dashboard: cannedDashboard(),
		series:    cannedSeries()[:1], // a single point → not enough for a line
	}
	body := dashboardBody(t, deps, "/")
	if !strings.Contains(body, "Ainda não há histórico suficiente") {
		t.Errorf("single-point series should render the empty state")
	}
	if strings.Contains(body, "<polyline") {
		t.Errorf("empty chart should not render a line")
	}
}

func TestDashboardRendersAllocation(t *testing.T) {
	body := dashboardBody(t, testDeps(true, nil), "/")
	// The donut SVG + arcs, the legend rows (key + percent + value), the centre
	// total, the Security/Account toggle, and the partial note.
	for _, want := range []string{
		"Alocação da carteira", "<svg", "stroke-dasharray", "stroke-alloc-1", "bg-alloc-1",
		"AAPL", "80%", "4.000,00 BRL", // legend slice 1
		"PETR4", "20%", "1.000,00 BRL", // legend slice 2
		"Total", "5.000,00 BRL", // centre total
		"Ativo", "Conta", "by=account", "by=security", // toggle links (& is HTML-escaped)
		"A alocação exclui", "USD", // partial note
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard allocation missing %q", want)
		}
	}
	// Default dimension is Security → that link is current.
	if !strings.Contains(body, `aria-current="true">Ativo`) {
		t.Errorf("default dimension Security should be marked current")
	}
	// The chart range links must preserve the active dimension (5.4 cross-preserve;
	// the ampersand is HTML-escaped to &amp; in the rendered href).
	if !strings.Contains(body, "/?range=1m&amp;by=security") {
		t.Errorf("chart range links should carry the active by= dimension")
	}
}

func TestDashboardAllocationByAccount(t *testing.T) {
	deps := testDeps(true, nil)
	a := cannedAllocation()
	a.By = "account"
	a.Groups = []valuation.AllocationGroup{
		{Key: "Broker A", Percent: 100, Value: money.New(decimal.RequireFromString("5000.0000"), money.BRL)},
	}
	a.Missing = nil
	deps.Valuation = &stubValuation{portfolio: cannedPortfolio(), dashboard: cannedDashboard(), series: cannedSeries(), allocation: a}
	body := dashboardBody(t, deps, "/?by=account")
	if !strings.Contains(body, `aria-current="true">Conta`) {
		t.Errorf("?by=account should mark the Account link current")
	}
	if !strings.Contains(body, "Broker A") {
		t.Errorf("by-account allocation should list the account name")
	}
	if strings.Contains(body, "A alocação exclui") {
		t.Errorf("no missing currency → no partial note")
	}
}

func TestDashboardAllocationEmptyState(t *testing.T) {
	deps := testDeps(true, nil)
	deps.Valuation = &stubValuation{
		portfolio:  cannedPortfolio(),
		dashboard:  cannedDashboard(),
		series:     cannedSeries(),
		allocation: valuation.Allocation{By: "security", Display: money.BRL, Total: money.New(decimal.Zero, money.BRL)},
	}
	body := dashboardBody(t, deps, "/")
	if !strings.Contains(body, "Ainda não há posições investidas para alocar") {
		t.Errorf("no groups should render the allocation empty state")
	}
	// The donut should not render when there is no data (no arc dasharrays).
	if strings.Contains(body, "stroke-dasharray") {
		t.Errorf("empty allocation should not render donut arcs")
	}
}

func TestDashboardAllocationErrorState(t *testing.T) {
	// AC #6: an allocation load failure shows a distinct "couldn't load" message
	// (error ≠ no-data) while the rest of the dashboard (KPIs, chart) still renders.
	deps := testDeps(true, nil)
	deps.Valuation = &stubValuation{
		portfolio: cannedPortfolio(),
		dashboard: cannedDashboard(),
		series:    cannedSeries(),
		allocErr:  errors.New("boom"),
	}
	body := dashboardBody(t, deps, "/")
	// The apostrophe in "Couldn't" is HTML-escaped to &#39; in the rendered text,
	// so match the unambiguous tail of the distinct error copy.
	if !strings.Contains(body, "carregar sua alocação agora") {
		t.Errorf("allocation error should render the distinct couldn't-load message")
	}
	if strings.Contains(body, "Ainda não há posições investidas para alocar") {
		t.Errorf("an error must not render the no-data empty state (error ≠ no-data)")
	}
	// The rest of the dashboard survives the allocation error.
	for _, want := range []string{"Patrimônio líquido", "Patrimônio ao longo do tempo"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard should still render %q despite the allocation error", want)
		}
	}
}

func TestDashboardRendersInsight(t *testing.T) {
	body := dashboardBody(t, testDeps(true, nil), "/")
	for _, want := range []string{
		"Seu patrimônio subiu 4,0% neste mês", // the framed sentence
		"▲", "subiu",                          // direction cue + sr-only label
		"text-accent", "bg-accent", // bold accent call-out identity
		"Patrimônio líquido 5.200,00 BRL", // context line
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard insight missing %q", want)
		}
	}
}

func TestDashboardInsightEmptyState(t *testing.T) {
	deps := testDeps(true, nil)
	deps.Valuation = &stubValuation{
		portfolio:  cannedPortfolio(),
		dashboard:  cannedDashboard(),
		series:     cannedSeries(),
		allocation: cannedAllocation(),
		insight:    valuation.Insight{HasData: false}, // no month-start baseline
	}
	body := dashboardBody(t, deps, "/")
	if !strings.Contains(body, "a evolução do seu patrimônio aparecerá aqui") {
		t.Errorf("no-baseline insight should render the calm fallback")
	}
	// An insight load error must also not crash the page (KPIs still render).
	deps.Valuation = &stubValuation{
		portfolio: cannedPortfolio(), dashboard: cannedDashboard(), series: cannedSeries(),
		allocation: cannedAllocation(), insightErr: errors.New("boom"),
	}
	body = dashboardBody(t, deps, "/")
	if !strings.Contains(body, "Patrimônio líquido") {
		t.Errorf("dashboard should still render despite an insight error")
	}
}

func TestDashboardRecentActivity(t *testing.T) {
	deps := testDeps(true, nil)
	deps.Transactions = &stubTransactions{rows: []transaction.Transaction{
		{ID: 1, Date: time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC), Type: transaction.Income, Description: "Salary", AccountID: 1, Amount: decimal.RequireFromString("5000.0000"), Incoming: true},
		{ID: 2, Date: time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC), Type: transaction.Expense, Description: "Groceries", AccountID: 1, Amount: decimal.RequireFromString("120.5000"), CategoryName: "Food"},
	}}
	body := dashboardBody(t, deps, "/")
	for _, want := range []string{
		"Atividade recente", "Ver todos", `href="/transactions"`,
		"Salary", "Groceries", "Food", // descriptions + category badge
		"+5.000,00 USD", "-120,50 USD", // signed amounts (stub Register uses USD)
		"text-gain", "text-loss", // income green / expense red
	} {
		if !strings.Contains(body, want) {
			t.Errorf("recent-activity widget missing %q", want)
		}
	}
}

func TestDashboardRecentActivityEmpty(t *testing.T) {
	// testDeps uses an empty stubTransactions → the widget shows its empty state.
	body := dashboardBody(t, testDeps(true, nil), "/")
	if !strings.Contains(body, "Nenhuma transação ainda") {
		t.Errorf("empty ledger should render the recent-activity empty state")
	}
}

func TestSettingsRequiresAuth(t *testing.T) {
	rec := httptest.NewRecorder()
	NewRouter(testDeps(false, nil)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/settings", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("unauth GET /settings = %d -> %q, want 303 -> /login", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSettingsShowsAndUpdates(t *testing.T) {
	router := NewRouter(testDeps(true, nil))

	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	// GET shows both currency options, defaulting to USD in the header.
	recGet := httptest.NewRecorder()
	router.ServeHTTP(recGet, withCookie(httptest.NewRequest(http.MethodGet, "/settings", nil), cookie))
	if recGet.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d, want 200", recGet.Code)
	}
	for _, want := range []string{"Moeda de exibição", "USD", "BRL"} {
		if !strings.Contains(recGet.Body.String(), want) {
			t.Errorf("settings page missing %q", want)
		}
	}

	// POST BRL redirects, and the header then reflects BRL.
	recPost := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader("currency=BRL"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recPost, withCookie(req, cookie))
	if recPost.Code != http.StatusSeeOther {
		t.Fatalf("POST /settings = %d, want 303", recPost.Code)
	}

	recHome := httptest.NewRecorder()
	router.ServeHTTP(recHome, withCookie(httptest.NewRequest(http.MethodGet, "/", nil), cookie))
	if !strings.Contains(recHome.Body.String(), "BRL") {
		t.Error("shell header should show BRL after switching display currency")
	}
}

func TestExchangeRatesRequiresAuth(t *testing.T) {
	rec := httptest.NewRecorder()
	NewRouter(testDeps(false, nil)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/exchange-rates", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("unauth GET /exchange-rates = %d -> %q, want 303 -> /login", rec.Code, rec.Header().Get("Location"))
	}
}

func TestExchangeRatesAddAndList(t *testing.T) {
	router := NewRouter(testDeps(true, nil))
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	// GET shows the add form.
	recGet := httptest.NewRecorder()
	router.ServeHTTP(recGet, withCookie(httptest.NewRequest(http.MethodGet, "/exchange-rates", nil), cookie))
	if recGet.Code != http.StatusOK || !strings.Contains(recGet.Body.String(), "Taxas de câmbio") {
		t.Fatalf("GET /exchange-rates = %d, missing heading", recGet.Code)
	}

	// POST a valid rate redirects, and it then appears in the list.
	recAdd := httptest.NewRecorder()
	add := httptest.NewRequest(http.MethodPost, "/exchange-rates", strings.NewReader("from=USD&to=BRL&effective_date=2024-01-01&rate=5,25"))
	add.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recAdd, withCookie(add, cookie))
	if recAdd.Code != http.StatusSeeOther {
		t.Fatalf("POST valid rate = %d, want 303", recAdd.Code)
	}
	recList := httptest.NewRecorder()
	router.ServeHTTP(recList, withCookie(httptest.NewRequest(http.MethodGet, "/exchange-rates", nil), cookie))
	body := recList.Body.String()
	for _, want := range []string{"USD", "BRL", "01/01/2024", "5,25"} {
		if !strings.Contains(body, want) {
			t.Errorf("rates list missing %q", want)
		}
	}

	// An invalid (same-currency) rate is rejected without crashing.
	recBad := httptest.NewRecorder()
	bad := httptest.NewRequest(http.MethodPost, "/exchange-rates", strings.NewReader("from=USD&to=USD&effective_date=2024-01-01&rate=1"))
	bad.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recBad, withCookie(bad, cookie))
	if recBad.Code != http.StatusBadRequest {
		t.Fatalf("POST same-currency = %d, want 400", recBad.Code)
	}
}

func TestAccountsRequiresAuth(t *testing.T) {
	rec := httptest.NewRecorder()
	NewRouter(testDeps(false, nil)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/accounts", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("unauth GET /accounts = %d -> %q, want 303 -> /login", rec.Code, rec.Header().Get("Location"))
	}
}

func TestAccountsCreateRenameArchive(t *testing.T) {
	router := NewRouter(testDeps(true, nil))
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	// GET shows the create form + the per-type balance labels.
	recGet := httptest.NewRecorder()
	router.ServeHTTP(recGet, withCookie(httptest.NewRequest(http.MethodGet, "/accounts", nil), cookie))
	if recGet.Code != http.StatusOK || !strings.Contains(recGet.Body.String(), "Criar conta") {
		t.Fatalf("GET /accounts = %d, missing create form", recGet.Code)
	}

	// POST a valid account redirects, and it then appears in the list.
	recAdd := httptest.NewRecorder()
	add := httptest.NewRequest(http.MethodPost, "/accounts", strings.NewReader("name=Checking&type=cash&currency=USD"))
	add.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recAdd, withCookie(add, cookie))
	if recAdd.Code != http.StatusSeeOther {
		t.Fatalf("POST valid account = %d, want 303", recAdd.Code)
	}
	recList := httptest.NewRecorder()
	router.ServeHTTP(recList, withCookie(httptest.NewRequest(http.MethodGet, "/accounts", nil), cookie))
	if body := recList.Body.String(); !strings.Contains(body, "Checking") || !strings.Contains(body, "Saldo em caixa") {
		t.Errorf("accounts list missing the created account or its balance label")
	}

	// Rename it (id=1, the first created account in the stub).
	recRen := httptest.NewRecorder()
	ren := httptest.NewRequest(http.MethodPost, "/accounts/rename", strings.NewReader("id=1&name=Main+Checking"))
	ren.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recRen, withCookie(ren, cookie))
	if recRen.Code != http.StatusSeeOther {
		t.Fatalf("POST rename = %d, want 303", recRen.Code)
	}
	recList2 := httptest.NewRecorder()
	router.ServeHTTP(recList2, withCookie(httptest.NewRequest(http.MethodGet, "/accounts", nil), cookie))
	if !strings.Contains(recList2.Body.String(), "Main Checking") {
		t.Errorf("renamed account not reflected in the list")
	}

	// Archive it: it drops from the default list and reappears under ?show=archived.
	recArch := httptest.NewRecorder()
	arch := httptest.NewRequest(http.MethodPost, "/accounts/archive", strings.NewReader("id=1&archived=true"))
	arch.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recArch, withCookie(arch, cookie))
	if recArch.Code != http.StatusSeeOther {
		t.Fatalf("POST archive = %d, want 303", recArch.Code)
	}
	recActive := httptest.NewRecorder()
	router.ServeHTTP(recActive, withCookie(httptest.NewRequest(http.MethodGet, "/accounts", nil), cookie))
	if strings.Contains(recActive.Body.String(), "Main Checking") {
		t.Errorf("archived account should be absent from the default list")
	}
	recArchived := httptest.NewRecorder()
	router.ServeHTTP(recArchived, withCookie(httptest.NewRequest(http.MethodGet, "/accounts?show=archived", nil), cookie))
	if !strings.Contains(recArchived.Body.String(), "Main Checking") {
		t.Errorf("archived account should appear under show=archived")
	}
}

func TestAccountsInvalidCreate(t *testing.T) {
	router := NewRouter(testDeps(true, nil))
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	// An empty name is rejected without crashing.
	rec := httptest.NewRecorder()
	bad := httptest.NewRequest(http.MethodPost, "/accounts", strings.NewReader("name=+&type=cash&currency=USD"))
	bad.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(rec, withCookie(bad, cookie))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST empty-name account = %d, want 400", rec.Code)
	}
}

func TestAccountDetailRequiresAuth(t *testing.T) {
	rec := httptest.NewRecorder()
	NewRouter(testDeps(false, nil)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/accounts/1", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("unauth GET /accounts/1 = %d -> %q, want 303 -> /login", rec.Code, rec.Header().Get("Location"))
	}
}

func TestAccountTransactionsFlow(t *testing.T) {
	router := NewRouter(testDeps(true, nil))
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	// Create a cash USD account (becomes id 1 in the stub).
	recAcct := httptest.NewRecorder()
	mk := httptest.NewRequest(http.MethodPost, "/accounts", strings.NewReader("name=Wallet&type=cash&currency=USD"))
	mk.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recAcct, withCookie(mk, cookie))
	if recAcct.Code != http.StatusSeeOther {
		t.Fatalf("create account = %d, want 303", recAcct.Code)
	}

	get := func() string {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, withCookie(httptest.NewRequest(http.MethodGet, "/accounts/1", nil), cookie))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /accounts/1 = %d, want 200", rec.Code)
		}
		return rec.Body.String()
	}
	post := func(path, body string, want int) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		router.ServeHTTP(rec, withCookie(req, cookie))
		if rec.Code != want {
			t.Fatalf("POST %s = %d, want %d", path, rec.Code, want)
		}
	}

	// Empty register, zero balance.
	if body := get(); !strings.Contains(body, "Adicionar transação") || !strings.Contains(body, "0,00 USD") {
		t.Errorf("fresh detail page missing add form or zero balance")
	}

	// Income 100 (tx id 1), expense 30 (tx id 2) -> balance 70.
	post("/accounts/1/transaction", "type=income&amount=100&date=2024-01-05&description=salary", http.StatusSeeOther)
	post("/accounts/1/transaction", "type=expense&amount=30&date=2024-01-06&description=food", http.StatusSeeOther)
	body := get()
	for _, want := range []string{"+100,00 USD", "-30,00 USD", "70,00 USD", "salary", "food"} {
		if !strings.Contains(body, want) {
			t.Errorf("register missing %q", want)
		}
	}

	// Edit the expense (tx 2) 30 -> 50 -> balance 50.
	post("/accounts/1/transaction/edit", "tx_id=2&type=expense&amount=50&date=2024-01-06&description=food", http.StatusSeeOther)
	if body := get(); !strings.Contains(body, "50,00 USD") {
		t.Errorf("balance after edit should be 50.0000 USD")
	}

	// Delete the income (tx 1) -> balance -50.
	post("/accounts/1/transaction/delete", "tx_id=1", http.StatusSeeOther)
	if body := get(); !strings.Contains(body, "-50,00 USD") {
		t.Errorf("balance after deleting income should be -50.0000 USD")
	}

	// Invalid amount is rejected without crashing.
	post("/accounts/1/transaction", "type=income&amount=abc&date=2024-01-07", http.StatusBadRequest)
}

func TestCreditAccountShowsBalanceOwed(t *testing.T) {
	router := NewRouter(testDeps(true, nil))
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	// Create a credit USD account (id 1 in the stub).
	recAcct := httptest.NewRecorder()
	mk := httptest.NewRequest(http.MethodPost, "/accounts", strings.NewReader("name=Card&type=credit&currency=USD"))
	mk.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recAcct, withCookie(mk, cookie))
	if recAcct.Code != http.StatusSeeOther {
		t.Fatalf("create credit account = %d, want 303", recAcct.Code)
	}

	// Two expenses (500 + 30) -> owed 530. The 530 total appears only in the
	// balance area, so it cleanly proves the positive-liability presentation
	// (the individual rows render signed -500 / -30, which is correct).
	for _, amt := range []string{"500", "30"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/accounts/1/transaction", strings.NewReader("type=expense&amount="+amt+"&date=2024-03-01&description=buy"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		router.ServeHTTP(rec, withCookie(req, cookie))
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("credit expense %s = %d, want 303", amt, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withCookie(httptest.NewRequest(http.MethodGet, "/accounts/1", nil), cookie))
	body := rec.Body.String()
	if !strings.Contains(body, "Saldo devedor") {
		t.Errorf("credit detail should label the balance 'Balance owed'")
	}
	if !strings.Contains(body, "530,00 USD") {
		t.Errorf("credit detail should show the positive amount owed (530.0000 USD)")
	}
}

func TestTransferMovesBothBalances(t *testing.T) {
	router := NewRouter(testDeps(true, nil))
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	post := func(path, body string, want int) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		router.ServeHTTP(rec, withCookie(req, cookie))
		if rec.Code != want {
			t.Fatalf("POST %s = %d, want %d", path, rec.Code, want)
		}
	}
	bodyOf := func(path string) string {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, withCookie(httptest.NewRequest(http.MethodGet, path, nil), cookie))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, rec.Code)
		}
		return rec.Body.String()
	}

	// Two cash USD accounts (ids 1 and 2).
	post("/accounts", "name=Checking&type=cash&currency=USD", http.StatusSeeOther)
	post("/accounts", "name=Savings&type=cash&currency=USD", http.StatusSeeOther)

	// Transfer 200 from account 1 to account 2.
	post("/accounts/1/transfer", "to_account_id=2&from_amount=200&date=2024-05-01&description=move", http.StatusSeeOther)

	// Source shows -200 (one row, no double-count); destination shows +200.
	src := bodyOf("/accounts/1")
	if !strings.Contains(src, "-200,00 USD") {
		t.Errorf("source detail should reflect the outgoing -200.0000 USD")
	}
	if !strings.Contains(src, "transfer") {
		t.Errorf("source register should list a transfer row")
	}
	dst := bodyOf("/accounts/2")
	if !strings.Contains(dst, "+200,00 USD") {
		t.Errorf("destination register should show the incoming +200.0000 USD")
	}

	// The transfer row has no Edit control (corrected via delete + recreate).
	if strings.Contains(dst, "?edit=") {
		t.Errorf("transfer rows must not offer an Edit link")
	}

	// A same-account transfer is rejected without crashing.
	post("/accounts/1/transfer", "to_account_id=1&from_amount=10&date=2024-05-02", http.StatusBadRequest)
}

func TestCategoriesPageAndGuardedDelete(t *testing.T) {
	deps := testDeps(true, nil)
	cats := &stubCategories{usage: map[int64]int64{}}
	deps.Categories = cats
	router := NewRouter(deps)

	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	post := func(path, body string, want int) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		router.ServeHTTP(rec, withCookie(req, cookie))
		if rec.Code != want {
			t.Fatalf("POST %s = %d, want %d", path, rec.Code, want)
		}
	}
	body := func(path string) string {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, withCookie(httptest.NewRequest(http.MethodGet, path, nil), cookie))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, rec.Code)
		}
		return rec.Body.String()
	}

	// Auth gate.
	recUnauth := httptest.NewRecorder()
	NewRouter(testDeps(false, nil)).ServeHTTP(recUnauth, httptest.NewRequest(http.MethodGet, "/categories", nil))
	if recUnauth.Code != http.StatusSeeOther {
		t.Fatalf("unauth GET /categories = %d, want 303", recUnauth.Code)
	}

	// Create a category (becomes id 1) and see it listed.
	post("/categories", "name=Food&kind=expense", http.StatusSeeOther)
	if b := body("/categories"); !strings.Contains(b, "Food") || !strings.Contains(b, "expense") {
		t.Errorf("categories page missing the created category")
	}

	// Mark it in use: a plain delete is refused (400), force succeeds.
	cats.usage[1] = 2
	post("/categories/delete", "id=1", http.StatusBadRequest)
	post("/categories/delete", "id=1&force=true", http.StatusSeeOther)
	if b := body("/categories"); strings.Contains(b, "Food") {
		t.Errorf("category should be gone after force delete")
	}
}

func TestSecuritiesPage(t *testing.T) {
	deps := testDeps(true, nil)
	deps.Securities = &stubSecurities{}
	router := NewRouter(deps)

	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	post := func(path, body string, want int) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		router.ServeHTTP(rec, withCookie(req, cookie))
		if rec.Code != want {
			t.Fatalf("POST %s = %d, want %d", path, rec.Code, want)
		}
	}
	body := func(path string) string {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, withCookie(httptest.NewRequest(http.MethodGet, path, nil), cookie))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, rec.Code)
		}
		return rec.Body.String()
	}

	// Auth gate.
	recUnauth := httptest.NewRecorder()
	NewRouter(testDeps(false, nil)).ServeHTTP(recUnauth, httptest.NewRequest(http.MethodGet, "/securities", nil))
	if recUnauth.Code != http.StatusSeeOther {
		t.Fatalf("unauth GET /securities = %d, want 303", recUnauth.Code)
	}

	// Create a security and see its row listed. Assert on the upper-cased symbol
	// and the unique name — NOT the bare "ETF" label, which always appears in the
	// type <select> and would make that check vacuous.
	post("/securities", "symbol=voo&name=Vanguard+500+Index&type=etf&quote_currency=USD", http.StatusSeeOther)
	if b := body("/securities"); !strings.Contains(b, "VOO") || !strings.Contains(b, "Vanguard 500 Index") {
		t.Errorf("securities page missing the created security row")
	}

	// Duplicate symbol (case-insensitive) is rejected and adds no second row.
	post("/securities", "symbol=Voo&name=Dup&type=stock&quote_currency=USD", http.StatusBadRequest)
	if b := body("/securities"); strings.Count(b, "VOO") != 1 {
		t.Errorf("duplicate symbol should not add a second row")
	}

	// Unsupported currency is rejected AND the row is not persisted.
	post("/securities", "symbol=PETR4&name=Petrobras&type=stock&quote_currency=EUR", http.StatusBadRequest)
	if b := body("/securities"); strings.Contains(b, "PETR4") {
		t.Errorf("a security with an unsupported currency must not be persisted")
	}
}

func TestInvestmentAccountDetail(t *testing.T) {
	router := NewRouter(testDeps(true, nil))
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	post := func(path, body string, want int) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		router.ServeHTTP(rec, withCookie(req, cookie))
		if rec.Code != want {
			t.Fatalf("POST %s = %d, want %d", path, rec.Code, want)
		}
	}
	body := func(path string) string {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, withCookie(httptest.NewRequest(http.MethodGet, path, nil), cookie))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, rec.Code)
		}
		return rec.Body.String()
	}

	// An investment account (id 1) renders the holdings/trade UI, not the
	// income/expense form.
	post("/accounts", "name=Broker&type=investment&currency=USD", http.StatusSeeOther)
	if b := body("/accounts/1"); !strings.Contains(b, "Posições") || !strings.Contains(b, "Saldo em caixa") {
		t.Errorf("investment detail missing holdings/cash sections")
	}

	// Buy 10 @ 5 fee 0 → holding shows; cash goes negative by the cost (50).
	post("/accounts/1/buy", "security_id=1&quantity=10&price=5&fees=0&date=2026-06-01", http.StatusSeeOther)
	if b := body("/accounts/1"); !strings.Contains(b, "S1") {
		t.Errorf("holdings table missing the bought security")
	}
	if b := body("/accounts/1"); !strings.Contains(b, "-50,00 USD") {
		t.Errorf("cash balance should be -50.0000 USD after the buy")
	}

	// Sell 4 @ 6 → ok. Oversell 999 → rejected (400).
	post("/accounts/1/sell", "security_id=1&quantity=4&price=6&fees=0&date=2026-06-02", http.StatusSeeOther)
	post("/accounts/1/sell", "security_id=1&quantity=999&price=6&fees=0&date=2026-06-03", http.StatusBadRequest)

	// Dividend credits cash; holding unchanged (still S1 listed).
	post("/accounts/1/dividend", "security_id=1&amount=12,50&date=2026-06-04", http.StatusSeeOther)
	if b := body("/accounts/1"); !strings.Contains(b, "S1") {
		t.Errorf("holding should remain after dividend")
	}
}

func TestPricesRequiresAuth(t *testing.T) {
	rec := httptest.NewRecorder()
	NewRouter(testDeps(false, nil)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/prices", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("unauth GET /prices = %d -> %q, want 303 -> /login", rec.Code, rec.Header().Get("Location"))
	}
}

func TestPricesAddAndList(t *testing.T) {
	router := NewRouter(testDeps(true, nil))
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	// Seed a security so the add form renders (it is hidden when none exist).
	recSec := httptest.NewRecorder()
	sec := httptest.NewRequest(http.MethodPost, "/securities", strings.NewReader("symbol=petr4&name=Petrobras&type=stock&quote_currency=BRL"))
	sec.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recSec, withCookie(sec, cookie))

	// GET shows the prices page + the add form.
	recGet := httptest.NewRecorder()
	router.ServeHTTP(recGet, withCookie(httptest.NewRequest(http.MethodGet, "/prices", nil), cookie))
	if recGet.Code != http.StatusOK || !strings.Contains(recGet.Body.String(), "Preços dos ativos") {
		t.Fatalf("GET /prices = %d, missing heading", recGet.Code)
	}

	// POST a valid price redirects, and it then appears in the list.
	recAdd := httptest.NewRecorder()
	add := httptest.NewRequest(http.MethodPost, "/prices", strings.NewReader("security_id=1&effective_date=2024-06-01&price=16,00"))
	add.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recAdd, withCookie(add, cookie))
	if recAdd.Code != http.StatusSeeOther {
		t.Fatalf("POST valid price = %d, want 303", recAdd.Code)
	}
	recList := httptest.NewRecorder()
	router.ServeHTTP(recList, withCookie(httptest.NewRequest(http.MethodGet, "/prices", nil), cookie))
	body := recList.Body.String()
	for _, want := range []string{"01/06/2024", "16,00 BRL"} {
		if !strings.Contains(body, want) {
			t.Errorf("prices list missing %q", want)
		}
	}

	// A non-positive price is rejected without crashing.
	recBad := httptest.NewRecorder()
	bad := httptest.NewRequest(http.MethodPost, "/prices", strings.NewReader("security_id=1&effective_date=2024-06-01&price=0"))
	bad.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recBad, withCookie(bad, cookie))
	if recBad.Code != http.StatusBadRequest {
		t.Fatalf("POST non-positive price = %d, want 400", recBad.Code)
	}
}

// TestHoldingValuationColumns proves the investment-detail holdings table shows
// market value + unrealized G/L once a price exists, and "—" when it does not.
func TestHoldingValuationColumns(t *testing.T) {
	txs := &stubTransactions{}
	deps := Deps{
		Sessions:      scs.New(),
		Auth:          stubAuth{ok: true},
		Settings:      &stubSettings{},
		ExchangeRates: &stubExchangeRates{},
		Prices:        &stubPrices{},
		Accounts:      &stubAccounts{},
		Transactions:  txs,
		Categories:    &stubCategories{usage: map[int64]int64{}},
		CategoryRules: &stubCategoryRules{},
		Budgets:       &stubBudgets{},
		Analytics:     &stubAnalytics{report: cannedAnalytics()},
		Securities:    &stubSecurities{},
		Imports:       &stubImports{},
		OwnerName:     "TestOwner",
	}
	router := NewRouter(deps)
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	post := func(path, body string, want int) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		router.ServeHTTP(rec, withCookie(req, cookie))
		if rec.Code != want {
			t.Fatalf("POST %s = %d, want %d", path, rec.Code, want)
		}
	}
	get := func(path string) string {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, withCookie(httptest.NewRequest(http.MethodGet, path, nil), cookie))
		return rec.Body.String()
	}

	// Investment account (id 1), buy 100 @ 10 fee 5 → qty 100, basis 1005.
	post("/accounts", "name=Broker&type=investment&currency=USD", http.StatusSeeOther)
	post("/accounts/1/buy", "security_id=1&quantity=100&price=10&fees=5&date=2026-06-01", http.StatusSeeOther)

	// No price yet → the price-dependent cells render the muted "—" placeholder
	// (assert the specific cell markup, not just any em dash on the page).
	if b := get("/accounts/1"); !strings.Contains(b, `text-muted">—`) {
		t.Errorf("holding with no price should render muted em-dash cells")
	}
	if b := get("/accounts/1"); strings.Contains(b, "1600.0000") {
		t.Errorf("no market value should be shown before a price exists")
	}

	// Set a price on the held position, then it re-values on read: market value
	// 100×16 = 1600, unrealized 1600 − 1005 = 595.
	txs.hold(1).price = decimal.RequireFromString("16")
	b := get("/accounts/1")
	if !strings.Contains(b, "1.600,00 USD") {
		t.Errorf("market value 1600.0000 USD missing after price set")
	}
	if !strings.Contains(b, "595,00 USD") {
		t.Errorf("unrealized gain 595.0000 USD missing after price set")
	}
	if !strings.Contains(b, "em 01/06/2026") {
		t.Errorf("price effective date (staleness) missing")
	}
}

func TestTransactionsRegister(t *testing.T) {
	router := NewRouter(testDeps(true, nil))
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	post := func(path, body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		router.ServeHTTP(rec, withCookie(req, cookie))
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("POST %s = %d, want 303", path, rec.Code)
		}
	}

	// Auth gate.
	recUnauth := httptest.NewRecorder()
	NewRouter(testDeps(false, nil)).ServeHTTP(recUnauth, httptest.NewRequest(http.MethodGet, "/transactions", nil))
	if recUnauth.Code != http.StatusSeeOther || recUnauth.Header().Get("Location") != "/login" {
		t.Fatalf("unauth GET /transactions = %d -> %q, want 303 -> /login", recUnauth.Code, recUnauth.Header().Get("Location"))
	}

	// An account with an income and an expense.
	post("/accounts", "name=Acc&type=cash&currency=USD")
	post("/accounts/1/transaction", "type=income&amount=100&date=2024-08-01&description=wage")
	post("/accounts/1/transaction", "type=expense&amount=40&date=2024-08-02&description=food")

	// Full page: filter form + both rows.
	recFull := httptest.NewRecorder()
	router.ServeHTTP(recFull, withCookie(httptest.NewRequest(http.MethodGet, "/transactions", nil), cookie))
	full := recFull.Body.String()
	for _, want := range []string{"Todas as contas", "Todos os tipos", "wage", "food", "<!doctype html>", "htmx.min.js"} {
		if !strings.Contains(strings.ToLower(full), strings.ToLower(want)) {
			t.Errorf("full register page missing %q", want)
		}
	}

	// HTMX request returns ONLY the rows partial (no shell/doctype).
	recHX := httptest.NewRecorder()
	hxReq := httptest.NewRequest(http.MethodGet, "/transactions", nil)
	hxReq.Header.Set("HX-Request", "true")
	router.ServeHTTP(recHX, withCookie(hxReq, cookie))
	hx := recHX.Body.String()
	if strings.Contains(strings.ToLower(hx), "<!doctype") || strings.Contains(hx, "Bem-vindo(a) de volta") {
		t.Errorf("HTMX response should be a bare partial, got shell markup")
	}
	if !strings.Contains(hx, "wage") || !strings.Contains(hx, "food") {
		t.Errorf("HTMX partial should contain the rows")
	}

	// Type filter narrows to income only.
	recFil := httptest.NewRecorder()
	fil := httptest.NewRequest(http.MethodGet, "/transactions?type=income", nil)
	fil.Header.Set("HX-Request", "true")
	router.ServeHTTP(recFil, withCookie(fil, cookie))
	body := recFil.Body.String()
	if !strings.Contains(body, "wage") || strings.Contains(body, "food") {
		t.Errorf("type=income filter should show wage and hide food; got %q", body)
	}
}

func TestImportPreviewAndCommit(t *testing.T) {
	deps := testDeps(true, nil)
	imp := &stubImports{}
	deps.Imports = imp
	router := NewRouter(deps)

	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	// Need an account (id 1) so renderImport's Accounts.Get succeeds.
	recAcc := httptest.NewRecorder()
	mk := httptest.NewRequest(http.MethodPost, "/accounts", strings.NewReader("name=Imp&type=cash&currency=USD"))
	mk.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recAcc, withCookie(mk, cookie))

	// Auth gate.
	recUnauth := httptest.NewRecorder()
	NewRouter(testDeps(false, nil)).ServeHTTP(recUnauth, httptest.NewRequest(http.MethodGet, "/accounts/1/import", nil))
	if recUnauth.Code != http.StatusSeeOther {
		t.Fatalf("unauth import = %d, want 303", recUnauth.Code)
	}

	// Import form renders.
	recForm := httptest.NewRecorder()
	router.ServeHTTP(recForm, withCookie(httptest.NewRequest(http.MethodGet, "/accounts/1/import", nil), cookie))
	if recForm.Code != http.StatusOK || !strings.Contains(recForm.Body.String(), "Importar transações") {
		t.Fatalf("import form = %d, missing heading", recForm.Code)
	}

	content := "15/03/2024\tSalary\t5.000,00\n31/02/24\tBad\t10,00\n" // 1 valid + 1 error
	body := url.Values{"content": {content}}.Encode()

	// Preview shows a new row, an error row, and a commit button.
	recPrev := httptest.NewRecorder()
	prev := httptest.NewRequest(http.MethodPost, "/accounts/1/import/preview", strings.NewReader(body))
	prev.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recPrev, withCookie(prev, cookie))
	pb := recPrev.Body.String()
	for _, want := range []string{"Salary", "+5.000,00 USD", "erro:", "Confirmar 1 novas linhas"} {
		if !strings.Contains(pb, want) {
			t.Errorf("preview missing %q", want)
		}
	}

	// Commit records the content and redirects to the account detail.
	recCommit := httptest.NewRecorder()
	commit := httptest.NewRequest(http.MethodPost, "/accounts/1/import/commit", strings.NewReader(body))
	commit.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recCommit, withCookie(commit, cookie))
	if recCommit.Code != http.StatusSeeOther || recCommit.Header().Get("Location") != "/accounts/1" {
		t.Fatalf("commit = %d -> %q, want 303 -> /accounts/1", recCommit.Code, recCommit.Header().Get("Location"))
	}
	if len(imp.committed) != 1 || imp.committed[0] != content {
		t.Errorf("commit should have recorded the content; got %v", imp.committed)
	}
}

// TestImportOFXFormat covers the OFX branch: format=ofx routes to PreviewOFX/
// CommitOFX, the preview surfaces the no-FITID warning, and commit records the
// content on the OFX side (not the tab side).
func TestImportOFXFormat(t *testing.T) {
	deps := testDeps(true, nil)
	imp := &stubImports{}
	deps.Imports = imp
	router := NewRouter(deps)

	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	recAcc := httptest.NewRecorder()
	mk := httptest.NewRequest(http.MethodPost, "/accounts", strings.NewReader("name=Imp&type=cash&currency=USD"))
	mk.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recAcc, withCookie(mk, cookie))

	content := "<OFX><BANKTRANLIST>\n" +
		"<STMTTRN><DTPOSTED>20240301<TRNAMT>100.00<FITID>Z1<NAME>WithFitid</STMTTRN>\n" +
		"<STMTTRN><DTPOSTED>20240302<TRNAMT>-9.00<NAME>NoFitid</STMTTRN>\n" +
		"</BANKTRANLIST></OFX>\n"
	body := url.Values{"content": {content}, "format": {"ofx"}}.Encode()

	// Preview: both rows new, the no-FITID row shows the warning.
	recPrev := httptest.NewRecorder()
	prev := httptest.NewRequest(http.MethodPost, "/accounts/1/import/preview", strings.NewReader(body))
	prev.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recPrev, withCookie(prev, cookie))
	pb := recPrev.Body.String()
	for _, want := range []string{"WithFitid", "NoFitid", "sem FITID", "Confirmar 2 novas linhas"} {
		if !strings.Contains(pb, want) {
			t.Errorf("OFX preview missing %q", want)
		}
	}

	// Commit routes to the OFX side, not the tab side.
	recCommit := httptest.NewRecorder()
	commit := httptest.NewRequest(http.MethodPost, "/accounts/1/import/commit", strings.NewReader(body))
	commit.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recCommit, withCookie(commit, cookie))
	if recCommit.Code != http.StatusSeeOther || recCommit.Header().Get("Location") != "/accounts/1" {
		t.Fatalf("ofx commit = %d -> %q, want 303 -> /accounts/1", recCommit.Code, recCommit.Header().Get("Location"))
	}
	if len(imp.committedOFX) != 1 || imp.committedOFX[0] != content {
		t.Errorf("commit should have recorded OFX content; got %v", imp.committedOFX)
	}
	if len(imp.committed) != 0 {
		t.Errorf("OFX commit must not touch the tab path; got %v", imp.committed)
	}
}

// TestCategoryRulesPage covers the guarded rules management page (Story 7.2):
// auth gate, add renders the rule, delete removes it.
func TestCategoryRulesPage(t *testing.T) {
	deps := testDeps(true, nil)
	cats := &stubCategories{usage: map[int64]int64{}}
	_, _ = cats.Create(context.Background(), "Food", category.Expense)
	deps.Categories = cats
	rules := &stubCategoryRules{}
	deps.CategoryRules = rules
	router := NewRouter(deps)

	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	// Auth gate.
	recUnauth := httptest.NewRecorder()
	NewRouter(testDeps(false, nil)).ServeHTTP(recUnauth, httptest.NewRequest(http.MethodGet, "/categories/rules", nil))
	if recUnauth.Code != http.StatusSeeOther {
		t.Fatalf("unauth rules = %d, want 303", recUnauth.Code)
	}

	// Page renders with the category option.
	recForm := httptest.NewRecorder()
	router.ServeHTTP(recForm, withCookie(httptest.NewRequest(http.MethodGet, "/categories/rules", nil), cookie))
	if recForm.Code != http.StatusOK || !strings.Contains(recForm.Body.String(), "Regras de categorização automática") {
		t.Fatalf("rules page = %d, missing heading", recForm.Code)
	}

	// Add a rule.
	recAdd := httptest.NewRecorder()
	add := httptest.NewRequest(http.MethodPost, "/categories/rules", strings.NewReader("match_text=uber&category_id=1"))
	add.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recAdd, withCookie(add, cookie))
	if recAdd.Code != http.StatusSeeOther || len(rules.rules) != 1 || rules.rules[0].MatchText != "uber" {
		t.Fatalf("add rule = %d, rules = %+v", recAdd.Code, rules.rules)
	}

	// Delete it.
	recDel := httptest.NewRecorder()
	del := httptest.NewRequest(http.MethodPost, "/categories/rules/delete", strings.NewReader("id=1"))
	del.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recDel, withCookie(del, cookie))
	if recDel.Code != http.StatusSeeOther || len(rules.rules) != 0 {
		t.Fatalf("delete rule = %d, rules = %+v", recDel.Code, rules.rules)
	}
}

// TestBudgetsPage exercises the /budgets set/list/delete flow (Story 8.1): auth
// gate, page render with the category option, setting a target (formatted in the
// Display Currency), rejecting a non-positive amount, and removing a target.
func TestBudgetsPage(t *testing.T) {
	deps := testDeps(true, nil)
	deps.Settings = &stubSettings{current: money.BRL}
	cats := &stubCategories{usage: map[int64]int64{}}
	_, _ = cats.Create(context.Background(), "Food", category.Expense)
	deps.Categories = cats
	budgets := &stubBudgets{}
	deps.Budgets = budgets
	router := NewRouter(deps)

	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	// Auth gate.
	recUnauth := httptest.NewRecorder()
	NewRouter(testDeps(false, nil)).ServeHTTP(recUnauth, httptest.NewRequest(http.MethodGet, "/budgets", nil))
	if recUnauth.Code != http.StatusSeeOther {
		t.Fatalf("unauth budgets = %d, want 303", recUnauth.Code)
	}

	// Page renders with the heading and the planned/actual/remaining view columns.
	recForm := httptest.NewRecorder()
	router.ServeHTTP(recForm, withCookie(httptest.NewRequest(http.MethodGet, "/budgets", nil), cookie))
	body := recForm.Body.String()
	if recForm.Code != http.StatusOK || !strings.Contains(body, "Orçamento mensal") ||
		!strings.Contains(body, "Planejado") || !strings.Contains(body, "Realizado") || !strings.Contains(body, "Restante") {
		t.Fatalf("budgets page = %d, missing heading/columns", recForm.Code)
	}

	// Set a target using a Brazilian-format amount; the month is preserved on the
	// post-write redirect.
	recSet := httptest.NewRecorder()
	set := httptest.NewRequest(http.MethodPost, "/budgets", strings.NewReader("category_id=1&amount=1.234,56&month=2026-06"))
	set.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recSet, withCookie(set, cookie))
	if recSet.Code != http.StatusSeeOther || len(budgets.budgets) != 1 ||
		!budgets.budgets[0].Amount.Equal(decimal.RequireFromString("1234.56")) {
		t.Fatalf("set budget = %d, budgets = %+v", recSet.Code, budgets.budgets)
	}
	if loc := recSet.Header().Get("Location"); loc != "/budgets?month=2026-06" {
		t.Fatalf("set redirect = %q, want /budgets?month=2026-06", loc)
	}

	// The view renders the target formatted in the Display Currency, for the month
	// carried in the query string.
	recList := httptest.NewRecorder()
	router.ServeHTTP(recList, withCookie(httptest.NewRequest(http.MethodGet, "/budgets?month=2026-06", nil), cookie))
	if !strings.Contains(recList.Body.String(), "1.234,56 BRL") {
		t.Fatalf("budgets view missing formatted target:\n%s", recList.Body.String())
	}

	// A non-positive amount is rejected (400) and does not change the store.
	recBad := httptest.NewRecorder()
	bad := httptest.NewRequest(http.MethodPost, "/budgets", strings.NewReader("category_id=1&amount=0"))
	bad.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recBad, withCookie(bad, cookie))
	if recBad.Code != http.StatusBadRequest || !strings.Contains(recBad.Body.String(), "maior que zero") {
		t.Fatalf("set zero = %d, want 400 with message", recBad.Code)
	}

	// Delete it.
	recDel := httptest.NewRecorder()
	del := httptest.NewRequest(http.MethodPost, "/budgets/delete", strings.NewReader("category_id=1"))
	del.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recDel, withCookie(del, cookie))
	if recDel.Code != http.StatusSeeOther || len(budgets.budgets) != 0 {
		t.Fatalf("delete budget = %d, budgets = %+v", recDel.Code, budgets.budgets)
	}
}

// TestAnalyticsPage renders the spending & cash-flow view (Story 8.3): the
// breakdown, the monthly chart, and the months range toggle.
func TestAnalyticsPage(t *testing.T) {
	router := NewRouter(testDeps(true, nil))
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withCookie(httptest.NewRequest(http.MethodGet, "/analytics", nil), cookie))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("analytics = %d, want 200", rec.Code)
	}
	for _, want := range []string{"Análises", "Gastos por categoria", "Rent", "60%", "600,00 BRL", "Fluxo de caixa", "jun/26", "Receita", "Despesa", "12 meses"} {
		if !strings.Contains(body, want) {
			t.Fatalf("analytics page missing %q\n%s", want, body)
		}
	}
	// The default window is 12 months (aria-current marks it active).
	if !strings.Contains(body, `href="/analytics?months=12" class="rounded px-2 py-0.5 bg-accent/10 text-accent font-medium" aria-current="true"`) {
		t.Fatalf("12-month range not marked active:\n%s", body)
	}

	// Selecting 6 months marks that toggle active instead.
	rec6 := httptest.NewRecorder()
	router.ServeHTTP(rec6, withCookie(httptest.NewRequest(http.MethodGet, "/analytics?months=6", nil), cookie))
	if !strings.Contains(rec6.Body.String(), `href="/analytics?months=6" class="rounded px-2 py-0.5 bg-accent/10 text-accent font-medium" aria-current="true"`) {
		t.Fatalf("6-month range not marked active:\n%s", rec6.Body.String())
	}
}

// TestImportCategorySelect verifies the preview renders a per-row category select
// and that committing forwards the chosen category to the service (Story 7.2).
func TestImportCategorySelect(t *testing.T) {
	deps := testDeps(true, nil)
	cats := &stubCategories{usage: map[int64]int64{}}
	_, _ = cats.Create(context.Background(), "Food", category.Expense)
	deps.Categories = cats
	imp := &stubImports{}
	deps.Imports = imp
	router := NewRouter(deps)

	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	recAcc := httptest.NewRecorder()
	mk := httptest.NewRequest(http.MethodPost, "/accounts", strings.NewReader("name=Imp&type=cash&currency=USD"))
	mk.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recAcc, withCookie(mk, cookie))

	content := "01/03/2024\tGrocery\t-10,00\n" // one new expense row → Line 1
	body := url.Values{"content": {content}}.Encode()

	// Preview renders a category select for the new row, with the expense option.
	recPrev := httptest.NewRecorder()
	prev := httptest.NewRequest(http.MethodPost, "/accounts/1/import/preview", strings.NewReader(body))
	prev.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recPrev, withCookie(prev, cookie))
	pb := recPrev.Body.String()
	for _, want := range []string{`name="cat_1"`, "Food", "— sem categoria —"} {
		if !strings.Contains(pb, want) {
			t.Errorf("preview missing %q", want)
		}
	}

	// Commit forwards the chosen category (cat_1=1) to the service.
	commitBody := url.Values{"content": {content}, "cat_1": {"1"}}.Encode()
	recCommit := httptest.NewRecorder()
	commit := httptest.NewRequest(http.MethodPost, "/accounts/1/import/commit", strings.NewReader(commitBody))
	commit.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recCommit, withCookie(commit, cookie))
	if recCommit.Code != http.StatusSeeOther {
		t.Fatalf("commit = %d, want 303", recCommit.Code)
	}
	if imp.committedCats[1] != 1 {
		t.Errorf("commit forwarded cats = %+v; want {1:1}", imp.committedCats)
	}
}

func TestCSRFRejectsCrossOrigin(t *testing.T) {
	rec := httptest.NewRecorder()
	req := loginPost("owner", "right")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	NewRouter(testDeps(true, nil)).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin POST = %d, want 403", rec.Code)
	}
}

func loginPost(user, pass string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username="+user+"&password="+pass))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func withCookie(req *http.Request, cookie string) *http.Request {
	req.Header.Set("Cookie", cookie)
	return req
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == "session" {
			return c.Name + "=" + c.Value
		}
	}
	t.Fatal("no session cookie set on login")
	return ""
}

// --- Story 6.1: GET /export ---

func TestExportRequiresAuth(t *testing.T) {
	rec := httptest.NewRecorder()
	NewRouter(testDeps(true, nil)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/export", nil))
	// Unauthenticated requests to the protected group redirect to /login.
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("unauth GET /export = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Errorf("redirect = %q, want /login", loc)
	}
}

func TestExportDownloadsAuthoredJSON(t *testing.T) {
	deps := testDeps(true, nil)
	router := NewRouter(deps)
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withCookie(httptest.NewRequest(http.MethodGet, "/export", nil), cookie))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /export = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment; filename=") || !strings.Contains(cd, "financas-export-") || !strings.HasSuffix(cd, `.json"`) {
		t.Errorf("Content-Disposition = %q, want attachment financas-export-...json", cd)
	}

	var got backup.Export
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not valid Export JSON: %v", err)
	}
	if got.Schema != backup.ExportSchema || got.Version != backup.ExportVersion {
		t.Errorf("schema/version = %q/%d", got.Schema, got.Version)
	}
	if got.DisplayCurrency != "BRL" || len(got.Accounts) != 1 || got.Accounts[0].Name != "CashUSD" {
		t.Errorf("export body missing canned authored rows: %+v", got)
	}
	// Derived figures must never appear in the file.
	for _, banned := range []string{"net_worth", "networth", "holdings", "balance", "valuation", "gain_loss"} {
		if strings.Contains(strings.ToLower(rec.Body.String()), banned) {
			t.Errorf("export body unexpectedly contains derived key %q", banned)
		}
	}
}

func TestExportServiceErrorIs500(t *testing.T) {
	deps := testDeps(true, nil)
	deps.Backup = &stubBackup{err: errors.New("boom")}
	router := NewRouter(deps)
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withCookie(httptest.NewRequest(http.MethodGet, "/export", nil), cookie))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET /export with service error = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "\"schema\"") {
		t.Error("error response should not contain partial export JSON")
	}
}

func TestSettingsPageHasExportLink(t *testing.T) {
	body := authedGet(t, testDeps(true, nil), "/settings")
	if !strings.Contains(body, `href="/export"`) {
		t.Error("settings page missing /export download link")
	}
	if !strings.Contains(body, "Backup") {
		t.Error("settings page missing Backup section heading")
	}
}

// authedGet logs in and performs an authenticated GET, returning the body. It
// does not assert a status (callers that need 200 can check), mirroring
// dashboardBody but without the 200 requirement.
func authedGet(t *testing.T, deps Deps, path string) string {
	t.Helper()
	router := NewRouter(deps)
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withCookie(httptest.NewRequest(http.MethodGet, path, nil), cookie))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", path, rec.Code)
	}
	return rec.Body.String()
}

// --- Story 6.2: POST /restore ---

// multipartRestore builds a multipart/form-data POST body for /restore with an
// optional file part and an optional confirm checkbox.
func multipartRestore(t *testing.T, fileContent string, withFile, confirm bool) (*http.Request, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if withFile {
		fw, err := mw.CreateFormFile("file", "backup.json")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write([]byte(fileContent))
	}
	if confirm {
		_ = mw.WriteField("confirm", "on")
	}
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/restore", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req, mw.FormDataContentType()
}

func TestRestoreRequiresAuth(t *testing.T) {
	// An unauthenticated unsafe method gets 401 (requireAuth redirects GETs but
	// rejects non-GET rather than redirect a POST body).
	req, _ := multipartRestore(t, "{}", true, true)
	rec := httptest.NewRecorder()
	NewRouter(testDeps(true, nil)).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth POST /restore = %d, want 401", rec.Code)
	}
}

func TestRestoreUploadSuccess(t *testing.T) {
	deps := testDeps(true, nil)
	stub := &stubBackup{export: cannedExport(), restoreSummary: backup.RestoreSummary{Accounts: 1, Transactions: 1}}
	deps.Backup = stub
	router := NewRouter(deps)
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	payload := `{"schema":"financas.export","version":1,"display_currency":"USD"}`
	req, _ := multipartRestore(t, payload, true, true)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withCookie(req, cookie))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /restore = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/settings?restored=1" {
		t.Errorf("redirect = %q, want /settings?restored=1", loc)
	}
	if !stub.restoreCalled {
		t.Fatal("Restore was not called")
	}
	if string(stub.restoredBytes) != payload {
		t.Errorf("service got %q, want the uploaded file bytes %q", stub.restoredBytes, payload)
	}
}

func TestRestoreMissingConfirmRejected(t *testing.T) {
	deps := testDeps(true, nil)
	stub := &stubBackup{export: cannedExport()}
	deps.Backup = stub
	router := NewRouter(deps)
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	req, _ := multipartRestore(t, "{}", true, false) // file but no confirm
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withCookie(req, cookie))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("no-confirm POST /restore = %d, want 400", rec.Code)
	}
	if stub.restoreCalled {
		t.Error("Restore must not run without confirmation")
	}
	if !strings.Contains(rec.Body.String(), "Marque a caixa") {
		t.Error("expected a confirm-required message")
	}
}

func TestRestoreServiceErrorRendersReason(t *testing.T) {
	deps := testDeps(true, nil)
	deps.Backup = &stubBackup{export: cannedExport(), restoreErr: backup.ErrUnsupportedVersion}
	router := NewRouter(deps)
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	req, _ := multipartRestore(t, `{"schema":"financas.export","version":999}`, true, true)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withCookie(req, cookie))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("version-error POST /restore = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "versão incompatível") {
		t.Errorf("expected an incompatible-version message, body = %s", rec.Body.String())
	}
}

func TestSettingsShowsRestoreFormAndNotice(t *testing.T) {
	// The restore form is present on the settings page.
	body := authedGet(t, testDeps(true, nil), "/settings")
	for _, want := range []string{`action="/restore"`, `enctype="multipart/form-data"`, `name="confirm"`, `name="file"`, "substitui todos"} {
		if !strings.Contains(body, want) {
			t.Errorf("settings page missing restore-form bit %q", want)
		}
	}
	// The ?restored=1 success notice renders.
	notice := authedGet(t, testDeps(true, nil), "/settings?restored=1")
	if !strings.Contains(notice, "restaurados do backup") {
		t.Error("settings page missing the restored success notice")
	}
}

// --- Faxina: primary-load failures surface as HTTP 500 (not a misleading empty page) ---

// authedGetRaw logs in and performs an authenticated GET, returning the recorder
// without asserting a status (callers check the code themselves).
func authedGetRaw(t *testing.T, deps Deps, path string) *httptest.ResponseRecorder {
	t.Helper()
	router := NewRouter(deps)
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withCookie(httptest.NewRequest(http.MethodGet, path, nil), cookie))
	return rec
}

func TestPrimaryLoadFailureReturns500(t *testing.T) {
	boom := errors.New("db is down")
	cases := []struct {
		name string
		path string
		mut  func(d *Deps)
	}{
		{"accounts", "/accounts", func(d *Deps) { d.Accounts = &stubAccounts{listErr: boom} }},
		{"prices", "/prices", func(d *Deps) { d.Prices = &stubPrices{listErr: boom} }},
		{"exchange-rates", "/exchange-rates", func(d *Deps) { d.ExchangeRates = &stubExchangeRates{listErr: boom} }},
		{"categories", "/categories", func(d *Deps) { d.Categories = &stubCategories{usage: map[int64]int64{}, listErr: boom} }},
		{"securities", "/securities", func(d *Deps) { d.Securities = &stubSecurities{listErr: boom} }},
		{"transactions register", "/transactions", func(d *Deps) { d.Transactions = &stubTransactions{registerErr: boom} }},
		{"category summary", "/categories/1", func(d *Deps) {
			d.Categories = &stubCategories{usage: map[int64]int64{}, listErr: boom}
		}},
		{"account detail list", "/accounts/1", func(d *Deps) {
			d.Accounts = &stubAccounts{accts: []account.Account{{ID: 1, Name: "Carteira", Type: account.Cash, Currency: money.USD}}}
			d.Transactions = &stubTransactions{listErr: boom}
		}},
		{"investment detail list", "/accounts/1", func(d *Deps) {
			d.Accounts = &stubAccounts{accts: []account.Account{{ID: 1, Name: "Corretora", Type: account.Investment, Currency: money.USD}}}
			d.Transactions = &stubTransactions{listErr: boom}
		}},
		{"account detail Get outage (not a 404)", "/accounts/1", func(d *Deps) {
			d.Accounts = &stubAccounts{getErr: boom}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			deps := testDeps(true, nil)
			c.mut(&deps)
			rec := authedGetRaw(t, deps, c.path)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("%s with a load error = %d, want 500 (must not look like an empty 200)", c.path, rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "Não foi possível carregar") {
				t.Errorf("%s 500 body should carry the load-error banner, got: %s", c.path, rec.Body.String())
			}
		})
	}
}

// A secondary-load failure (a filter dropdown) must NOT fail the page — the
// register still renders 200 with its rows even if the accounts filter errors.
func TestSecondaryLoadFailureDegradesGracefully(t *testing.T) {
	deps := testDeps(true, nil)
	deps.Accounts = &stubAccounts{listErr: errors.New("dropdown down")} // filter dropdown source
	// Register itself succeeds (default stubTransactions), so the page must render.
	rec := authedGetRaw(t, deps, "/transactions")
	if rec.Code != http.StatusOK {
		t.Fatalf("register with only a failing filter dropdown = %d, want 200 (graceful degrade)", rec.Code)
	}
}

// A genuine 404 (unknown account) must stay a 404 — the load-error sweep must not
// turn missing resources into 500s.
func TestUnknownAccountStays404(t *testing.T) {
	deps := testDeps(true, nil) // empty stubAccounts → Get returns ErrNotFound
	rec := authedGetRaw(t, deps, "/accounts/999")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown account = %d, want 404", rec.Code)
	}
}

// --- Faxina: validation sentinels → pt-BR; unknown errors → generic (no raw leak) ---

func TestKnownErrMsgMapsSentinels(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{account.ErrEmptyName, "O nome da conta não pode ficar vazio."},
		{account.ErrUnsupportedCurrency, "Moeda não suportada."},
		{category.ErrCategoryInUse, "Esta categoria está em uso por transações."},
		{security.ErrDuplicateSymbol, "Já existe um ativo com esse código."},
		{exchangerate.ErrSameCurrency, "As moedas de origem e destino devem ser diferentes."},
		{transaction.ErrOversold, "A venda excede a quantidade em carteira."},
		{transaction.ErrTradeCurrencyMismatch, "A moeda de cotação do ativo deve ser igual à da conta."},
		{importer.ErrAccountNotFound, "Conta não encontrada."},
		{importer.ErrUnsupportedAccountType, "A importação exige uma conta de caixa ou crédito."},
		{fmt.Errorf("wrapped: %w", transaction.ErrNonPositiveAmount), "O valor deve ser positivo."},
	}
	for _, c := range cases {
		got, ok := knownErrMsg(c.err)
		if !ok || got != c.want {
			t.Errorf("knownErrMsg(%v) = (%q,%v), want (%q,true)", c.err, got, ok, c.want)
		}
	}
	// An unknown/infra error is not classified.
	if _, ok := knownErrMsg(errors.New("pq: connection refused")); ok {
		t.Error("an unknown error must not be classified as a known sentinel")
	}
}

func TestProblemMsgNeverLeaksRawError(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/accounts", nil)
	raw := errors.New("pq: duplicate key value violates unique constraint \"account_pkey\"")
	got := problemMsg(req, "Não foi possível criar a conta. Verifique os dados e tente novamente.", raw)
	if strings.Contains(got, "pq:") || strings.Contains(got, "constraint") {
		t.Errorf("problemMsg leaked the raw error: %q", got)
	}
	if got != "Não foi possível criar a conta. Verifique os dados e tente novamente." {
		t.Errorf("unknown error should yield the generic fallback, got %q", got)
	}
	// A known sentinel surfaces its specific pt-BR message instead of the fallback.
	if got := problemMsg(req, "fallback", account.ErrEmptyName); got != "O nome da conta não pode ficar vazio." {
		t.Errorf("known sentinel should map to its message, got %q", got)
	}
}

// At the handler level, a real validation sentinel surfaces its pt-BR reason.
func TestRateSameCurrencyShowsPtBRReason(t *testing.T) {
	deps := testDeps(true, nil)
	router := NewRouter(deps)
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, loginPost("owner", "right"))
	cookie := sessionCookie(t, recLogin)

	req := httptest.NewRequest(http.MethodPost, "/exchange-rates",
		strings.NewReader("from=USD&to=USD&effective_date=2024-01-01&rate=5"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	req.Host = "example.com"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withCookie(req, cookie))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("same-currency rate = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "moedas de origem e destino devem ser diferentes") {
		t.Errorf("expected the pt-BR same-currency reason, body: %s", rec.Body.String())
	}
}

// TestInvestmentDetailOversoldNotice (HTTP-level, LOW-3 from review): an oversold
// position renders a warning naming the symbol AND still shows the good holdings —
// a 200, never a whole-page block.
func TestInvestmentDetailOversoldNotice(t *testing.T) {
	deps := testDeps(true, nil)
	deps.Accounts = &stubAccounts{accts: []account.Account{
		{ID: 1, Name: "Broker", Type: account.Investment, Currency: money.USD},
	}}
	deps.Transactions = &stubTransactions{
		held: map[int64]*stubHolding{
			2: {qty: decimal.RequireFromString("4"), basis: decimal.RequireFromString("80"), price: decimal.RequireFromString("30")},
		},
		oversold: []string{"BADSYM"},
	}
	body := authedGet(t, deps, "/accounts/1")
	// The good holding still renders (S2), and the oversold symbol is named in a warning.
	for _, want := range []string{"S2", "BADSYM", "venda excede a quantidade mantida"} {
		if !strings.Contains(body, want) {
			t.Errorf("investment detail oversold view missing %q", want)
		}
	}
	// It must NOT hide the holdings table behind a whole-page block.
	if !strings.Contains(body, "Posições") {
		t.Error("holdings section should still render alongside the oversold warning")
	}
}
