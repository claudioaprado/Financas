package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/internal/money"
	"github.com/claudioaprado/financas/internal/service/account"
	"github.com/claudioaprado/financas/internal/service/category"
	"github.com/claudioaprado/financas/internal/service/exchangerate"
	"github.com/claudioaprado/financas/internal/service/importer"
	"github.com/claudioaprado/financas/internal/service/security"
	"github.com/claudioaprado/financas/internal/service/transaction"
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
type stubExchangeRates struct{ rates []exchangerate.Rate }

func (s *stubExchangeRates) Add(_ context.Context, from, to money.Currency, eff time.Time, rate decimal.Decimal) (exchangerate.Rate, error) {
	if from == to {
		return exchangerate.Rate{}, errors.New("same currency")
	}
	if !money.IsSupported(from) || !money.IsSupported(to) {
		return exchangerate.Rate{}, errors.New("unsupported")
	}
	if !rate.IsPositive() {
		return exchangerate.Rate{}, errors.New("non-positive")
	}
	r := exchangerate.Rate{ID: int64(len(s.rates) + 1), From: from, To: to, EffectiveDate: eff, Rate: rate}
	s.rates = append(s.rates, r)
	return r, nil
}

func (s *stubExchangeRates) List(context.Context) ([]exchangerate.Rate, error) { return s.rates, nil }

// stubAccounts is an in-memory Accounts for handler tests.
type stubAccounts struct {
	accts  []account.Account
	nextID int64
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
	for _, a := range s.accts {
		if a.ID == id {
			return a, nil
		}
	}
	return account.Account{}, account.ErrNotFound
}

func (s *stubAccounts) List(_ context.Context, includeArchived bool) ([]account.Account, error) {
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
	rows   []transaction.Transaction
	nextID int64
	held   map[int64]*stubHolding // by security id
}

