// Package http wires the chi router, middleware, and templ-rendered views.
// It is the outermost layer (AD-1): it translates HTTP <-> service calls and
// renders results. It performs no business logic, no SQL, and no financial
// math. It owns sessions, the login/logout flow, CSRF protection, and route
// authentication, depending on an Authenticator abstraction it defines.
package http

import (
	"context"
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
	"github.com/claudioaprado/financas/internal/service/exchangerate"
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
	Record(ctx context.Context, accountID int64, typ transaction.TxType, amount decimal.Decimal, date time.Time, description string) (transaction.Transaction, error)
	Edit(ctx context.Context, accountID, txID int64, typ transaction.TxType, amount decimal.Decimal, date time.Time, description string) error
	Delete(ctx context.Context, txID int64) error
	Transfer(ctx context.Context, fromID, toID int64, fromAmount, toAmount decimal.Decimal, date time.Time, description string) error
	Balance(ctx context.Context, accountID int64) (money.Money, error)
	List(ctx context.Context, accountID int64) ([]transaction.Transaction, error)
}

// Deps are the collaborators the router needs, injected by main.
type Deps struct {
	Sessions      *scs.SessionManager
	Auth          Authenticator
	Ready         ReadyCheck
	Settings      Settings
	ExchangeRates ExchangeRates
	Accounts      Accounts
	Transactions  Transactions
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
		pr.Get("/transactions", renderPage(deps, "transactions", func(d web.ShellData) templ.Component { return web.ComingSoon(d, "Transactions") }))
		pr.Get("/accounts", accountsForm(deps))
		pr.Post("/accounts", accountsCreate(deps))
		pr.Post("/accounts/rename", accountsRename(deps))
		pr.Post("/accounts/archive", accountsArchive(deps))
		pr.Get("/accounts/{id}", accountDetail(deps))
		pr.Post("/accounts/{id}/transaction", txCreate(deps))
		pr.Post("/accounts/{id}/transaction/edit", txEdit(deps))
		pr.Post("/accounts/{id}/transaction/delete", txDelete(deps))
		pr.Post("/accounts/{id}/transfer", txTransfer(deps))
		pr.Get("/analytics", renderPage(deps, "analytics", func(d web.ShellData) templ.Component { return web.ComingSoon(d, "Analytics") }))
		pr.Get("/settings", settingsForm(deps))
		pr.Post("/settings", settingsSubmit(deps))
		pr.Get("/exchange-rates", exchangeRatesForm(deps))
		pr.Post("/exchange-rates", exchangeRatesSubmit(deps))
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
		typ, amount, date, desc, ok := parseTxForm(req)
		if !ok {
			renderAccountDetail(deps, w, req, acctID, 0, "Enter a valid amount and date.", http.StatusBadRequest)
			return
		}
		if _, err := deps.Transactions.Record(req.Context(), acctID, typ, amount, date, desc); err != nil {
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
		typ, amount, date, desc, ok := parseTxForm(req)
		if !ok {
			renderAccountDetail(deps, w, req, acctID, txID, "Enter a valid amount and date.", http.StatusBadRequest)
			return
		}
		if err := deps.Transactions.Edit(req.Context(), acctID, txID, typ, amount, date, desc); err != nil {
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

// parsePathID reads the numeric {id} path parameter.
func parsePathID(req *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(req, "id"), 10, 64)
	return id, err == nil
}

// parseTxForm parses the shared transaction form fields (type/amount/date/
// description). Amount is parsed as a decimal string, never a float (AD-4).
func parseTxForm(req *http.Request) (typ transaction.TxType, amount decimal.Decimal, date time.Time, desc string, ok bool) {
	if err := req.ParseForm(); err != nil {
		return "", decimal.Decimal{}, time.Time{}, "", false
	}
	typ = transaction.TxType(req.PostFormValue("type"))
	amount, aErr := decimal.NewFromString(req.PostFormValue("amount"))
	date, dErr := time.Parse("2006-01-02", req.PostFormValue("date"))
	if aErr != nil || dErr != nil {
		return "", decimal.Decimal{}, time.Time{}, "", false
	}
	return typ, amount, date, req.PostFormValue("description"), true
}

func accountPath(id int64) string { return "/accounts/" + strconv.FormatInt(id, 10) }

func renderAccountDetail(deps Deps, w http.ResponseWriter, req *http.Request, acctID, editID int64, errMsg string, code int) {
	acct, err := deps.Accounts.Get(req.Context(), acctID)
	if err != nil {
		http.NotFound(w, req)
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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_ = web.AccountDetailPage(shellData(deps, req.Context(), "accounts"), acctID, acct.Name, string(acct.Type), string(acct.Currency), balLabel, balStr, types, rows, editing, edit, targets, errMsg).Render(req.Context(), w)
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
