// Package http wires the chi router, middleware, and templ-rendered views.
// It is the outermost layer (AD-1): it translates HTTP <-> service calls and
// renders results. It performs no business logic, no SQL, and no financial
// math. It owns sessions, the login/logout flow, CSRF protection, and route
// authentication, depending on an Authenticator abstraction it defines.
package http

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/internal/domain"
	"github.com/claudioaprado/financas/internal/money"
	"github.com/claudioaprado/financas/internal/service/account"
	"github.com/claudioaprado/financas/internal/service/category"
	"github.com/claudioaprado/financas/internal/service/exchangerate"
	"github.com/claudioaprado/financas/internal/service/importer"
	"github.com/claudioaprado/financas/internal/service/price"
	"github.com/claudioaprado/financas/internal/service/security"
	"github.com/claudioaprado/financas/internal/service/transaction"
	"github.com/claudioaprado/financas/web"
)

// ReadyCheck reports whether a downstream dependency (the database) is reachable.
type ReadyCheck func(context.Context) error

// Authenticator verifies owner credentials. It is defined here (consumer side)
// and implemented by internal/service/auth, keeping this layer free of the
// credential mechanics (AD-1).
type Authenticator interface {
	Authenticate(ctx context.Context, username, password string) error
}

// Settings reads and updates application settings (the Display Currency).
// Defined here (consumer side) and implemented by service/settings.
type Settings interface {
	DisplayCurrency(ctx context.Context) (money.Currency, error)
	SetDisplayCurrency(ctx context.Context, c money.Currency) error
	ListCurrencies(ctx context.Context) ([]money.Currency, error)
}

// ExchangeRates appends and lists owner-entered exchange rates. Defined here
// (consumer side) and implemented by service/exchangerate.
type ExchangeRates interface {
	Add(ctx context.Context, from, to money.Currency, effective time.Time, rate decimal.Decimal) (exchangerate.Rate, error)
	List(ctx context.Context) ([]exchangerate.Rate, error)
}

// Prices appends and lists owner-entered, effective-dated security prices.
// Defined here (consumer side) and implemented by service/price.
type Prices interface {
	Add(ctx context.Context, securityID int64, effective time.Time, price decimal.Decimal) (price.Price, error)
	List(ctx context.Context) ([]price.Price, error)
}

// Accounts creates, lists, renames, and archives the owner's accounts. Defined
// here (consumer side) and implemented by service/account.
type Accounts interface {
	Create(ctx context.Context, name string, typ account.AccountType, currency money.Currency) (account.Account, error)
	Get(ctx context.Context, id int64) (account.Account, error)
	Rename(ctx context.Context, id int64, name string) error
	SetArchived(ctx context.Context, id int64, archived bool) error
	List(ctx context.Context, includeArchived bool) ([]account.Account, error)
}

// Transactions records, edits, deletes, lists, and derives balances for cash
// income/expense. Defined here (consumer side) and implemented by
// service/transaction.
type Transactions interface {
	Record(ctx context.Context, accountID int64, typ transaction.TxType, amount decimal.Decimal, date time.Time, description string, categoryID int64) (transaction.Transaction, error)
	Edit(ctx context.Context, accountID, txID int64, typ transaction.TxType, amount decimal.Decimal, date time.Time, description string, categoryID int64) error
	Delete(ctx context.Context, txID int64) error
	Transfer(ctx context.Context, fromID, toID int64, fromAmount, toAmount decimal.Decimal, date time.Time, description string) error
	Balance(ctx context.Context, accountID int64) (money.Money, error)
	List(ctx context.Context, accountID int64) ([]transaction.Transaction, error)
	CategoryTransactions(ctx context.Context, categoryID int64) ([]transaction.CategoryTxn, []money.Money, error)
	Register(ctx context.Context, f transaction.RegisterFilter) ([]transaction.RegisterRow, error)
	// Investment trades (Story 4.2).
	Buy(ctx context.Context, accountID, securityID int64, quantity, price, fees decimal.Decimal, date time.Time, description string) (transaction.Transaction, error)
	Sell(ctx context.Context, accountID, securityID int64, quantity, price, fees decimal.Decimal, date time.Time, description string) (transaction.Transaction, error)
	Dividend(ctx context.Context, accountID, securityID int64, amount decimal.Decimal, date time.Time, description string) (transaction.Transaction, error)
	Holdings(ctx context.Context, accountID int64) ([]transaction.HoldingView, money.Money, error)
}

// Categories creates, lists, and deletes income/expense categories. Defined here
// (consumer side) and implemented by service/category.
type Categories interface {
	Create(ctx context.Context, name string, kind category.Kind) (category.Category, error)
	List(ctx context.Context) ([]category.Category, error)
	ListWithUsage(ctx context.Context) ([]category.CategoryUsage, error)
	Delete(ctx context.Context, id int64, force bool) error
}

// Securities creates and lists the owner's securities. Defined here (consumer
// side) and implemented by service/security.
type Securities interface {
	Create(ctx context.Context, symbol, name string, typ security.SecurityType, quote money.Currency) (security.Security, error)
	List(ctx context.Context) ([]security.Security, error)
}

// Imports previews and commits tab-delimited file imports. Defined here
// (consumer side) and implemented by service/importer.
type Imports interface {
	Preview(ctx context.Context, accountID int64, content string) (importer.Result, error)
	Commit(ctx context.Context, accountID int64, content string) (importer.Result, error)
}