type stubHolding struct {
	qty, basis, realized decimal.Decimal
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

func (s *stubTransactions) Holdings(_ context.Context, _ int64) ([]transaction.HoldingView, money.Money, error) {
	realized := decimal.Zero
	var out []transaction.HoldingView
	for id, h := range s.held {
		realized = realized.Add(h.realized)
		if !h.qty.IsPositive() {
			continue
		}
		out = append(out, transaction.HoldingView{
			SecurityID:   id,
			Symbol:       fmt.Sprintf("S%d", id),
			Quantity:     h.qty,
			AvgCost:      money.New(h.basis.Div(h.qty), money.USD).Rounded(),
			CostBasis:    money.New(h.basis, money.USD),
			RealizedGain: money.New(h.realized, money.USD),
		})
	}
	return out, money.New(realized, money.USD), nil
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
	out := []transaction.Transaction{}
	for _, r := range s.rows {
		if r.AccountID == accountID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *stubTransactions) Register(_ context.Context, f transaction.RegisterFilter) ([]transaction.RegisterRow, error) {
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
	cats   []category.Category
	usage  map[int64]int64
	nextID int64
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

func (s *stubCategories) List(_ context.Context) ([]category.Category, error) { return s.cats, nil }

func (s *stubCategories) ListWithUsage(_ context.Context) ([]category.CategoryUsage, error) {
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
// importer.Parse and records committed content; every OK row counts as "new".
type stubImports struct{ committed []string }

func (s *stubImports) Preview(_ context.Context, _ int64, content string) (importer.Result, error) {
	return stubImportResult(content), nil
}

func (s *stubImports) Commit(_ context.Context, _ int64, content string) (importer.Result, error) {
	s.committed = append(s.committed, content)
	return stubImportResult(content), nil
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

// stubSecurities is an in-memory Securities for handler tests. It normalizes the
// symbol and rejects duplicates case-insensitively, mirroring the real service.
type stubSecurities struct {
	secs   []security.Security
	nextID int64
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

func (s *stubSecurities) List(_ context.Context) ([]security.Security, error) { return s.secs, nil }

// testDeps builds Deps with a fresh in-memory session manager (so each router
// instance has an isolated store) and stubs for the services.
func testDeps(authOK bool, ready ReadyCheck) Deps {
	return Deps{
		Sessions:      scs.New(),
		Auth:          stubAuth{ok: authOK},
		Ready:         ready,
		Settings:      &stubSettings{},
		ExchangeRates: &stubExchangeRates{},
		Accounts:      &stubAccounts{},
		Transactions:  &stubTransactions{},
		Categories:    &stubCategories{usage: map[int64]int64{}},
		Securities:    &stubSecurities{},
		Imports:       &stubImports{},
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
	for _, want := range []string{"Welcome back", "TestOwner", "Dashboard", "Investments", "Transactions", "Accounts", "Analytics", "/logout"} {
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
	if body := rec.Body.String(); !strings.Contains(body, "Investments") || !strings.Contains(body, "Coming soon") {
		t.Errorf("/investments page missing expected content")
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
	for _, want := range []string{"Display currency", "USD", "BRL"} {
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
	if recGet.Code != http.StatusOK || !strings.Contains(recGet.Body.String(), "Exchange rates") {
		t.Fatalf("GET /exchange-rates = %d, missing heading", recGet.Code)
	}

	// POST a valid rate redirects, and it then appears in the list.
	recAdd := httptest.NewRecorder()
	add := httptest.NewRequest(http.MethodPost, "/exchange-rates", strings.NewReader("from=USD&to=BRL&effective_date=2024-01-01&rate=5.25"))
	add.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recAdd, withCookie(add, cookie))
	if recAdd.Code != http.StatusSeeOther {
		t.Fatalf("POST valid rate = %d, want 303", recAdd.Code)
	}
	recList := httptest.NewRecorder()
	router.ServeHTTP(recList, withCookie(httptest.NewRequest(http.MethodGet, "/exchange-rates", nil), cookie))
	body := recList.Body.String()
	for _, want := range []string{"USD", "BRL", "2024-01-01", "5.25"} {
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
	if recGet.Code != http.StatusOK || !strings.Contains(recGet.Body.String(), "Create account") {
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
	if body := recList.Body.String(); !strings.Contains(body, "Checking") || !strings.Contains(body, "Cash balance") {
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
	if body := get(); !strings.Contains(body, "Add transaction") || !strings.Contains(body, "0.0000 USD") {
		t.Errorf("fresh detail page missing add form or zero balance")
	}

	// Income 100 (tx id 1), expense 30 (tx id 2) -> balance 70.
	post("/accounts/1/transaction", "type=income&amount=100&date=2024-01-05&description=salary", http.StatusSeeOther)
	post("/accounts/1/transaction", "type=expense&amount=30&date=2024-01-06&description=food", http.StatusSeeOther)
	body := get()
	for _, want := range []string{"+100.0000 USD", "-30.0000 USD", "70.0000 USD", "salary", "food"} {
		if !strings.Contains(body, want) {
			t.Errorf("register missing %q", want)
		}
	}

	// Edit the expense (tx 2) 30 -> 50 -> balance 50.
	post("/accounts/1/transaction/edit", "tx_id=2&type=expense&amount=50&date=2024-01-06&description=food", http.StatusSeeOther)
	if body := get(); !strings.Contains(body, "50.0000 USD") {
		t.Errorf("balance after edit should be 50.0000 USD")
	}

	// Delete the income (tx 1) -> balance -50.
	post("/accounts/1/transaction/delete", "tx_id=1", http.StatusSeeOther)
	if body := get(); !strings.Contains(body, "-50.0000 USD") {
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
	if !strings.Contains(body, "Balance owed") {
		t.Errorf("credit detail should label the balance 'Balance owed'")
	}
	if !strings.Contains(body, "530.0000 USD") {
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
	if !strings.Contains(src, "-200.0000 USD") {
		t.Errorf("source detail should reflect the outgoing -200.0000 USD")
	}
	if !strings.Contains(src, "transfer") {
		t.Errorf("source register should list a transfer row")
	}
	dst := bodyOf("/accounts/2")
	if !strings.Contains(dst, "+200.0000 USD") {
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
	if b := body("/accounts/1"); !strings.Contains(b, "Holdings") || !strings.Contains(b, "Cash balance") {
		t.Errorf("investment detail missing holdings/cash sections")
	}

	// Buy 10 @ 5 fee 0 → holding shows; cash goes negative.
	post("/accounts/1/buy", "security_id=1&quantity=10&price=5&fees=0&date=2026-06-01", http.StatusSeeOther)
	if b := body("/accounts/1"); !strings.Contains(b, "S1") {
		t.Errorf("holdings table missing the bought security")
	}

	// Sell 4 @ 6 → ok. Oversell 999 → rejected (400).
	post("/accounts/1/sell", "security_id=1&quantity=4&price=6&fees=0&date=2026-06-02", http.StatusSeeOther)
	post("/accounts/1/sell", "security_id=1&quantity=999&price=6&fees=0&date=2026-06-03", http.StatusBadRequest)

	// Dividend credits cash; holding unchanged (still S1 listed).
	post("/accounts/1/dividend", "security_id=1&amount=12.50&date=2026-06-04", http.StatusSeeOther)
	if b := body("/accounts/1"); !strings.Contains(b, "S1") {
		t.Errorf("holding should remain after dividend")
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
	for _, want := range []string{"All accounts", "All types", "wage", "food", "<!doctype html>", "htmx.min.js"} {
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
	if strings.Contains(strings.ToLower(hx), "<!doctype") || strings.Contains(hx, "Welcome back") {
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
	if recForm.Code != http.StatusOK || !strings.Contains(recForm.Body.String(), "Import transactions") {
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
	for _, want := range []string{"Salary", "+5000.0000 USD", "error:", "Commit 1 new rows"} {
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