// Deps are the collaborators the router needs, injected by main.
type Deps struct {
	Sessions      *scs.SessionManager
	Auth          Authenticator
	Ready         ReadyCheck
	Settings      Settings
	ExchangeRates ExchangeRates
	Prices        Prices
	Accounts      Accounts
	Transactions  Transactions
	Categories    Categories
	Securities    Securities
	Imports       Imports
	OwnerName     string // shown in the shell greeting (from config)
}

// sessionAuthKey marks an authenticated session.
const sessionAuthKey = "authenticated"

// NewRouter builds the application's HTTP handler: CSRF protection, session
// load/save, the public health/readiness probes and login/logout flow, embedded
// static assets, and the authenticated area.
func NewRouter(deps Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Cross-origin (CSRF) protection for unsafe methods (Go 1.25+). Safe GET/HEAD
	// and same-origin form POSTs pass; cross-origin state changes are rejected.
	r.Use(http.NewCrossOriginProtection().Handler)

	// Load/save the session cookie for every request.
	r.Use(deps.Sessions.LoadAndSave)

	// --- Public routes ---
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	r.Get("/readyz", readyHandler(deps.Ready))
	r.Get("/login", loginForm(deps.Sessions))
	r.Post("/login", loginSubmit(deps))
	r.Post("/logout", logout(deps.Sessions))

	staticFS, err := fs.Sub(web.StaticFS, "static")
	if err != nil {
		// embed.FS layout is fixed at compile time; a failure here is a build bug.
		panic(err)
	}
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	// --- Authenticated area (every non-login route requires a session, AD-7) ---
	// The app shell + navigation (UX-DR1) wrap each page. Targets beyond the
	// dashboard are navigable placeholders until their epics build them.
	r.Group(func(pr chi.Router) {
		pr.Use(requireAuth(deps.Sessions))
		pr.Get("/", renderPage(deps, "dashboard", func(d web.ShellData) templ.Component { return web.DashboardPage(d) }))
		pr.Get("/investments", renderPage(deps, "investments", func(d web.ShellData) templ.Component { return web.ComingSoon(d, "Investments") }))
		pr.Get("/transactions", transactionsRegister(deps))
		pr.Get("/accounts", accountsForm(deps))
		pr.Post("/accounts", accountsCreate(deps))
		pr.Post("/accounts/rename", accountsRename(deps))
		pr.Post("/accounts/archive", accountsArchive(deps))
		pr.Get("/accounts/{id}", accountDetail(deps))
		pr.Post("/accounts/{id}/transaction", txCreate(deps))
		pr.Post("/accounts/{id}/transaction/edit", txEdit(deps))
		pr.Post("/accounts/{id}/transaction/delete", txDelete(deps))
		pr.Post("/accounts/{id}/transfer", txTransfer(deps))
		pr.Post("/accounts/{id}/buy", tradeBuy(deps))
		pr.Post("/accounts/{id}/sell", tradeSell(deps))
		pr.Post("/accounts/{id}/dividend", tradeDividend(deps))
		pr.Get("/accounts/{id}/import", importForm(deps))
		pr.Post("/accounts/{id}/import/preview", importPreview(deps))
		pr.Post("/accounts/{id}/import/commit", importCommit(deps))
		pr.Get("/categories", categoriesPage(deps))
		pr.Post("/categories", categoriesCreate(deps))
		pr.Post("/categories/delete", categoriesDelete(deps))
		pr.Get("/categories/{id}", categorySummary(deps))
		pr.Get("/securities", securitiesPage(deps))
		pr.Post("/securities", securitiesCreate(deps))
		pr.Get("/analytics", renderPage(deps, "analytics", func(d web.ShellData) templ.Component { return web.ComingSoon(d, "Analytics") }))
		pr.Get("/settings", settingsForm(deps))
		pr.Post("/settings", settingsSubmit(deps))
		pr.Get("/exchange-rates", exchangeRatesForm(deps))
		pr.Post("/exchange-rates", exchangeRatesSubmit(deps))
		pr.Get("/prices", pricesForm(deps))
		pr.Post("/prices", pricesSubmit(deps))
	})

	return r
}

func exchangeRatesForm(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		renderExchangeRates(deps, w, req, "", http.StatusOK)
	}
}

func exchangeRatesSubmit(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		from := money.Currency(req.PostFormValue("from"))
		to := money.Currency(req.PostFormValue("to"))
		effective, dErr := time.Parse("2006-01-02", req.PostFormValue("effective_date"))
		rate, rErr := decimal.NewFromString(req.PostFormValue("rate"))
		if dErr != nil || rErr != nil {
			renderExchangeRates(deps, w, req, "Enter a valid date and a decimal rate.", http.StatusBadRequest)
			return
		}
		if _, err := deps.ExchangeRates.Add(req.Context(), from, to, effective, rate); err != nil {
			renderExchangeRates(deps, w, req, "Could not add rate: "+err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, "/exchange-rates", http.StatusSeeOther)
	}
}

func renderExchangeRates(deps Deps, w http.ResponseWriter, req *http.Request, errMsg string, code int) {
	var rows []web.RateRow
	if rs, err := deps.ExchangeRates.List(req.Context()); err == nil {
		for _, r := range rs {
			rows = append(rows, web.RateRow{
				From:          string(r.From),
				To:            string(r.To),
				EffectiveDate: r.EffectiveDate.Format("2006-01-02"),
				Rate:          r.Rate.String(),
			})
		}
	}
	var codes []string
	for _, c := range money.Supported() {
		codes = append(codes, string(c))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_ = web.ExchangeRatesPage(shellData(deps, req.Context(), "settings"), rows, codes, errMsg).Render(req.Context(), w)
}

func pricesForm(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		renderPrices(deps, w, req, "", http.StatusOK)
	}
}

func pricesSubmit(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		securityID, idErr := strconv.ParseInt(req.PostFormValue("security_id"), 10, 64)
		effective, dErr := time.Parse("2006-01-02", req.PostFormValue("effective_date"))
		price, pErr := decimal.NewFromString(req.PostFormValue("price"))
		if idErr != nil || dErr != nil || pErr != nil {
			renderPrices(deps, w, req, "Choose a security and enter a valid date and a decimal price.", http.StatusBadRequest)
			return
		}
		if _, err := deps.Prices.Add(req.Context(), securityID, effective, price); err != nil {
			renderPrices(deps, w, req, "Could not add price: "+err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, "/prices", http.StatusSeeOther)
	}
}

func renderPrices(deps Deps, w http.ResponseWriter, req *http.Request, errMsg string, code int) {
	var rows []web.PriceRow
	if ps, err := deps.Prices.List(req.Context()); err == nil {
		for _, p := range ps {
			rows = append(rows, web.PriceRow{
				Symbol:        p.Symbol,
				EffectiveDate: p.EffectiveDate.Format("2006-01-02"),
				Price:         p.Price.String(),
			})
		}
	}
	// A price applies to any security — the select is NOT currency-filtered (unlike
	// the trade form). All securities are offered.
	var securities []web.SecurityChoice
	if secs, err := deps.Securities.List(req.Context()); err == nil {
		for _, s := range secs {
			securities = append(securities, web.SecurityChoice{ID: s.ID, Symbol: s.Symbol})
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_ = web.PricesPage(shellData(deps, req.Context(), "settings"), rows, securities, errMsg).Render(req.Context(), w)
}

func accountsForm(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		renderAccounts(deps, w, req, showArchived(req), "", http.StatusOK)
	}
}

func accountsCreate(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		name := req.PostFormValue("name")
		typ := account.AccountType(req.PostFormValue("type"))
		currency := money.Currency(req.PostFormValue("currency"))
		if _, err := deps.Accounts.Create(req.Context(), name, typ, currency); err != nil {
			renderAccounts(deps, w, req, false, "Could not create account: "+err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, "/accounts", http.StatusSeeOther)
	}
}

func accountsRename(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		id, err := strconv.ParseInt(req.PostFormValue("id"), 10, 64)
		if err != nil {
			renderAccounts(deps, w, req, showArchived(req), "Invalid account id.", http.StatusBadRequest)
			return
		}
		if err := deps.Accounts.Rename(req.Context(), id, req.PostFormValue("name")); err != nil {
			renderAccounts(deps, w, req, showArchived(req), "Could not rename account: "+err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, accountsRedirect(req), http.StatusSeeOther)
	}
}

func accountsArchive(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		id, err := strconv.ParseInt(req.PostFormValue("id"), 10, 64)
		if err != nil {
			renderAccounts(deps, w, req, showArchived(req), "Invalid account id.", http.StatusBadRequest)
			return
		}
		archived := req.PostFormValue("archived") == "true"
		if err := deps.Accounts.SetArchived(req.Context(), id, archived); err != nil {
			renderAccounts(deps, w, req, showArchived(req), "Could not update account: "+err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, accountsRedirect(req), http.StatusSeeOther)
	}
}

// showArchived reports whether the request asks to include archived accounts,
// reading "?show=archived" (GET) or a "show=archived" form field (POST).
func showArchived(req *http.Request) bool {
	return req.URL.Query().Get("show") == "archived" || req.PostFormValue("show") == "archived"
}

// accountsRedirect preserves the archived view across a redirect.
func accountsRedirect(req *http.Request) string {
	if showArchived(req) {
		return "/accounts?show=archived"
	}
	return "/accounts"
}

// balanceLabel names the balance an account type carries. It is presentation
// only (no financial math); the value is derived in later epics (AD-2, AD-10).
func balanceLabel(t account.AccountType) string {
	if t == account.Credit {
		return "Balance owed"
	}
	return "Cash balance"
}

func renderAccounts(deps Deps, w http.ResponseWriter, req *http.Request, includeArchived bool, errMsg string, code int) {
	var rows []web.AccountRow
	if accts, err := deps.Accounts.List(req.Context(), includeArchived); err == nil {
		for _, a := range accts {
			rows = append(rows, web.AccountRow{
				ID:           a.ID,
				Name:         a.Name,
				Type:         string(a.Type),
				Currency:     string(a.Currency),
				BalanceLabel: balanceLabel(a.Type),
				Archived:     a.Archived,
			})
		}
	}
	types := []string{string(account.Cash), string(account.Credit), string(account.Investment)}
	var codes []string
	for _, c := range money.Supported() {
		codes = append(codes, string(c))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_ = web.AccountsPage(shellData(deps, req.Context(), "accounts"), rows, types, codes, includeArchived, errMsg).Render(req.Context(), w)
}

func accountDetail(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		acctID, ok := parsePathID(req)
		if !ok {
			http.NotFound(w, req)
			return
		}
		editID, _ := strconv.ParseInt(req.URL.Query().Get("edit"), 10, 64) // 0 if absent/invalid
		renderAccountDetail(deps, w, req, acctID, editID, "", http.StatusOK)
	}
}

func txCreate(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		acctID, ok := parsePathID(req)
		if !ok {
			http.NotFound(w, req)
			return
		}
		typ, amount, date, desc, catID, ok := parseTxForm(req)
		if !ok {
			renderAccountDetail(deps, w, req, acctID, 0, "Enter a valid amount and date.", http.StatusBadRequest)
			return
		}
		if _, err := deps.Transactions.Record(req.Context(), acctID, typ, amount, date, desc, catID); err != nil {
			renderAccountDetail(deps, w, req, acctID, 0, "Could not add transaction: "+err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, accountPath(acctID), http.StatusSeeOther)
	}
}

func txEdit(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		acctID, ok := parsePathID(req)
		if !ok {
			http.NotFound(w, req)
			return
		}
		txID, err := strconv.ParseInt(req.PostFormValue("tx_id"), 10, 64)
		if err != nil {
			renderAccountDetail(deps, w, req, acctID, 0, "Invalid transaction id.", http.StatusBadRequest)
			return
		}
		typ, amount, date, desc, catID, ok := parseTxForm(req)
		if !ok {
			renderAccountDetail(deps, w, req, acctID, txID, "Enter a valid amount and date.", http.StatusBadRequest)
			return
		}
		if err := deps.Transactions.Edit(req.Context(), acctID, txID, typ, amount, date, desc, catID); err != nil {
			renderAccountDetail(deps, w, req, acctID, txID, "Could not save transaction: "+err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, accountPath(acctID), http.StatusSeeOther)
	}
}

func txDelete(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		acctID, ok := parsePathID(req)
		if !ok {
			http.NotFound(w, req)
			return
		}
		txID, err := strconv.ParseInt(req.PostFormValue("tx_id"), 10, 64)
		if err != nil {
			renderAccountDetail(deps, w, req, acctID, 0, "Invalid transaction id.", http.StatusBadRequest)
			return
		}
		if err := deps.Transactions.Delete(req.Context(), txID); err != nil {
			renderAccountDetail(deps, w, req, acctID, 0, "Could not delete transaction: "+err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, accountPath(acctID), http.StatusSeeOther)
	}
}

func txTransfer(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		acctID, ok := parsePathID(req)
		if !ok {
			http.NotFound(w, req)
			return
		}
		if err := req.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		toID, idErr := strconv.ParseInt(req.PostFormValue("to_account_id"), 10, 64)
		fromAmount, faErr := decimal.NewFromString(req.PostFormValue("from_amount"))
		date, dErr := time.Parse("2006-01-02", req.PostFormValue("date"))
		if idErr != nil || faErr != nil || dErr != nil {
			renderAccountDetail(deps, w, req, acctID, 0, "Enter a destination, a valid amount, and a date.", http.StatusBadRequest)
			return
		}
		// The received amount is optional (blank ⇒ same-currency); a non-empty
		// value must parse.
		toAmount := decimal.Zero
		if raw := strings.TrimSpace(req.PostFormValue("to_amount")); raw != "" {
			parsed, err := decimal.NewFromString(raw)
			if err != nil {
				renderAccountDetail(deps, w, req, acctID, 0, "Enter a valid received amount.", http.StatusBadRequest)
				return
			}
			toAmount = parsed
		}
		if err := deps.Transactions.Transfer(req.Context(), acctID, toID, fromAmount, toAmount, date, req.PostFormValue("description")); err != nil {
			renderAccountDetail(deps, w, req, acctID, 0, "Could not transfer: "+err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, accountPath(acctID), http.StatusSeeOther)
	}
}

func tradeBuy(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		acctID, ok := parsePathID(req)
		if !ok {
			http.NotFound(w, req)
			return
		}
		secID, qty, price, fees, date, ok := parseTradeForm(req)
		if !ok {
			renderAccountDetail(deps, w, req, acctID, 0, "Enter a security, a valid quantity, price, and date.", http.StatusBadRequest)
			return
		}
		if _, err := deps.Transactions.Buy(req.Context(), acctID, secID, qty, price, fees, date, req.PostFormValue("description")); err != nil {
			renderAccountDetail(deps, w, req, acctID, 0, "Could not record buy: "+err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, accountPath(acctID), http.StatusSeeOther)
	}
}

func tradeSell(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		acctID, ok := parsePathID(req)
		if !ok {
			http.NotFound(w, req)
			return
		}
		secID, qty, price, fees, date, ok := parseTradeForm(req)
		if !ok {
			renderAccountDetail(deps, w, req, acctID, 0, "Enter a security, a valid quantity, price, and date.", http.StatusBadRequest)
			return
		}
		if _, err := deps.Transactions.Sell(req.Context(), acctID, secID, qty, price, fees, date, req.PostFormValue("description")); err != nil {
			renderAccountDetail(deps, w, req, acctID, 0, "Could not record sell: "+err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, accountPath(acctID), http.StatusSeeOther)
	}
}

func tradeDividend(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		acctID, ok := parsePathID(req)
		if !ok {
			http.NotFound(w, req)
			return
		}
		if err := req.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		secID, sErr := strconv.ParseInt(req.PostFormValue("security_id"), 10, 64)
		amount, aErr := decimal.NewFromString(req.PostFormValue("amount"))
		date, dErr := time.Parse("2006-01-02", req.PostFormValue("date"))
		if sErr != nil || aErr != nil || dErr != nil {
			renderAccountDetail(deps, w, req, acctID, 0, "Enter a security, a valid amount, and a date.", http.StatusBadRequest)
			return
		}
		if _, err := deps.Transactions.Dividend(req.Context(), acctID, secID, amount, date, req.PostFormValue("description")); err != nil {
			renderAccountDetail(deps, w, req, acctID, 0, "Could not record dividend: "+err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, accountPath(acctID), http.StatusSeeOther)
	}
}

// parseTradeForm parses the shared buy/sell form (security_id, quantity, price,
// fees, date). Amounts are decimal strings, never floats (AD-4). Fees default to
// 0 when blank.
func parseTradeForm(req *http.Request) (securityID int64, quantity, price, fees decimal.Decimal, date time.Time, ok bool) {
	if err := req.ParseForm(); err != nil {
		return 0, decimal.Decimal{}, decimal.Decimal{}, decimal.Decimal{}, time.Time{}, false
	}
	secID, sErr := strconv.ParseInt(req.PostFormValue("security_id"), 10, 64)
	qty, qErr := decimal.NewFromString(req.PostFormValue("quantity"))
	pr, pErr := decimal.NewFromString(req.PostFormValue("price"))
	dt, dErr := time.Parse("2006-01-02", req.PostFormValue("date"))
	if sErr != nil || qErr != nil || pErr != nil || dErr != nil {
		return 0, decimal.Decimal{}, decimal.Decimal{}, decimal.Decimal{}, time.Time{}, false
	}
	feeStr := strings.TrimSpace(req.PostFormValue("fees"))
	feeAmt := decimal.Zero
	if feeStr != "" {
		f, err := decimal.NewFromString(feeStr)
		if err != nil {
			return 0, decimal.Decimal{}, decimal.Decimal{}, decimal.Decimal{}, time.Time{}, false
		}
		feeAmt = f
	}
	return secID, qty, pr, feeAmt, dt, true
}

func importForm(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		acctID, ok := parsePathID(req)
		if !ok {
			http.NotFound(w, req)
			return
		}
		renderImport(deps, w, req, acctID, "", nil, "", http.StatusOK)
	}
}

func importPreview(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		acctID, ok := parsePathID(req)
		if !ok {
			http.NotFound(w, req)
			return
		}
		content := readImportContent(req)
		res, err := deps.Imports.Preview(req.Context(), acctID, content)
		if err != nil {
			renderImport(deps, w, req, acctID, content, nil, "Could not read import: "+err.Error(), http.StatusBadRequest)
			return
		}
		renderImport(deps, w, req, acctID, content, &res, "", http.StatusOK)
	}
}

func importCommit(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		acctID, ok := parsePathID(req)
		if !ok {
			http.NotFound(w, req)
			return
		}
		if err := req.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		content := req.PostFormValue("content")
		res, err := deps.Imports.Commit(req.Context(), acctID, content)
		if err != nil {
			renderImport(deps, w, req, acctID, content, nil, "Could not import: "+err.Error(), http.StatusBadRequest)
			return
		}
		_ = res
		http.Redirect(w, req, accountPath(acctID), http.StatusSeeOther)
	}
}

// readImportContent reads the import text from an uploaded file (multipart
// "file") or, failing that, the "content" form field.
func readImportContent(req *http.Request) string {
	if err := req.ParseMultipartForm(8 << 20); err == nil {
		if f, _, ferr := req.FormFile("file"); ferr == nil {
			defer f.Close()
			if b, rerr := io.ReadAll(f); rerr == nil && len(b) > 0 {
				return string(b)
			}
		}
	}
	return req.FormValue("content")
}

func renderImport(deps Deps, w http.ResponseWriter, req *http.Request, acctID int64, content string, res *importer.Result, errMsg string, code int) {
	acct, err := deps.Accounts.Get(req.Context(), acctID)
	if err != nil {
		http.NotFound(w, req)
		return
	}
	var rows []web.ImportRow
	newCount := 0
	hasResult := res != nil
	if res != nil {
		newCount = res.New
		for _, r := range res.Rows {
			ir := web.ImportRow{Line: r.Line, Description: r.Description, Status: r.Status, Reason: r.Reason, Raw: r.Raw}
			if r.OK {
				ir.Date = r.Date.Format("2006-01-02")
				ir.Type = r.Type
				sign := "+"
				if r.Type == "expense" {
					sign = "-"
				}
				ir.Amount = sign + money.New(r.Amount, acct.Currency).String()
			}
			rows = append(rows, ir)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_ = web.ImportPage(shellData(deps, req.Context(), "accounts"), acctID, acct.Name, string(acct.Currency), content, rows, newCount, hasResult, errMsg).Render(req.Context(), w)
}

func transactionsRegister(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		acctID, _ := strconv.ParseInt(req.URL.Query().Get("account"), 10, 64)
		catID, _ := strconv.ParseInt(req.URL.Query().Get("category"), 10, 64)
		typ := transaction.TxType(req.URL.Query().Get("type"))

		regRows, _ := deps.Transactions.Register(req.Context(), transaction.RegisterFilter{
			AccountID:  acctID,
			Type:       typ,
			CategoryID: catID,
		})
		rows := make([]web.RegisterRow, 0, len(regRows))
		for _, r := range regRows {
			rows = append(rows, web.RegisterRow{
				ID:          r.ID,
				Date:        r.Date.Format("2006-01-02"),
				Type:        string(r.Type),
				Description: r.Description,
				Category:    r.Category,
				Security:    r.Security,
				Account:     r.Account,
				Amount:      registerAmount(r),
				Incoming:    r.Incoming,
				IsTransfer:  r.IsTransfer,
			})
		}

		// HTMX filter change → swap just the rows.
		if req.Header.Get("HX-Request") == "true" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_ = web.TransactionRows(rows).Render(req.Context(), w)
			return
		}

		var accounts []web.FilterOption
		if accts, err := deps.Accounts.List(req.Context(), true); err == nil {
			for _, a := range accts {
				accounts = append(accounts, web.FilterOption{ID: a.ID, Label: a.Name})
			}
		}
		var cats []web.FilterOption
		if deps.Categories != nil {
			if cs, err := deps.Categories.List(req.Context()); err == nil {
				for _, c := range cs {
					cats = append(cats, web.FilterOption{ID: c.ID, Label: c.Name})
				}
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = web.TransactionsPage(shellData(deps, req.Context(), "transactions"), accounts, cats, acctID, string(typ), catID, rows).Render(req.Context(), w)
	}
}

// registerAmount composes a register row's amount string: signed for
// income/expense, neutral legs for transfers (presentation only).
func registerAmount(r transaction.RegisterRow) string {
	if r.IsTransfer {
		s := r.Amount.String()
		if r.CrossCurrency {
			s += " → " + r.ToAmount.String()
		}
		return s
	}
	if r.Incoming {
		return "+" + r.Amount.String()
	}
	return "-" + r.Amount.String()
}

func categoriesPage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		renderCategories(deps, w, req, "", http.StatusOK)
	}
}

func categoriesCreate(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		name := req.PostFormValue("name")
		kind := category.Kind(req.PostFormValue("kind"))
		if _, err := deps.Categories.Create(req.Context(), name, kind); err != nil {
			renderCategories(deps, w, req, "Could not create category: "+err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, "/categories", http.StatusSeeOther)
	}
}

func categoriesDelete(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		id, err := strconv.ParseInt(req.PostFormValue("id"), 10, 64)
		if err != nil {
			renderCategories(deps, w, req, "Invalid category id.", http.StatusBadRequest)
			return
		}
		force := req.PostFormValue("force") == "true"
		if err := deps.Categories.Delete(req.Context(), id, force); err != nil {
			renderCategories(deps, w, req, "Could not delete category: "+err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, "/categories", http.StatusSeeOther)
	}
}

func renderCategories(deps Deps, w http.ResponseWriter, req *http.Request, errMsg string, code int) {
	var rows []web.CategoryRow
	if cs, err := deps.Categories.ListWithUsage(req.Context()); err == nil {
		for _, c := range cs {
			rows = append(rows, web.CategoryRow{ID: c.ID, Name: c.Name, Kind: string(c.Kind), Count: c.Count})
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_ = web.CategoriesPage(shellData(deps, req.Context(), "settings"), rows, errMsg).Render(req.Context(), w)
}

func categorySummary(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := parsePathID(req)
		if !ok {
			http.NotFound(w, req)
			return
		}
		// Resolve the category name from the list (no single-Get on the iface).
		name := ""
		var kind string
		if cs, err := deps.Categories.List(req.Context()); err == nil {
			for _, c := range cs {
				if c.ID == id {
					name, kind = c.Name, string(c.Kind)
				}
			}
		}
		if name == "" {
			http.NotFound(w, req)
			return
		}
		var rows []web.CategoryTxRow
		var totals []string
		if txns, sums, err := deps.Transactions.CategoryTransactions(req.Context(), id); err == nil {
			for _, t := range txns {
				rows = append(rows, web.CategoryTxRow{
					Account:     t.AccountName,
					Date:        t.Date.Format("2006-01-02"),
					Description: t.Description,
					Amount:      t.Amount.String(),
				})
			}
			for _, m := range sums {
				totals = append(totals, m.String())
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = web.CategorySummaryPage(shellData(deps, req.Context(), "settings"), name, kind, rows, totals).Render(req.Context(), w)
	}
}

func securitiesPage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		renderSecurities(deps, w, req, "", http.StatusOK)
	}
}

func securitiesCreate(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		symbol := req.PostFormValue("symbol")
		name := req.PostFormValue("name")
		typ := security.SecurityType(req.PostFormValue("type"))
		quote := money.Currency(req.PostFormValue("quote_currency"))
		if _, err := deps.Securities.Create(req.Context(), symbol, name, typ, quote); err != nil {
			renderSecurities(deps, w, req, "Could not add security: "+err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, "/securities", http.StatusSeeOther)
	}
}

// securityTypeLabel renders a stored (lowercase) security type for display.
func securityTypeLabel(t security.SecurityType) string {
	switch t {
	case security.ETF:
		return "ETF"
	case security.Stock:
		return "Stock"
	case security.Fund:
		return "Fund"
	default:
		return "Other"
	}
}

func renderSecurities(deps Deps, w http.ResponseWriter, req *http.Request, errMsg string, code int) {
	var rows []web.SecurityRow
	if secs, err := deps.Securities.List(req.Context()); err == nil {
		for _, s := range secs {
			rows = append(rows, web.SecurityRow{
				Symbol:        s.Symbol,
				Name:          s.Name,
				TypeLabel:     securityTypeLabel(s.Type),
				QuoteCurrency: string(s.QuoteCurrency),
			})
		}
	}
	types := []web.SecurityTypeOption{
		{Value: string(security.Stock), Label: "Stock"},
		{Value: string(security.ETF), Label: "ETF"},
		{Value: string(security.Fund), Label: "Fund"},
		{Value: string(security.Other), Label: "Other"},
	}
	var codes []string
	for _, c := range money.Supported() {
		codes = append(codes, string(c))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_ = web.SecuritiesPage(shellData(deps, req.Context(), "settings"), rows, types, codes, errMsg).Render(req.Context(), w)
}

// parsePathID reads the numeric {id} path parameter.
func parsePathID(req *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(req, "id"), 10, 64)
	return id, err == nil
}

// parseTxForm parses the shared transaction form fields (type/amount/date/
// description). Amount is parsed as a decimal string, never a float (AD-4).
func parseTxForm(req *http.Request) (typ transaction.TxType, amount decimal.Decimal, date time.Time, desc string, categoryID int64, ok bool) {
	if err := req.ParseForm(); err != nil {
		return "", decimal.Decimal{}, time.Time{}, "", 0, false
	}
	typ = transaction.TxType(req.PostFormValue("type"))
	amount, aErr := decimal.NewFromString(req.PostFormValue("amount"))
	date, dErr := time.Parse("2006-01-02", req.PostFormValue("date"))
	if aErr != nil || dErr != nil {
		return "", decimal.Decimal{}, time.Time{}, "", 0, false
	}
	// category_id is optional (blank ⇒ 0 ⇒ uncategorized).
	catID, _ := strconv.ParseInt(req.PostFormValue("category_id"), 10, 64)
	return typ, amount, date, req.PostFormValue("description"), catID, true
}

func accountPath(id int64) string { return "/accounts/" + strconv.FormatInt(id, 10) }

func renderAccountDetail(deps Deps, w http.ResponseWriter, req *http.Request, acctID, editID int64, errMsg string, code int) {
	acct, err := deps.Accounts.Get(req.Context(), acctID)
	if err != nil {
		http.NotFound(w, req)
		return
	}
	if account.AccountType(acct.Type) == account.Investment {
		renderInvestmentDetail(deps, w, req, acct, errMsg, code)
		return
	}
	// Credit accounts present their balance as a positive amount owed (a
	// liability); cash/investment show the signed balance. The owed figure is
	// produced by domain (AD-10) — http only renders it.
	balLabel := "Balance"
	balStr := ""
	if bal, bErr := deps.Transactions.Balance(req.Context(), acctID); bErr == nil {
		if account.AccountType(acct.Type) == account.Credit {
			balLabel = "Balance owed"
			balStr = domain.AmountOwed(bal).String()
		} else {
			balStr = bal.String()
		}
	}
	var rows []web.TxRow
	var edit web.TxRow
	editing := false
	if txns, lErr := deps.Transactions.List(req.Context(), acctID); lErr == nil {
		for _, t := range txns {
			sign := "-"
			if t.Incoming {
				sign = "+"
			}
			row := web.TxRow{
				ID:           t.ID,
				Type:         string(t.Type),
				Date:         t.Date.Format("2006-01-02"),
				Description:  t.Description,
				Counterparty: t.Counterparty,
				Category:     t.CategoryName,
				CategoryID:   t.CategoryID,
				Amount:       t.Amount.String(),
				Signed:       sign + money.New(t.Amount, acct.Currency).String(),
				Incoming:     t.Incoming,
				Editable:     t.Type != transaction.Transfer,
			}
			rows = append(rows, row)
			if editID != 0 && t.ID == editID && row.Editable {
				edit = row
				editing = true
			}
		}
	}
	if !editing {
		edit = web.TxRow{Type: string(transaction.Income), Date: time.Now().Format("2006-01-02")}
	}
	types := []string{string(transaction.Income), string(transaction.Expense)}

	// Transfer targets: the owner's other active accounts.
	var targets []web.TransferTarget
	if accts, aErr := deps.Accounts.List(req.Context(), false); aErr == nil {
		for _, a := range accts {
			if a.ID == acctID {
				continue
			}
			targets = append(targets, web.TransferTarget{ID: a.ID, Name: a.Name, Currency: string(a.Currency)})
		}
	}

	// Category options for the income/expense form.
	var cats []web.CategoryOption
	if deps.Categories != nil {
		if cs, cErr := deps.Categories.List(req.Context()); cErr == nil {
			for _, c := range cs {
				cats = append(cats, web.CategoryOption{ID: c.ID, Name: c.Name, Kind: string(c.Kind)})
			}
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_ = web.AccountDetailPage(shellData(deps, req.Context(), "accounts"), acctID, acct.Name, string(acct.Type), string(acct.Currency), balLabel, balStr, types, cats, rows, editing, edit, targets, errMsg).Render(req.Context(), w)
}

// renderInvestmentDetail renders an investment account's page: cash balance,
// derived holdings (read-only, AD-2), trade forms (buy/sell/dividend), and the
// account's investment transaction list. Trades are corrected via delete + re-add
// (no in-place edit), mirroring transfers.
func renderInvestmentDetail(deps Deps, w http.ResponseWriter, req *http.Request, acct account.Account, errMsg string, code int) {
	balStr := ""
	if bal, bErr := deps.Transactions.Balance(req.Context(), acct.ID); bErr == nil {
		balStr = bal.String()
	}

	var holdings []web.HoldingRow
	realized := ""
	oversold := false
	if hs, rg, hErr := deps.Transactions.Holdings(req.Context(), acct.ID); hErr != nil {
		oversold = errors.Is(hErr, transaction.ErrOversold)
	} else {
		for _, h := range hs {
			row := web.HoldingRow{
				Symbol:       h.Symbol,
				Name:         h.Name,
				Quantity:     h.Quantity.String(),
				AvgCost:      h.AvgCost.String(),
				CostBasis:    h.CostBasis.String(),
				RealizedGain: h.RealizedGain.String(),
				HasPrice:     h.HasPrice,
			}
			if h.HasPrice {
				row.Price = h.Price.String()
				row.PriceDate = h.PriceDate.Format("2006-01-02")
				row.MarketValue = h.MarketValue.String()
				row.UnrealizedGain = h.UnrealizedGain.String()
				row.UnrealizedNegative = h.UnrealizedGain.Amount().IsNegative()
			}
			holdings = append(holdings, row)
		}
		realized = rg.String()
	}

	var rows []web.TxRow
	if txns, lErr := deps.Transactions.List(req.Context(), acct.ID); lErr == nil {
		for _, t := range txns {
			sign := "-"
			if t.Incoming {
				sign = "+"
			}
			rows = append(rows, web.TxRow{
				ID:          t.ID,
				Type:        string(t.Type),
				Date:        t.Date.Format("2006-01-02"),
				Description: t.Description,
				Security:    t.Security,
				Quantity:    t.Quantity.String(),
				Price:       t.Price.String(),
				Signed:      sign + money.New(t.Amount, acct.Currency).String(),
				Incoming:    t.Incoming,
				Editable:    false, // trades corrected via delete + re-add
			})
		}
	}

	// Tradeable securities: same currency as the account (same-currency-only).
	var securities []web.SecurityChoice
	if secs, sErr := deps.Securities.List(req.Context()); sErr == nil {
		for _, s := range secs {
			if string(s.QuoteCurrency) == string(acct.Currency) {
				securities = append(securities, web.SecurityChoice{ID: s.ID, Symbol: s.Symbol})
			}
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_ = web.InvestmentAccountDetailPage(shellData(deps, req.Context(), "accounts"), acct.ID, acct.Name, string(acct.Currency), balStr, errMsg, holdings, realized, oversold, securities, rows).Render(req.Context(), w)
}

// shellData builds the shared shell state, including the current Display
// Currency when a Settings service is wired.
func shellData(deps Deps, ctx context.Context, active string) web.ShellData {
	dc := ""
	if deps.Settings != nil {
		if c, err := deps.Settings.DisplayCurrency(ctx); err == nil {
			dc = string(c)
		}
	}
	return web.ShellData{OwnerName: deps.OwnerName, Active: active, DisplayCurrency: dc}
}

// renderPage renders a shell-wrapped page for the given active nav section.
func renderPage(deps Deps, active string, build func(web.ShellData) templ.Component) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = build(shellData(deps, req.Context(), active)).Render(req.Context(), w)
	}
}

func settingsForm(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var codes []string
		if currs, err := deps.Settings.ListCurrencies(req.Context()); err == nil {
			for _, c := range currs {
				codes = append(codes, string(c))
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = web.SettingsPage(shellData(deps, req.Context(), "settings"), codes).Render(req.Context(), w)
	}
}

func settingsSubmit(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		// Reject unsupported values; the service is the validation authority.
		_ = deps.Settings.SetDisplayCurrency(req.Context(), money.Currency(req.PostFormValue("currency")))
		http.Redirect(w, req, "/settings", http.StatusSeeOther)
	}
}

func readyHandler(ready ReadyCheck) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if ready == nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()
		if err := ready(ctx); err != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	}
}

// requireAuth redirects unauthenticated GETs to /login and rejects other
// methods with 401.
func requireAuth(sm *scs.SessionManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if !sm.GetBool(req.Context(), sessionAuthKey) {
				if req.Method == http.MethodGet {
					http.Redirect(w, req, "/login", http.StatusSeeOther)
					return
				}
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, req)
		})
	}
}

func loginForm(sm *scs.SessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if sm.GetBool(req.Context(), sessionAuthKey) {
			http.Redirect(w, req, "/", http.StatusSeeOther)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = web.Login("").Render(req.Context(), w)
	}
}

func loginSubmit(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		username := req.PostFormValue("username")
		password := req.PostFormValue("password")

		if err := deps.Auth.Authenticate(req.Context(), username, password); err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_ = web.Login("Invalid credentials").Render(req.Context(), w)
			return
		}

		// Renew the token on privilege change to prevent session fixation.
		if err := deps.Sessions.RenewToken(req.Context()); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		deps.Sessions.Put(req.Context(), sessionAuthKey, true)
		http.Redirect(w, req, "/", http.StatusSeeOther)
	}
}

func logout(sm *scs.SessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		_ = sm.Destroy(req.Context())
		http.Redirect(w, req, "/login", http.StatusSeeOther)
	}
}
