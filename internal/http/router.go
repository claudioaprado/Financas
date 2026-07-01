// Package http wires the chi router, middleware, and templ-rendered views.
// It is the outermost layer (AD-1): it translates HTTP <-> service calls and
// renders results. It performs no business logic, no SQL, and no financial
// math. It owns sessions, the login/logout flow, CSRF protection, and route
// authentication, depending on an Authenticator abstraction it defines.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
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
	"github.com/claudioaprado/financas/internal/service/backup"
	"github.com/claudioaprado/financas/internal/service/budget"
	"github.com/claudioaprado/financas/internal/service/category"
	"github.com/claudioaprado/financas/internal/service/categoryrule"
	"github.com/claudioaprado/financas/internal/service/exchangerate"
	"github.com/claudioaprado/financas/internal/service/importer"
	"github.com/claudioaprado/financas/internal/service/price"
	"github.com/claudioaprado/financas/internal/service/recurring"
	"github.com/claudioaprado/financas/internal/service/security"
	"github.com/claudioaprado/financas/internal/service/transaction"
	"github.com/claudioaprado/financas/internal/service/valuation"
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
	BulkCategorize(ctx context.Context, ids []int64, categoryID int64) (int, error)
	Annotate(ctx context.Context, txID int64, note string, tags []string) error
	// Investment trades (Story 4.2).
	Buy(ctx context.Context, accountID, securityID int64, quantity, price, fees decimal.Decimal, date time.Time, description string) (transaction.Transaction, error)
	Sell(ctx context.Context, accountID, securityID int64, quantity, price, fees decimal.Decimal, date time.Time, description string) (transaction.Transaction, error)
	Dividend(ctx context.Context, accountID, securityID int64, amount decimal.Decimal, date time.Time, description string) (transaction.Transaction, error)
	Holdings(ctx context.Context, accountID int64) ([]transaction.HoldingView, money.Money, []string, error)
}

// Categories creates, lists, and deletes income/expense categories. Defined here
// (consumer side) and implemented by service/category.
type Categories interface {
	Create(ctx context.Context, name string, kind category.Kind) (category.Category, error)
	List(ctx context.Context) ([]category.Category, error)
	ListWithUsage(ctx context.Context) ([]category.CategoryUsage, error)
	Delete(ctx context.Context, id int64, force bool) error
}

// CategoryRules manages auto-categorization rules (Story 7.2 / FR-17). Defined
// here (consumer side) and implemented by service/categoryrule.
type CategoryRules interface {
	List(ctx context.Context) ([]categoryrule.Rule, error)
	Add(ctx context.Context, matchText string, categoryID int64) (categoryrule.Rule, error)
	Delete(ctx context.Context, id int64) error
}

// Budgets sets, lists, and deletes monthly category budget targets (Story 8.1 /
// FR-18). Defined here (consumer side) and implemented by service/budget.
type Budgets interface {
	Report(ctx context.Context, year int, month time.Month) (domain.BudgetReport, error)
	Set(ctx context.Context, categoryID int64, amount decimal.Decimal) error
	Delete(ctx context.Context, categoryID int64) error
}

// Analytics derives the spending & cash-flow view (Story 8.3 / FR-19). Defined
// here (consumer side) and implemented by service/analytics.
type Analytics interface {
	Report(ctx context.Context, months int) (domain.Analytics, error)
}

// Recurring manages recurring transaction templates and posts due occurrences
// (Epic 9 / FR-20). Defined here (consumer side) and implemented by
// service/recurring.
type Recurring interface {
	Create(ctx context.Context, in recurring.Input) (int64, error)
	Edit(ctx context.Context, id int64, in recurring.Input) error
	Delete(ctx context.Context, id int64) error
	List(ctx context.Context) ([]recurring.Recurring, error)
	Due(ctx context.Context) ([]recurring.Recurring, error)
	Post(ctx context.Context, id int64, occurrence time.Time) error
	Skip(ctx context.Context, id int64, occurrence time.Time) error
}

// Securities creates and lists the owner's securities. Defined here (consumer
// side) and implemented by service/security.
type Securities interface {
	Create(ctx context.Context, symbol, name string, typ security.SecurityType, quote money.Currency) (security.Security, error)
	List(ctx context.Context) ([]security.Security, error)
}

// Imports previews and commits file imports — tab-delimited (Story 3.6) or OFX
// (Story 7.1). Defined here (consumer side) and implemented by service/importer.
type Imports interface {
	Preview(ctx context.Context, accountID int64, content string) (importer.Result, error)
	Commit(ctx context.Context, accountID int64, content string, cats map[int]int64) (importer.Result, error)
	PreviewOFX(ctx context.Context, accountID int64, content string) (importer.Result, error)
	CommitOFX(ctx context.Context, accountID int64, content string, cats map[int]int64) (importer.Result, error)
}

// Valuation derives the cross-account portfolio & Net Worth in the Display
// Currency (Story 4.4). Defined here (consumer side) and implemented by
// service/valuation.
type Valuation interface {
	Portfolio(ctx context.Context) (valuation.Portfolio, error)
	Dashboard(ctx context.Context) (valuation.Dashboard, error)
	ValueSeries(ctx context.Context, from time.Time) ([]valuation.SeriesPoint, error)
	Allocation(ctx context.Context, by string) (valuation.Allocation, error)
	Insight(ctx context.Context) (valuation.Insight, error)
}

// Backup assembles the owner's authored-data export (Story 6.1, FR-15). Defined
// here (consumer side) and implemented by service/backup. The http layer only
// serializes and streams the result (AD-1).
type Backup interface {
	Export(ctx context.Context) (backup.Export, error)
	Restore(ctx context.Context, raw []byte) (backup.RestoreSummary, error)
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
	CategoryRules CategoryRules
	Budgets       Budgets
	Analytics     Analytics
	Recurring     Recurring
	Securities    Securities
	Imports       Imports
	Valuation     Valuation
	Backup        Backup
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
		pr.Get("/", dashboardPage(deps))
		pr.Get("/investments", investmentsPage(deps))
		pr.Get("/transactions", transactionsRegister(deps))
		pr.Post("/transactions/bulk-categorize", transactionsBulkCategorize(deps))
		pr.Get("/accounts", accountsForm(deps))
		pr.Post("/accounts", accountsCreate(deps))
		pr.Post("/accounts/rename", accountsRename(deps))
		pr.Post("/accounts/archive", accountsArchive(deps))
		pr.Get("/accounts/{id}", accountDetail(deps))
		pr.Post("/accounts/{id}/transaction", txCreate(deps))
		pr.Post("/accounts/{id}/transaction/edit", txEdit(deps))
		pr.Post("/accounts/{id}/transaction/delete", txDelete(deps))
		pr.Post("/accounts/{id}/transaction/annotate", txAnnotate(deps))
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
		pr.Get("/categories/rules", rulesPage(deps))
		pr.Post("/categories/rules", rulesCreate(deps))
		pr.Post("/categories/rules/delete", rulesDelete(deps))
		pr.Get("/budgets", budgetsPage(deps))
		pr.Post("/budgets", budgetsSet(deps))
		pr.Post("/budgets/delete", budgetsDelete(deps))
		pr.Get("/recurring", recurringPage(deps))
		pr.Post("/recurring", recurringCreate(deps))
		pr.Post("/recurring/edit", recurringEdit(deps))
		pr.Post("/recurring/delete", recurringDelete(deps))
		pr.Post("/recurring/post", recurringPost(deps))
		pr.Post("/recurring/skip", recurringSkip(deps))
		pr.Get("/categories/{id}", categorySummary(deps))
		pr.Get("/securities", securitiesPage(deps))
		pr.Post("/securities", securitiesCreate(deps))
		pr.Get("/analytics", analyticsPage(deps))
		pr.Get("/settings", settingsForm(deps))
		pr.Post("/settings", settingsSubmit(deps))
		pr.Get("/export", exportData(deps))
		pr.Post("/restore", restoreData(deps))
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
			http.Error(w, "requisição inválida", http.StatusBadRequest)
			return
		}
		from := money.Currency(req.PostFormValue("from"))
		to := money.Currency(req.PostFormValue("to"))
		effective, dErr := time.Parse("2006-01-02", req.PostFormValue("effective_date"))
		rate, rErr := money.ParseDecimal(req.PostFormValue("rate"))
		if dErr != nil || rErr != nil {
			renderExchangeRates(deps, w, req, "Informe uma data válida e uma taxa decimal.", http.StatusBadRequest)
			return
		}
		if _, err := deps.ExchangeRates.Add(req.Context(), from, to, effective, rate); err != nil {
			renderExchangeRates(deps, w, req, problemMsg(req, "Não foi possível adicionar a taxa. Verifique os dados e tente novamente.", err), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, "/exchange-rates", http.StatusSeeOther)
	}
}

// loadErrorMsg is the banner shown when a page's PRIMARY data can't be loaded
// (e.g. a database outage). Such a failure renders with HTTP 500 and this message
// instead of a misleading empty page — an empty list under a 200 reads as "you
// have no data", which on an outage is silently wrong (deferred-work: http-layer
// error swallowing).
const loadErrorMsg = "Não foi possível carregar esta página agora. Tente novamente."

// logLoad records a data-load failure server-side. Primary loads pair it with a
// 500 (the page can't be built); secondary loads (filter dropdowns, the shell
// greeting) log and degrade gracefully so the rest of the page still works.
func logLoad(req *http.Request, what string, err error) {
	log.Printf("http: %s %s: %v", req.Method, what, err)
}

// problemMsg turns a service error into an owner-facing pt-BR message for a failed
// mutation. A KNOWN validation sentinel maps to a specific, translated reason; an
// unrecognized error (a database/driver failure) is logged server-side and yields
// the caller's generic fallback — so the raw internal error is never shown to the
// user (AD-1; deferred-work: raw err.Error() echo).
func problemMsg(req *http.Request, fallback string, err error) string {
	if msg, ok := knownErrMsg(err); ok {
		return msg
	}
	log.Printf("http: %s %s: %v", req.Method, req.URL.Path, err)
	return fallback
}

// knownErrMsg maps a service's exported validation sentinels to a pt-BR message.
// The ok result is false for any other error (an infra failure), which the caller
// must treat generically rather than surface.
func knownErrMsg(err error) (string, bool) {
	switch {
	// account
	case errors.Is(err, account.ErrEmptyName):
		return "O nome da conta não pode ficar vazio.", true
	case errors.Is(err, account.ErrInvalidType):
		return "Tipo de conta inválido (caixa, crédito ou investimento).", true
	case errors.Is(err, account.ErrUnsupportedCurrency):
		return "Moeda não suportada.", true
	case errors.Is(err, account.ErrNotFound):
		return "Conta não encontrada.", true
	// category
	case errors.Is(err, category.ErrEmptyName):
		return "O nome da categoria não pode ficar vazio.", true
	case errors.Is(err, category.ErrInvalidKind):
		return "O tipo deve ser receita ou despesa.", true
	case errors.Is(err, category.ErrCategoryInUse):
		return "Esta categoria está em uso por transações.", true
	case errors.Is(err, category.ErrNotFound):
		return "Categoria não encontrada.", true
	// categoryrule
	case errors.Is(err, categoryrule.ErrEmptyMatch):
		return "O texto da regra não pode ficar vazio.", true
	case errors.Is(err, categoryrule.ErrCategoryNotFound):
		return "Categoria não encontrada.", true
	case errors.Is(err, categoryrule.ErrNotFound):
		return "Regra não encontrada.", true
	// budget
	case errors.Is(err, budget.ErrNonPositiveAmount):
		return "O valor do orçamento deve ser maior que zero.", true
	case errors.Is(err, budget.ErrCategoryNotFound):
		return "Categoria não encontrada.", true
	case errors.Is(err, budget.ErrNotFound):
		return "Orçamento não encontrado.", true
	// recurring
	case errors.Is(err, recurring.ErrInvalidType):
		return "Tipo inválido (receita, despesa ou transferência).", true
	case errors.Is(err, recurring.ErrNonPositiveAmount):
		return "O valor deve ser maior que zero.", true
	case errors.Is(err, recurring.ErrInvalidCadence):
		return "A cadência deve ser semanas, meses ou anos.", true
	case errors.Is(err, recurring.ErrNonPositiveInterval):
		return "O intervalo deve ser um número inteiro positivo.", true
	case errors.Is(err, recurring.ErrMissingStartDate):
		return "Informe a data de início.", true
	case errors.Is(err, recurring.ErrInvalidDateRange):
		return "A data de fim não pode ser anterior ao início.", true
	case errors.Is(err, recurring.ErrAccountNotFound):
		return "Conta não encontrada.", true
	case errors.Is(err, recurring.ErrUnsupportedAccountType):
		return "Receitas e despesas exigem uma conta de caixa ou crédito.", true
	case errors.Is(err, recurring.ErrSameAccount):
		return "A origem e o destino da transferência devem ser diferentes.", true
	case errors.Is(err, recurring.ErrToAmountRequired):
		return "Transferência entre moedas precisa do valor de destino.", true
	case errors.Is(err, recurring.ErrSameCurrencyAmountMismatch):
		return "Transferência na mesma moeda deve ter valores iguais.", true
	case errors.Is(err, recurring.ErrCategoryNotFound):
		return "Categoria não encontrada.", true
	case errors.Is(err, recurring.ErrCategoryKindMismatch):
		return "O tipo da categoria deve combinar com o tipo da transação.", true
	case errors.Is(err, recurring.ErrCategoryOnTransfer):
		return "Transferências não recebem categoria.", true
	case errors.Is(err, recurring.ErrNotFound):
		return "Recorrente não encontrado.", true
	case errors.Is(err, recurring.ErrNotDue):
		return "Esta ocorrência já foi tratada.", true
	// security
	case errors.Is(err, security.ErrEmptySymbol):
		return "O código do ativo não pode ficar vazio.", true
	case errors.Is(err, security.ErrEmptyName):
		return "O nome do ativo não pode ficar vazio.", true
	case errors.Is(err, security.ErrInvalidType):
		return "Tipo de ativo inválido (ação, ETF, fundo ou outro).", true
	case errors.Is(err, security.ErrUnsupportedCurrency):
		return "Moeda de cotação não suportada.", true
	case errors.Is(err, security.ErrDuplicateSymbol):
		return "Já existe um ativo com esse código.", true
	case errors.Is(err, security.ErrNotFound):
		return "Ativo não encontrado.", true
	// price
	case errors.Is(err, price.ErrSecurityNotFound):
		return "Ativo não encontrado.", true
	case errors.Is(err, price.ErrNonPositivePrice):
		return "O preço deve ser positivo.", true
	// exchange rate
	case errors.Is(err, exchangerate.ErrUnsupportedCurrency):
		return "Moeda não suportada.", true
	case errors.Is(err, exchangerate.ErrSameCurrency):
		return "As moedas de origem e destino devem ser diferentes.", true
	case errors.Is(err, exchangerate.ErrNonPositiveRate):
		return "A taxa deve ser positiva.", true
	// importer
	case errors.Is(err, importer.ErrAccountNotFound):
		return "Conta não encontrada.", true
	case errors.Is(err, importer.ErrUnsupportedAccountType):
		return "A importação exige uma conta de caixa ou crédito.", true
	// transaction
	case errors.Is(err, transaction.ErrAccountNotFound):
		return "Conta não encontrada.", true
	case errors.Is(err, transaction.ErrUnsupportedAccountType):
		return "Receitas e despesas exigem uma conta de caixa ou crédito.", true
	case errors.Is(err, transaction.ErrInvalidType):
		return "Tipo de transação inválido.", true
	case errors.Is(err, transaction.ErrNonPositiveAmount):
		return "O valor deve ser positivo.", true
	case errors.Is(err, transaction.ErrTxNotFound):
		return "Transação não encontrada.", true
	case errors.Is(err, transaction.ErrSameAccount):
		return "A origem e o destino da transferência devem ser diferentes.", true
	case errors.Is(err, transaction.ErrSameCurrencyAmountMismatch):
		return "Transferência na mesma moeda deve ter valores iguais.", true
	case errors.Is(err, transaction.ErrCrossCurrencyToAmountRequired):
		return "Transferência entre moedas precisa do valor de destino.", true
	case errors.Is(err, transaction.ErrCategoryNotFound):
		return "Categoria não encontrada.", true
	case errors.Is(err, transaction.ErrCategoryKindMismatch):
		return "O tipo da categoria deve combinar com o tipo da transação.", true
	case errors.Is(err, transaction.ErrNoSelection):
		return "Selecione ao menos uma transação.", true
	case errors.Is(err, transaction.ErrNotInvestmentAccount):
		return "Compra, venda e dividendo exigem uma conta de investimento.", true
	case errors.Is(err, transaction.ErrSecurityNotFound):
		return "Ativo não encontrado.", true
	case errors.Is(err, transaction.ErrTradeCurrencyMismatch):
		return "A moeda de cotação do ativo deve ser igual à da conta.", true
	case errors.Is(err, transaction.ErrNonPositiveQuantity):
		return "A quantidade deve ser positiva.", true
	case errors.Is(err, transaction.ErrNonPositivePrice):
		return "O preço deve ser positivo.", true
	case errors.Is(err, transaction.ErrNegativeFees):
		return "As taxas não podem ser negativas.", true
	case errors.Is(err, transaction.ErrNegativeProceeds):
		return "As taxas excedem o valor bruto da venda.", true
	case errors.Is(err, transaction.ErrOversold):
		return "A venda excede a quantidade em carteira.", true
	default:
		return "", false
	}
}

// renderError renders the generic error page (or an inline fragment for HTMX)
// with HTTP 500. Used by primary-load handlers whose page component has no
// error-banner slot of their own (the register, the category summary).
func renderError(deps Deps, w http.ResponseWriter, req *http.Request, active, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	if req.Header.Get("HX-Request") == "true" {
		_ = web.ErrorFragment(msg).Render(req.Context(), w)
		return
	}
	_ = web.ErrorPage(shellData(deps, req.Context(), active), msg).Render(req.Context(), w)
}

func renderExchangeRates(deps Deps, w http.ResponseWriter, req *http.Request, errMsg string, code int) {
	var rows []web.RateRow
	rs, err := deps.ExchangeRates.List(req.Context())
	if err != nil {
		logLoad(req, "exchange-rates list", err)
		errMsg, code = loadErrorMsg, http.StatusInternalServerError
	}
	for _, r := range rs {
		rows = append(rows, web.RateRow{
			From:          string(r.From),
			To:            string(r.To),
			EffectiveDate: r.EffectiveDate.Format("02/01/2006"),
			Rate:          money.FormatDecimal(r.Rate),
		})
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
			http.Error(w, "requisição inválida", http.StatusBadRequest)
			return
		}
		securityID, idErr := strconv.ParseInt(req.PostFormValue("security_id"), 10, 64)
		effective, dErr := time.Parse("2006-01-02", req.PostFormValue("effective_date"))
		price, pErr := money.ParseDecimal(req.PostFormValue("price"))
		if idErr != nil || dErr != nil || pErr != nil {
			renderPrices(deps, w, req, "Escolha um ativo e informe uma data válida e um preço decimal.", http.StatusBadRequest)
			return
		}
		if _, err := deps.Prices.Add(req.Context(), securityID, effective, price); err != nil {
			renderPrices(deps, w, req, problemMsg(req, "Não foi possível adicionar o preço. Verifique os dados e tente novamente.", err), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, "/prices", http.StatusSeeOther)
	}
}

func renderPrices(deps Deps, w http.ResponseWriter, req *http.Request, errMsg string, code int) {
	var rows []web.PriceRow
	ps, err := deps.Prices.List(req.Context())
	if err != nil {
		logLoad(req, "prices list", err)
		errMsg, code = loadErrorMsg, http.StatusInternalServerError
	}
	for _, p := range ps {
		rows = append(rows, web.PriceRow{
			Symbol:        p.Symbol,
			EffectiveDate: p.EffectiveDate.Format("02/01/2006"),
			Price:         money.New(p.Price, p.Currency).Display(),
		})
	}
	// A price applies to any security — the select is NOT currency-filtered (unlike
	// the trade form). All securities are offered. The dropdown is secondary: a load
	// failure degrades to an empty select (logged), it doesn't fail the page.
	var securities []web.SecurityChoice
	if secs, sErr := deps.Securities.List(req.Context()); sErr != nil {
		logLoad(req, "prices securities dropdown", sErr)
	} else {
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
			http.Error(w, "requisição inválida", http.StatusBadRequest)
			return
		}
		name := req.PostFormValue("name")
		typ := account.AccountType(req.PostFormValue("type"))
		currency := money.Currency(req.PostFormValue("currency"))
		if _, err := deps.Accounts.Create(req.Context(), name, typ, currency); err != nil {
			renderAccounts(deps, w, req, false, problemMsg(req, "Não foi possível criar a conta. Verifique os dados e tente novamente.", err), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, "/accounts", http.StatusSeeOther)
	}
}

func accountsRename(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, "requisição inválida", http.StatusBadRequest)
			return
		}
		id, err := strconv.ParseInt(req.PostFormValue("id"), 10, 64)
		if err != nil {
			renderAccounts(deps, w, req, showArchived(req), "ID de conta inválido.", http.StatusBadRequest)
			return
		}
		if err := deps.Accounts.Rename(req.Context(), id, req.PostFormValue("name")); err != nil {
			renderAccounts(deps, w, req, showArchived(req), problemMsg(req, "Não foi possível renomear a conta. Verifique os dados e tente novamente.", err), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, accountsRedirect(req), http.StatusSeeOther)
	}
}

func accountsArchive(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, "requisição inválida", http.StatusBadRequest)
			return
		}
		id, err := strconv.ParseInt(req.PostFormValue("id"), 10, 64)
		if err != nil {
			renderAccounts(deps, w, req, showArchived(req), "ID de conta inválido.", http.StatusBadRequest)
			return
		}
		archived := req.PostFormValue("archived") == "true"
		if err := deps.Accounts.SetArchived(req.Context(), id, archived); err != nil {
			renderAccounts(deps, w, req, showArchived(req), problemMsg(req, "Não foi possível atualizar a conta. Tente novamente.", err), http.StatusBadRequest)
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
		return "Saldo devedor"
	}
	return "Saldo em caixa"
}

func renderAccounts(deps Deps, w http.ResponseWriter, req *http.Request, includeArchived bool, errMsg string, code int) {
	var rows []web.AccountRow
	accts, err := deps.Accounts.List(req.Context(), includeArchived)
	if err != nil {
		logLoad(req, "accounts list", err)
		errMsg, code = loadErrorMsg, http.StatusInternalServerError
	}
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
			renderAccountDetail(deps, w, req, acctID, 0, "Informe um valor e uma data válidos.", http.StatusBadRequest)
			return
		}
		if _, err := deps.Transactions.Record(req.Context(), acctID, typ, amount, date, desc, catID); err != nil {
			renderAccountDetail(deps, w, req, acctID, 0, problemMsg(req, "Não foi possível adicionar a transação. Verifique os dados e tente novamente.", err), http.StatusBadRequest)
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
			renderAccountDetail(deps, w, req, acctID, 0, "ID de transação inválido.", http.StatusBadRequest)
			return
		}
		typ, amount, date, desc, catID, ok := parseTxForm(req)
		if !ok {
			renderAccountDetail(deps, w, req, acctID, txID, "Informe um valor e uma data válidos.", http.StatusBadRequest)
			return
		}
		if err := deps.Transactions.Edit(req.Context(), acctID, txID, typ, amount, date, desc, catID); err != nil {
			renderAccountDetail(deps, w, req, acctID, txID, problemMsg(req, "Não foi possível salvar a transação. Verifique os dados e tente novamente.", err), http.StatusBadRequest)
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
			renderAccountDetail(deps, w, req, acctID, 0, "ID de transação inválido.", http.StatusBadRequest)
			return
		}
		if err := deps.Transactions.Delete(req.Context(), txID); err != nil {
			renderAccountDetail(deps, w, req, acctID, 0, problemMsg(req, "Não foi possível excluir a transação. Tente novamente.", err), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, accountPath(acctID), http.StatusSeeOther)
	}
}

// txAnnotate saves a transaction's note and tag set (Story 10.2). Tags arrive as
// a comma-separated field; the service trims/dedupes/creates-on-use and replaces
// the whole set in one tx (AD-3). It redirects back to the account, keeping the
// row open for editing so the saved note/tags are visible.
func txAnnotate(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		acctID, ok := parsePathID(req)
		if !ok {
			http.NotFound(w, req)
			return
		}
		txID, err := strconv.ParseInt(req.PostFormValue("tx_id"), 10, 64)
		if err != nil {
			renderAccountDetail(deps, w, req, acctID, 0, "ID de transação inválido.", http.StatusBadRequest)
			return
		}
		note := req.PostFormValue("note")
		tags := splitTags(req.PostFormValue("tags"))
		if err := deps.Transactions.Annotate(req.Context(), txID, note, tags); err != nil {
			renderAccountDetail(deps, w, req, acctID, txID, problemMsg(req, "Não foi possível salvar as notas e etiquetas. Tente novamente.", err), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, accountPath(acctID)+"?edit="+accountIDStr(txID), http.StatusSeeOther)
	}
}

// splitTags parses a comma-separated tags field into trimmed, non-empty names.
// The service is the authority for further dedupe/validation.
func splitTags(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// accountIDStr renders an id for a query string (thin wrapper for readability).
func accountIDStr(id int64) string { return strconv.FormatInt(id, 10) }

func txTransfer(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		acctID, ok := parsePathID(req)
		if !ok {
			http.NotFound(w, req)
			return
		}
		if err := req.ParseForm(); err != nil {
			http.Error(w, "requisição inválida", http.StatusBadRequest)
			return
		}
		toID, idErr := strconv.ParseInt(req.PostFormValue("to_account_id"), 10, 64)
		fromAmount, faErr := money.ParseDecimal(req.PostFormValue("from_amount"))
		date, dErr := time.Parse("2006-01-02", req.PostFormValue("date"))
		if idErr != nil || faErr != nil || dErr != nil {
			renderAccountDetail(deps, w, req, acctID, 0, "Informe um destino, um valor válido e uma data.", http.StatusBadRequest)
			return
		}
		// The received amount is optional (blank ⇒ same-currency); a non-empty
		// value must parse.
		toAmount := decimal.Zero
		if raw := strings.TrimSpace(req.PostFormValue("to_amount")); raw != "" {
			parsed, err := money.ParseDecimal(raw)
			if err != nil {
				renderAccountDetail(deps, w, req, acctID, 0, "Informe um valor recebido válido.", http.StatusBadRequest)
				return
			}
			toAmount = parsed
		}
		if err := deps.Transactions.Transfer(req.Context(), acctID, toID, fromAmount, toAmount, date, req.PostFormValue("description")); err != nil {
			renderAccountDetail(deps, w, req, acctID, 0, problemMsg(req, "Não foi possível transferir. Verifique os dados e tente novamente.", err), http.StatusBadRequest)
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
			renderAccountDetail(deps, w, req, acctID, 0, "Informe um ativo, uma quantidade, um preço e uma data válidos.", http.StatusBadRequest)
			return
		}
		if _, err := deps.Transactions.Buy(req.Context(), acctID, secID, qty, price, fees, date, req.PostFormValue("description")); err != nil {
			renderAccountDetail(deps, w, req, acctID, 0, problemMsg(req, "Não foi possível registrar a compra. Verifique os dados e tente novamente.", err), http.StatusBadRequest)
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
			renderAccountDetail(deps, w, req, acctID, 0, "Informe um ativo, uma quantidade, um preço e uma data válidos.", http.StatusBadRequest)
			return
		}
		if _, err := deps.Transactions.Sell(req.Context(), acctID, secID, qty, price, fees, date, req.PostFormValue("description")); err != nil {
			renderAccountDetail(deps, w, req, acctID, 0, problemMsg(req, "Não foi possível registrar a venda. Verifique os dados e tente novamente.", err), http.StatusBadRequest)
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
			http.Error(w, "requisição inválida", http.StatusBadRequest)
			return
		}
		secID, sErr := strconv.ParseInt(req.PostFormValue("security_id"), 10, 64)
		amount, aErr := money.ParseDecimal(req.PostFormValue("amount"))
		date, dErr := time.Parse("2006-01-02", req.PostFormValue("date"))
		if sErr != nil || aErr != nil || dErr != nil {
			renderAccountDetail(deps, w, req, acctID, 0, "Informe um ativo, um valor válido e uma data.", http.StatusBadRequest)
			return
		}
		if _, err := deps.Transactions.Dividend(req.Context(), acctID, secID, amount, date, req.PostFormValue("description")); err != nil {
			renderAccountDetail(deps, w, req, acctID, 0, problemMsg(req, "Não foi possível registrar o dividendo. Verifique os dados e tente novamente.", err), http.StatusBadRequest)
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
	qty, qErr := money.ParseDecimal(req.PostFormValue("quantity"))
	pr, pErr := money.ParseDecimal(req.PostFormValue("price"))
	dt, dErr := time.Parse("2006-01-02", req.PostFormValue("date"))
	if sErr != nil || qErr != nil || pErr != nil || dErr != nil {
		return 0, decimal.Decimal{}, decimal.Decimal{}, decimal.Decimal{}, time.Time{}, false
	}
	feeStr := strings.TrimSpace(req.PostFormValue("fees"))
	feeAmt := decimal.Zero
	if feeStr != "" {
		f, err := money.ParseDecimal(feeStr)
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
		renderImport(deps, w, req, acctID, "tab", "", nil, "", http.StatusOK)
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
		format := importFormat(req.FormValue("format"))
		var res importer.Result
		var err error
		if format == "ofx" {
			res, err = deps.Imports.PreviewOFX(req.Context(), acctID, content)
		} else {
			res, err = deps.Imports.Preview(req.Context(), acctID, content)
		}
		if err != nil {
			renderImport(deps, w, req, acctID, format, content, nil, problemMsg(req, "Não foi possível ler a importação. Verifique o arquivo e tente novamente.", err), http.StatusBadRequest)
			return
		}
		renderImport(deps, w, req, acctID, format, content, &res, "", http.StatusOK)
	}
}

// importFormat normalizes the import-format form value to "tab" (default) or
// "ofx".
func importFormat(v string) string {
	if v == "ofx" {
		return "ofx"
	}
	return "tab"
}

// parseImportCategories reads the per-row category selects (cat_{line}=id) the
// owner accepted/overrode in the preview into a Line→categoryID map. A present
// value of 0 (the blank option) means "intentionally uncategorized"; an absent
// key lets the service fall back to the rule suggestion (FR-17).
func parseImportCategories(req *http.Request) map[int]int64 {
	cats := map[int]int64{}
	for key, vals := range req.PostForm {
		if !strings.HasPrefix(key, "cat_") || len(vals) == 0 {
			continue
		}
		line, err := strconv.Atoi(strings.TrimPrefix(key, "cat_"))
		if err != nil {
			continue
		}
		id, _ := strconv.ParseInt(vals[0], 10, 64) // "" / "0" ⇒ 0 (uncategorized)
		cats[line] = id
	}
	return cats
}

func importCommit(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		acctID, ok := parsePathID(req)
		if !ok {
			http.NotFound(w, req)
			return
		}
		if err := req.ParseForm(); err != nil {
			http.Error(w, "requisição inválida", http.StatusBadRequest)
			return
		}
		content := req.PostFormValue("content")
		format := importFormat(req.PostFormValue("format"))
		cats := parseImportCategories(req)
		var res importer.Result
		var err error
		if format == "ofx" {
			res, err = deps.Imports.CommitOFX(req.Context(), acctID, content, cats)
		} else {
			res, err = deps.Imports.Commit(req.Context(), acctID, content, cats)
		}
		if err != nil {
			renderImport(deps, w, req, acctID, format, content, nil, problemMsg(req, "Não foi possível importar. Verifique o arquivo e tente novamente.", err), http.StatusBadRequest)
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

func renderImport(deps Deps, w http.ResponseWriter, req *http.Request, acctID int64, format, content string, res *importer.Result, errMsg string, code int) {
	acct, err := deps.Accounts.Get(req.Context(), acctID)
	if err != nil {
		// A genuinely unknown id is a 404; a DB outage is a load error (500) — never
		// let an outage masquerade as "not found".
		if errors.Is(err, account.ErrNotFound) {
			http.NotFound(w, req)
			return
		}
		logLoad(req, "import account lookup", err)
		renderError(deps, w, req, "accounts", loadErrorMsg)
		return
	}
	// Categories power the per-row suggestion selects. A load failure here is
	// secondary — degrade to no selects rather than failing the whole import.
	var incomeCats, expenseCats []web.CategoryOption
	if deps.Categories != nil {
		if cs, cerr := deps.Categories.List(req.Context()); cerr != nil {
			logLoad(req, "import categories", cerr)
		} else {
			for _, c := range cs {
				opt := web.CategoryOption{ID: c.ID, Name: c.Name, Kind: string(c.Kind)}
				if c.Kind == category.Income {
					incomeCats = append(incomeCats, opt)
				} else {
					expenseCats = append(expenseCats, opt)
				}
			}
		}
	}

	var rows []web.ImportRow
	newCount := 0
	hasResult := res != nil
	if res != nil {
		newCount = res.New
		for _, r := range res.Rows {
			ir := web.ImportRow{Line: r.Line, Description: r.Description, Status: r.Status, Reason: r.Reason, Warning: r.Warning, Raw: r.Raw, SuggestedCategoryID: r.SuggestedCategoryID}
			if r.OK {
				ir.Date = r.Date.Format("02/01/2006")
				ir.Type = r.Type
				sign := "+"
				if r.Type == "expense" {
					sign = "-"
				}
				ir.Amount = sign + money.New(r.Amount, acct.Currency).Display()
			}
			rows = append(rows, ir)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_ = web.ImportPage(shellData(deps, req.Context(), "accounts"), acctID, acct.Name, string(acct.Currency), format, content, rows, incomeCats, expenseCats, newCount, hasResult, errMsg).Render(req.Context(), w)
}

func transactionsRegister(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		f := registerFilter(req)
		regRows, rErr := deps.Transactions.Register(req.Context(), f)
		if rErr != nil {
			logLoad(req, "transactions register", rErr)
			renderError(deps, w, req, "transactions", loadErrorMsg)
			return
		}
		rows := mapRegisterRows(regRows)

		// HTMX filter change → swap just the rows.
		if req.Header.Get("HX-Request") == "true" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_ = web.TransactionRows(rows).Render(req.Context(), w)
			return
		}
		renderRegister(deps, w, req, f, rows, "", http.StatusOK)
	}
}

// registerFilter reads the register's filters from the request (query on GET,
// form on POST — FormValue covers both). CategoryID/AccountID default to 0 (all).
func registerFilter(req *http.Request) transaction.RegisterFilter {
	acctID, _ := strconv.ParseInt(req.FormValue("account"), 10, 64)
	catID, _ := strconv.ParseInt(req.FormValue("category"), 10, 64)
	return transaction.RegisterFilter{
		AccountID:  acctID,
		Type:       transaction.TxType(req.FormValue("type")),
		CategoryID: catID,
		Search:     req.FormValue("q"),
	}
}

// renderRegister renders the full register page for the given filter + rows,
// gathering the (secondary) filter/bulk dropdowns. Used by the GET handler and by
// a failed bulk op (to show the error banner without losing the filters).
func renderRegister(deps Deps, w http.ResponseWriter, req *http.Request, f transaction.RegisterFilter, rows []web.RegisterRow, errMsg string, code int) {
	v := web.RegisterView{
		Rows:        rows,
		SelAccount:  f.AccountID,
		SelType:     string(f.Type),
		SelCategory: f.CategoryID,
		Query:       f.Search,
		ErrMsg:      errMsg,
	}
	// Dropdowns are secondary: a load failure degrades to empty (logged), it does
	// not fail the register page.
	if accts, err := deps.Accounts.List(req.Context(), true); err != nil {
		logLoad(req, "register accounts filter", err)
	} else {
		for _, a := range accts {
			v.Accounts = append(v.Accounts, web.FilterOption{ID: a.ID, Label: a.Name})
		}
	}
	if deps.Categories != nil {
		if cs, err := deps.Categories.List(req.Context()); err != nil {
			logLoad(req, "register categories filter", err)
		} else {
			for _, c := range cs {
				v.Categories = append(v.Categories, web.FilterOption{ID: c.ID, Label: c.Name})
				v.BulkCats = append(v.BulkCats, web.CategoryOption{ID: c.ID, Name: c.Name, Kind: string(c.Kind)})
			}
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_ = web.TransactionsPage(shellData(deps, req.Context(), "transactions"), v).Render(req.Context(), w)
}

// transactionsBulkCategorize applies one category to the selected income/expense
// register rows (Story 10.1 / FR-21) in one DB transaction, then redirects back
// to the register preserving the active filters. On failure it re-renders the
// page with an error banner (filters intact).
func transactionsBulkCategorize(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, "requisição inválida", http.StatusBadRequest)
			return
		}
		categoryID, _ := strconv.ParseInt(req.PostFormValue("category_id"), 10, 64)
		ids := make([]int64, 0, len(req.PostForm["tx"]))
		for _, v := range req.PostForm["tx"] {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil {
				ids = append(ids, id)
			}
		}
		if _, err := deps.Transactions.BulkCategorize(req.Context(), ids, categoryID); err != nil {
			f := registerFilter(req)
			regRows, rErr := deps.Transactions.Register(req.Context(), f)
			if rErr != nil {
				logLoad(req, "register reload after bulk", rErr)
				renderError(deps, w, req, "transactions", loadErrorMsg)
				return
			}
			renderRegister(deps, w, req, f, mapRegisterRows(regRows),
				problemMsg(req, "Não foi possível categorizar as transações. Tente novamente.", err), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, registerRedirect(req), http.StatusSeeOther)
	}
}

// registerRedirect rebuilds the /transactions URL preserving the active filters
// carried by the bulk form's hidden fields.
func registerRedirect(req *http.Request) string {
	q := url.Values{}
	for _, k := range []string{"account", "type", "category", "q"} {
		if v := req.PostFormValue(k); v != "" && v != "0" {
			q.Set(k, v)
		}
	}
	if len(q) == 0 {
		return "/transactions"
	}
	return "/transactions?" + q.Encode()
}

// mapRegisterRows maps service register rows to their pre-formatted view shape
// (signed amount, formatted date) — shared by the full register (/transactions)
// and the dashboard recent-activity widget so the two never drift.
func mapRegisterRows(regRows []transaction.RegisterRow) []web.RegisterRow {
	rows := make([]web.RegisterRow, 0, len(regRows))
	for _, r := range regRows {
		rows = append(rows, web.RegisterRow{
			ID:          r.ID,
			Date:        r.Date.Format("02/01/2006"),
			Type:        string(r.Type),
			Description: r.Description,
			Category:    r.Category,
			Security:    r.Security,
			Account:     r.Account,
			Amount:      registerAmount(r),
			Incoming:    r.Incoming,
			IsTransfer:  r.IsTransfer,
			Editable:    r.Editable,
			Note:        r.Note,
			Tags:        r.Tags,
		})
	}
	return rows
}

// registerAmount composes a register row's amount string: signed for
// income/expense, neutral legs for transfers (presentation only).
func registerAmount(r transaction.RegisterRow) string {
	if r.IsTransfer {
		s := r.Amount.Display()
		if r.CrossCurrency {
			s += " → " + r.ToAmount.Display()
		}
		return s
	}
	if r.Incoming {
		return "+" + r.Amount.Display()
	}
	return "-" + r.Amount.Display()
}

func categoriesPage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		renderCategories(deps, w, req, "", http.StatusOK)
	}
}

func categoriesCreate(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, "requisição inválida", http.StatusBadRequest)
			return
		}
		name := req.PostFormValue("name")
		kind := category.Kind(req.PostFormValue("kind"))
		if _, err := deps.Categories.Create(req.Context(), name, kind); err != nil {
			renderCategories(deps, w, req, problemMsg(req, "Não foi possível criar a categoria. Verifique os dados e tente novamente.", err), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, "/categories", http.StatusSeeOther)
	}
}

func categoriesDelete(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, "requisição inválida", http.StatusBadRequest)
			return
		}
		id, err := strconv.ParseInt(req.PostFormValue("id"), 10, 64)
		if err != nil {
			renderCategories(deps, w, req, "ID de categoria inválido.", http.StatusBadRequest)
			return
		}
		force := req.PostFormValue("force") == "true"
		if err := deps.Categories.Delete(req.Context(), id, force); err != nil {
			renderCategories(deps, w, req, problemMsg(req, "Não foi possível excluir a categoria. Tente novamente.", err), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, "/categories", http.StatusSeeOther)
	}
}

func renderCategories(deps Deps, w http.ResponseWriter, req *http.Request, errMsg string, code int) {
	var rows []web.CategoryRow
	cs, err := deps.Categories.ListWithUsage(req.Context())
	if err != nil {
		logLoad(req, "categories list", err)
		errMsg, code = loadErrorMsg, http.StatusInternalServerError
	}
	for _, c := range cs {
		rows = append(rows, web.CategoryRow{ID: c.ID, Name: c.Name, Kind: string(c.Kind), Count: c.Count})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_ = web.CategoriesPage(shellData(deps, req.Context(), "settings"), rows, errMsg).Render(req.Context(), w)
}

func rulesPage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		renderRules(deps, w, req, "", http.StatusOK)
	}
}

func rulesCreate(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, "requisição inválida", http.StatusBadRequest)
			return
		}
		matchText := req.PostFormValue("match_text")
		categoryID, _ := strconv.ParseInt(req.PostFormValue("category_id"), 10, 64)
		if _, err := deps.CategoryRules.Add(req.Context(), matchText, categoryID); err != nil {
			renderRules(deps, w, req, problemMsg(req, "Não foi possível criar a regra. Verifique os dados e tente novamente.", err), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, "/categories/rules", http.StatusSeeOther)
	}
}

func rulesDelete(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, "requisição inválida", http.StatusBadRequest)
			return
		}
		id, err := strconv.ParseInt(req.PostFormValue("id"), 10, 64)
		if err != nil {
			renderRules(deps, w, req, "ID de regra inválido.", http.StatusBadRequest)
			return
		}
		if err := deps.CategoryRules.Delete(req.Context(), id); err != nil {
			renderRules(deps, w, req, problemMsg(req, "Não foi possível remover a regra. Tente novamente.", err), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, "/categories/rules", http.StatusSeeOther)
	}
}

func renderRules(deps Deps, w http.ResponseWriter, req *http.Request, errMsg string, code int) {
	var cats []web.CategoryOption
	if cs, err := deps.Categories.List(req.Context()); err != nil {
		logLoad(req, "rules categories list", err)
		errMsg, code = loadErrorMsg, http.StatusInternalServerError
	} else {
		for _, c := range cs {
			cats = append(cats, web.CategoryOption{ID: c.ID, Name: c.Name, Kind: string(c.Kind)})
		}
	}
	var rules []web.RuleRow
	if rs, err := deps.CategoryRules.List(req.Context()); err != nil {
		logLoad(req, "rules list", err)
		errMsg, code = loadErrorMsg, http.StatusInternalServerError
	} else {
		for _, r := range rs {
			rules = append(rules, web.RuleRow{ID: r.ID, MatchText: r.MatchText, CategoryName: r.CategoryName, Kind: r.Kind})
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_ = web.CategoryRulesPage(shellData(deps, req.Context(), "settings"), cats, rules, errMsg).Render(req.Context(), w)
}

func budgetsPage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		renderBudgets(deps, w, req, "", http.StatusOK)
	}
}

func budgetsSet(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, "requisição inválida", http.StatusBadRequest)
			return
		}
		categoryID, idErr := strconv.ParseInt(req.PostFormValue("category_id"), 10, 64)
		amount, amtErr := money.ParseDecimal(req.PostFormValue("amount"))
		if idErr != nil || amtErr != nil {
			renderBudgets(deps, w, req, "Informe uma categoria e um valor válidos.", http.StatusBadRequest)
			return
		}
		if err := deps.Budgets.Set(req.Context(), categoryID, amount); err != nil {
			renderBudgets(deps, w, req, problemMsg(req, "Não foi possível salvar o orçamento. Verifique os dados e tente novamente.", err), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, budgetsRedirect(req), http.StatusSeeOther)
	}
}

func budgetsDelete(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, "requisição inválida", http.StatusBadRequest)
			return
		}
		categoryID, err := strconv.ParseInt(req.PostFormValue("category_id"), 10, 64)
		if err != nil {
			renderBudgets(deps, w, req, "ID de categoria inválido.", http.StatusBadRequest)
			return
		}
		if err := deps.Budgets.Delete(req.Context(), categoryID); err != nil {
			renderBudgets(deps, w, req, problemMsg(req, "Não foi possível remover o orçamento. Tente novamente.", err), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, budgetsRedirect(req), http.StatusSeeOther)
	}
}

// budgetsRedirect keeps the owner on the month they were editing after a write.
func budgetsRedirect(req *http.Request) string {
	if m := req.PostFormValue("month"); m != "" {
		return "/budgets?month=" + url.QueryEscape(m)
	}
	return "/budgets"
}

// selectedBudgetMonth reads ?month=YYYY-MM (query or form), defaulting to the
// current month when absent or malformed.
func selectedBudgetMonth(req *http.Request) (int, time.Month, string) {
	if v := req.FormValue("month"); v != "" {
		if t, err := time.Parse("2006-01", v); err == nil {
			return t.Year(), t.Month(), v
		}
	}
	now := time.Now().UTC()
	return now.Year(), now.Month(), now.Format("2006-01")
}

func renderBudgets(deps Deps, w http.ResponseWriter, req *http.Request, errMsg string, code int) {
	year, month, monthValue := selectedBudgetMonth(req)

	// The Display Currency labels the form and drives the report; a load failure
	// here is a load failure and we must not render figures with a blank code.
	cur, err := deps.Settings.DisplayCurrency(req.Context())
	if err != nil {
		logLoad(req, "budgets display currency", err)
		errMsg, code = loadErrorMsg, http.StatusInternalServerError
	}
	var cats []web.CategoryOption
	if cs, err := deps.Categories.List(req.Context()); err != nil {
		logLoad(req, "budgets categories list", err)
		errMsg, code = loadErrorMsg, http.StatusInternalServerError
	} else {
		for _, c := range cs {
			cats = append(cats, web.CategoryOption{ID: c.ID, Name: c.Name, Kind: string(c.Kind)})
		}
	}
	var rows []web.BudgetViewRow
	var notice string
	if cur != "" {
		if rep, err := deps.Budgets.Report(req.Context(), year, month); err != nil {
			logLoad(req, "budgets report", err)
			errMsg, code = loadErrorMsg, http.StatusInternalServerError
		} else {
			for _, l := range rep.Lines {
				// Favorable: an expense on or under budget (remaining ≥ 0), or an
				// income that met or beat its target (remaining ≤ 0). A dead-on
				// zero remaining is favorable for either kind.
				sign := l.Remaining.Amount().Sign()
				favorable := (l.Kind == "expense" && sign >= 0) || (l.Kind == "income" && sign <= 0)
				rows = append(rows, web.BudgetViewRow{
					CategoryID: l.CategoryID,
					Name:       l.Name,
					Kind:       l.Kind,
					Target:     l.Target.Display(),
					Carryover:  l.Carryover.Display(),
					Planned:    l.Planned.Display(),
					Actual:     l.Actual.Display(),
					Remaining:  l.Remaining.Display(),
					Favorable:  favorable,
				})
			}
			if len(rep.Missing) > 0 {
				codes := make([]string, len(rep.Missing))
				for i, c := range rep.Missing {
					codes[i] = string(c)
				}
				notice = "Total parcial: sem cotação para " + strings.Join(codes, ", ") + " em algumas datas."
			}
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_ = web.BudgetsPage(shellData(deps, req.Context(), "settings"), monthValue, string(cur), cats, rows, notice, errMsg).Render(req.Context(), w)
}

func recurringPage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		form := defaultRecurringForm()
		// ?edit=ID pre-fills the form for that template (secondary: a bad/missing id
		// just falls back to the create form).
		if v := req.URL.Query().Get("edit"); v != "" {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil {
				if list, lErr := deps.Recurring.List(req.Context()); lErr == nil {
					for _, r := range list {
						if r.ID == id {
							form = toRecurringForm(r)
							break
						}
					}
				} else {
					logLoad(req, "recurring edit lookup", lErr)
				}
			}
		}
		renderRecurring(deps, w, req, form, "", http.StatusOK)
	}
}

func recurringCreate(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		in, ok := parseRecurringForm(req)
		if !ok {
			renderRecurring(deps, w, req, formFromRequest(req, 0), "Informe valores e datas válidos.", http.StatusBadRequest)
			return
		}
		if _, err := deps.Recurring.Create(req.Context(), in); err != nil {
			renderRecurring(deps, w, req, formFromRequest(req, 0), problemMsg(req, "Não foi possível salvar o recorrente. Verifique os dados e tente novamente.", err), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, "/recurring", http.StatusSeeOther)
	}
}

func recurringEdit(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := strconv.ParseInt(req.PostFormValue("id"), 10, 64)
		if err != nil {
			renderRecurring(deps, w, req, defaultRecurringForm(), "ID de recorrente inválido.", http.StatusBadRequest)
			return
		}
		in, ok := parseRecurringForm(req)
		if !ok {
			renderRecurring(deps, w, req, formFromRequest(req, id), "Informe valores e datas válidos.", http.StatusBadRequest)
			return
		}
		if err := deps.Recurring.Edit(req.Context(), id, in); err != nil {
			renderRecurring(deps, w, req, formFromRequest(req, id), problemMsg(req, "Não foi possível salvar o recorrente. Verifique os dados e tente novamente.", err), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, "/recurring", http.StatusSeeOther)
	}
}

func recurringDelete(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, "requisição inválida", http.StatusBadRequest)
			return
		}
		id, err := strconv.ParseInt(req.PostFormValue("id"), 10, 64)
		if err != nil {
			renderRecurring(deps, w, req, defaultRecurringForm(), "ID de recorrente inválido.", http.StatusBadRequest)
			return
		}
		if err := deps.Recurring.Delete(req.Context(), id); err != nil {
			renderRecurring(deps, w, req, defaultRecurringForm(), problemMsg(req, "Não foi possível remover o recorrente. Tente novamente.", err), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, "/recurring", http.StatusSeeOther)
	}
}

func recurringPost(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, occ, ok := parseRecurringAction(req)
		if !ok {
			renderRecurring(deps, w, req, defaultRecurringForm(), "Ocorrência inválida.", http.StatusBadRequest)
			return
		}
		if err := deps.Recurring.Post(req.Context(), id, occ); err != nil {
			renderRecurring(deps, w, req, defaultRecurringForm(), problemMsg(req, "Não foi possível lançar esta ocorrência. Tente novamente.", err), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, "/recurring", http.StatusSeeOther)
	}
}

func recurringSkip(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, occ, ok := parseRecurringAction(req)
		if !ok {
			renderRecurring(deps, w, req, defaultRecurringForm(), "Ocorrência inválida.", http.StatusBadRequest)
			return
		}
		if err := deps.Recurring.Skip(req.Context(), id, occ); err != nil {
			renderRecurring(deps, w, req, defaultRecurringForm(), problemMsg(req, "Não foi possível pular esta ocorrência. Tente novamente.", err), http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, "/recurring", http.StatusSeeOther)
	}
}

// defaultRecurringForm is the empty create form's sensible defaults: an expense,
// monthly, every 1, starting today.
func defaultRecurringForm() web.RecurringForm {
	return web.RecurringForm{Type: "expense", Cadence: "months", IntervalN: 1, StartDate: time.Now().Format("2006-01-02")}
}

// parseRecurringForm reads the create/edit form into a service Input. It returns
// ok=false only for a structurally invalid form (bad amount/interval/dates); the
// service is the authority for semantic validation.
func parseRecurringForm(req *http.Request) (recurring.Input, bool) {
	if err := req.ParseForm(); err != nil {
		return recurring.Input{}, false
	}
	amount, aErr := money.ParseDecimal(req.PostFormValue("amount"))
	interval, iErr := strconv.Atoi(req.PostFormValue("interval_n"))
	start, sErr := time.Parse("2006-01-02", req.PostFormValue("start_date"))
	if aErr != nil || iErr != nil || sErr != nil {
		return recurring.Input{}, false
	}
	in := recurring.Input{
		Type:          req.PostFormValue("type"),
		AccountID:     parseID(req.PostFormValue("account_id")),
		FromAccountID: parseID(req.PostFormValue("from_account_id")),
		ToAccountID:   parseID(req.PostFormValue("to_account_id")),
		Amount:        amount,
		CategoryID:    parseID(req.PostFormValue("category_id")),
		Cadence:       req.PostFormValue("cadence"),
		IntervalN:     interval,
		StartDate:     start,
		Description:   req.PostFormValue("description"),
	}
	// to_amount is only meaningful for a cross-currency transfer (optional).
	if v := strings.TrimSpace(req.PostFormValue("to_amount")); v != "" {
		if ta, err := money.ParseDecimal(v); err == nil {
			in.ToAmount = ta
		}
	}
	if v := strings.TrimSpace(req.PostFormValue("end_date")); v != "" {
		if end, err := time.Parse("2006-01-02", v); err == nil {
			in.EndDate = &end
		}
	}
	return in, true
}

// parseRecurringAction reads the id + occurrence date carried by a post/skip form.
func parseRecurringAction(req *http.Request) (int64, time.Time, bool) {
	if err := req.ParseForm(); err != nil {
		return 0, time.Time{}, false
	}
	id, err := strconv.ParseInt(req.PostFormValue("id"), 10, 64)
	if err != nil {
		return 0, time.Time{}, false
	}
	occ, err := time.Parse("2006-01-02", req.PostFormValue("occurrence"))
	if err != nil {
		return 0, time.Time{}, false
	}
	return id, occ, true
}

// formFromRequest re-reads a submitted form back into RecurringForm so a failed
// create/edit re-renders with the owner's entries intact (id 0 ⇒ create).
func formFromRequest(req *http.Request, id int64) web.RecurringForm {
	_ = req.ParseForm()
	interval, _ := strconv.Atoi(req.PostFormValue("interval_n"))
	if interval < 1 {
		interval = 1
	}
	return web.RecurringForm{
		ID:            id,
		Type:          req.PostFormValue("type"),
		AccountID:     parseID(req.PostFormValue("account_id")),
		FromAccountID: parseID(req.PostFormValue("from_account_id")),
		ToAccountID:   parseID(req.PostFormValue("to_account_id")),
		Amount:        req.PostFormValue("amount"),
		ToAmount:      req.PostFormValue("to_amount"),
		CategoryID:    parseID(req.PostFormValue("category_id")),
		Cadence:       req.PostFormValue("cadence"),
		IntervalN:     interval,
		StartDate:     req.PostFormValue("start_date"),
		EndDate:       req.PostFormValue("end_date"),
		Description:   req.PostFormValue("description"),
	}
}

// toRecurringForm pre-fills the edit form from a stored template. The single
// income/expense account is derived from the money leg; amounts render in the
// pt-BR input convention and dates as the HTML date-input value.
func toRecurringForm(r recurring.Recurring) web.RecurringForm {
	f := web.RecurringForm{
		ID:            r.ID,
		Type:          r.Type,
		FromAccountID: r.FromAccountID,
		ToAccountID:   r.ToAccountID,
		Amount:        money.FormatDecimal(r.Amount.Amount()),
		CategoryID:    r.CategoryID,
		Cadence:       r.Cadence,
		IntervalN:     r.IntervalN,
		StartDate:     r.StartDate.Format("2006-01-02"),
		Description:   r.Description,
	}
	switch r.Type {
	case recurring.Income:
		f.AccountID = r.ToAccountID
	case recurring.Expense:
		f.AccountID = r.FromAccountID
	case recurring.Transfer:
		if r.CrossCurrency {
			f.ToAmount = money.FormatDecimal(r.ToAmount.Amount())
		}
	}
	if r.EndDate != nil {
		f.EndDate = r.EndDate.Format("2006-01-02")
	}
	return f
}

func renderRecurring(deps Deps, w http.ResponseWriter, req *http.Request, form web.RecurringForm, errMsg string, code int) {
	var templates, due []web.RecurringRow
	if list, err := deps.Recurring.List(req.Context()); err != nil {
		logLoad(req, "recurring list", err)
		errMsg, code = loadErrorMsg, http.StatusInternalServerError
	} else {
		for _, r := range list {
			row := mapRecurringRow(r)
			templates = append(templates, row)
			if r.Due {
				due = append(due, row)
			}
		}
	}

	// The form's account + category pickers are secondary: a failure degrades to
	// empty dropdowns (logged), it does not fail the page.
	var accounts []web.FilterOption
	if accts, err := deps.Accounts.List(req.Context(), false); err != nil {
		logLoad(req, "recurring accounts", err)
	} else {
		for _, a := range accts {
			accounts = append(accounts, web.FilterOption{ID: a.ID, Label: a.Name})
		}
	}
	var cats []web.CategoryOption
	if cs, err := deps.Categories.List(req.Context()); err != nil {
		logLoad(req, "recurring categories", err)
	} else {
		for _, c := range cs {
			cats = append(cats, web.CategoryOption{ID: c.ID, Name: c.Name, Kind: string(c.Kind)})
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_ = web.RecurringPage(shellData(deps, req.Context(), "recurring"), form, accounts, cats, due, templates, errMsg).Render(req.Context(), w)
}

// mapRecurringRow formats a service template for display.
func mapRecurringRow(r recurring.Recurring) web.RecurringRow {
	row := web.RecurringRow{
		ID:            r.ID,
		Type:          r.Type,
		TypeLabel:     recurringTypeLabel(r.Type),
		Category:      r.CategoryName,
		Amount:        r.Amount.Display(),
		CrossCurrency: r.CrossCurrency,
		Cadence:       cadencePhrase(r.Cadence, r.IntervalN),
		StartDate:     r.StartDate.Format("02/01/2006"),
		EndDate:       "—",
		NextDue:       r.NextDue.Format("02/01/2006"),
		NextDueValue:  r.NextDue.Format("2006-01-02"),
		Description:   r.Description,
		Due:           r.Due,
	}
	switch r.Type {
	case recurring.Income:
		row.Account = r.ToAccount
	case recurring.Expense:
		row.Account = r.FromAccount
	case recurring.Transfer:
		row.Account = r.FromAccount + " → " + r.ToAccount
		if r.CrossCurrency {
			row.ToAmount = r.ToAmount.Display()
		}
	}
	if r.EndDate != nil {
		row.EndDate = r.EndDate.Format("02/01/2006")
	}
	return row
}

// recurringTypeLabel maps a template type to its pt-BR label.
func recurringTypeLabel(t string) string {
	switch t {
	case recurring.Income:
		return "Receita"
	case recurring.Transfer:
		return "Transferência"
	default:
		return "Despesa"
	}
}

// cadencePhrase renders "a cada N unidade(s)" in pt-BR.
func cadencePhrase(cadence string, n int) string {
	unit := map[string]string{"weeks": "semana(s)", "months": "mês(es)", "years": "ano(s)"}[cadence]
	return "a cada " + strconv.Itoa(n) + " " + unit
}

// parseID parses a form id, treating blank/"0"/invalid as 0 (no selection).
func parseID(s string) int64 {
	id, _ := strconv.ParseInt(s, 10, 64)
	return id
}

func analyticsPage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		months := analyticsMonths(req)
		v := web.AnalyticsView{Ranges: analyticsRanges(months)}

		cur, err := deps.Settings.DisplayCurrency(req.Context())
		if err != nil {
			logLoad(req, "analytics display currency", err)
			renderError(deps, w, req, "analytics", loadErrorMsg)
			return
		}
		v.Display = string(cur)

		rep, err := deps.Analytics.Report(req.Context(), months)
		if err != nil {
			logLoad(req, "analytics report", err)
			renderError(deps, w, req, "analytics", loadErrorMsg)
			return
		}

		for _, s := range rep.Spending {
			name := s.Category
			if name == "" {
				name = "Sem categoria"
			}
			v.Spending = append(v.Spending, web.SpendRow{Category: name, Value: s.Total.Display(), Percent: s.Percent})
		}

		// Scale the cash-flow bars to the tallest income/expense in the window.
		maxV := decimal.Zero
		for _, f := range rep.Flow {
			if a := f.Income.Amount(); a.GreaterThan(maxV) {
				maxV = a
			}
			if a := f.Expense.Amount(); a.GreaterThan(maxV) {
				maxV = a
			}
		}
		v.HasFlow = maxV.IsPositive()
		for _, f := range rep.Flow {
			v.Flow = append(v.Flow, web.FlowBar{
				Label:    ptMonthLabel(f.Year, f.Month),
				Income:   f.Income.Display(),
				Expense:  f.Expense.Display(),
				IncomeH:  barPct(f.Income.Amount(), maxV),
				ExpenseH: barPct(f.Expense.Amount(), maxV),
			})
		}

		if len(rep.Missing) > 0 {
			codes := make([]string, len(rep.Missing))
			for i, c := range rep.Missing {
				codes[i] = string(c)
			}
			v.Missing = strings.Join(codes, ", ")
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = web.AnalyticsPage(shellData(deps, req.Context(), "analytics"), v).Render(req.Context(), w)
	}
}

// analyticsMonths reads ?months, accepting only 6/12/24 and defaulting to 12.
func analyticsMonths(req *http.Request) int {
	switch req.URL.Query().Get("months") {
	case "6":
		return 6
	case "24":
		return 24
	default:
		return 12
	}
}

// analyticsRanges builds the months range toggle, marking the active window.
func analyticsRanges(active int) []web.ChartRange {
	out := make([]web.ChartRange, 0, 3)
	for _, n := range []int{6, 12, 24} {
		key := strconv.Itoa(n)
		out = append(out, web.ChartRange{
			Key:    key,
			Label:  key + " meses",
			Href:   "/analytics?months=" + key,
			Active: n == active,
		})
	}
	return out
}

// barPct is v as an integer percentage of max (0 when max is non-positive).
func barPct(v, maxV decimal.Decimal) int {
	if !maxV.IsPositive() {
		return 0
	}
	return int(v.Mul(decimal.NewFromInt(100)).Div(maxV).IntPart())
}

// ptMonthAbbrev holds the Brazilian three-letter month abbreviations (index 0 =
// January) for the cash-flow axis labels.
var ptMonthAbbrev = [...]string{"jan", "fev", "mar", "abr", "mai", "jun", "jul", "ago", "set", "out", "nov", "dez"}

// ptMonthLabel renders a month as "abr/26" (pt-BR abbreviation + 2-digit year).
func ptMonthLabel(year int, m time.Month) string {
	return fmt.Sprintf("%s/%02d", ptMonthAbbrev[int(m)-1], year%100)
}

func categorySummary(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := parsePathID(req)
		if !ok {
			http.NotFound(w, req)
			return
		}
		// Resolve the category name from the list (no single-Get on the iface). A
		// DB error here is a load failure (500), distinct from a genuinely unknown
		// id (404) — never let an outage masquerade as "not found".
		cs, err := deps.Categories.List(req.Context())
		if err != nil {
			logLoad(req, "category summary lookup", err)
			renderError(deps, w, req, "settings", loadErrorMsg)
			return
		}
		name := ""
		var kind string
		for _, c := range cs {
			if c.ID == id {
				name, kind = c.Name, string(c.Kind)
			}
		}
		if name == "" {
			http.NotFound(w, req)
			return
		}
		txns, sums, err := deps.Transactions.CategoryTransactions(req.Context(), id)
		if err != nil {
			logLoad(req, "category transactions", err)
			renderError(deps, w, req, "settings", loadErrorMsg)
			return
		}
		var rows []web.CategoryTxRow
		var totals []string
		for _, t := range txns {
			rows = append(rows, web.CategoryTxRow{
				Account:     t.AccountName,
				Date:        t.Date.Format("02/01/2006"),
				Description: t.Description,
				Amount:      t.Amount.Display(),
			})
		}
		for _, m := range sums {
			totals = append(totals, m.Display())
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
			http.Error(w, "requisição inválida", http.StatusBadRequest)
			return
		}
		symbol := req.PostFormValue("symbol")
		name := req.PostFormValue("name")
		typ := security.SecurityType(req.PostFormValue("type"))
		quote := money.Currency(req.PostFormValue("quote_currency"))
		if _, err := deps.Securities.Create(req.Context(), symbol, name, typ, quote); err != nil {
			renderSecurities(deps, w, req, problemMsg(req, "Não foi possível adicionar o ativo. Verifique os dados e tente novamente.", err), http.StatusBadRequest)
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
	secs, err := deps.Securities.List(req.Context())
	if err != nil {
		logLoad(req, "securities list", err)
		errMsg, code = loadErrorMsg, http.StatusInternalServerError
	}
	for _, s := range secs {
		rows = append(rows, web.SecurityRow{
			Symbol:        s.Symbol,
			Name:          s.Name,
			TypeLabel:     securityTypeLabel(s.Type),
			QuoteCurrency: string(s.QuoteCurrency),
		})
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
	amount, aErr := money.ParseDecimal(req.PostFormValue("amount"))
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
		// A genuinely unknown id is a 404; a DB outage is a load error (500) — never
		// let an outage masquerade as "not found".
		if errors.Is(err, account.ErrNotFound) {
			http.NotFound(w, req)
			return
		}
		logLoad(req, "account detail lookup", err)
		renderError(deps, w, req, "accounts", loadErrorMsg)
		return
	}
	if account.AccountType(acct.Type) == account.Investment {
		renderInvestmentDetail(deps, w, req, acct, errMsg, code)
		return
	}
	// Credit accounts present their balance as a positive amount owed (a
	// liability); cash/investment show the signed balance. The owed figure is
	// produced by domain (AD-10) — http only renders it.
	balLabel := "Saldo"
	balStr := ""
	bal, bErr := deps.Transactions.Balance(req.Context(), acctID)
	if bErr != nil {
		logLoad(req, "account balance", bErr)
		errMsg, code = loadErrorMsg, http.StatusInternalServerError
	} else if account.AccountType(acct.Type) == account.Credit {
		balLabel = "Saldo devedor"
		balStr = domain.AmountOwed(bal).Display()
	} else {
		balStr = bal.Display()
	}
	var rows []web.TxRow
	var edit web.TxRow
	editing := false
	txns, lErr := deps.Transactions.List(req.Context(), acctID)
	if lErr != nil {
		logLoad(req, "account transactions", lErr)
		errMsg, code = loadErrorMsg, http.StatusInternalServerError
	}
	{
		for _, t := range txns {
			sign := "-"
			if t.Incoming {
				sign = "+"
			}
			row := web.TxRow{
				ID:           t.ID,
				Type:         string(t.Type),
				Date:         t.Date.Format("02/01/2006"),
				EditDate:     t.Date.Format("2006-01-02"),
				Description:  t.Description,
				Counterparty: t.Counterparty,
				Category:     t.CategoryName,
				CategoryID:   t.CategoryID,
				Amount:       money.FormatDecimal(t.Amount), // pt-BR: prefills the edit-form <input>, re-parsed by money.ParseDecimal
				Signed:       sign + money.New(t.Amount, acct.Currency).Display(),
				Incoming:     t.Incoming,
				Editable:     t.Type != transaction.Transfer,
				Note:         t.Note,
				Tags:         t.Tags,
				TagsCSV:      strings.Join(t.Tags, ", "),
			}
			rows = append(rows, row)
			if editID != 0 && t.ID == editID && row.Editable {
				edit = row
				editing = true
			}
		}
	}
	if !editing {
		edit = web.TxRow{Type: string(transaction.Income), EditDate: time.Now().Format("2006-01-02")}
	}
	types := []string{string(transaction.Income), string(transaction.Expense)}

	// Transfer targets: the owner's other active accounts (secondary — degrade).
	var targets []web.TransferTarget
	if accts, aErr := deps.Accounts.List(req.Context(), false); aErr != nil {
		logLoad(req, "transfer targets", aErr)
	} else {
		for _, a := range accts {
			if a.ID == acctID {
				continue
			}
			targets = append(targets, web.TransferTarget{ID: a.ID, Name: a.Name, Currency: string(a.Currency)})
		}
	}

	// Category options for the income/expense form (secondary — degrade).
	var cats []web.CategoryOption
	if deps.Categories != nil {
		if cs, cErr := deps.Categories.List(req.Context()); cErr != nil {
			logLoad(req, "account detail categories", cErr)
		} else {
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
	if bal, bErr := deps.Transactions.Balance(req.Context(), acct.ID); bErr != nil {
		logLoad(req, "investment balance", bErr)
		errMsg, code = loadErrorMsg, http.StatusInternalServerError
	} else {
		balStr = bal.Display()
	}

	var holdings []web.HoldingRow
	realized := ""
	oversold := ""
	if hs, rg, os, hErr := deps.Transactions.Holdings(req.Context(), acct.ID); hErr != nil {
		logLoad(req, "investment holdings", hErr)
		errMsg, code = loadErrorMsg, http.StatusInternalServerError
	} else {
		// Inconsistent (oversold) positions are surfaced as a warning while the good
		// holdings still render (per-security isolation).
		oversold = strings.Join(os, ", ")
		for _, h := range hs {
			row := web.HoldingRow{
				Symbol:       h.Symbol,
				Name:         h.Name,
				Quantity:     money.FormatDecimal(h.Quantity),
				AvgCost:      h.AvgCost.Display(),
				CostBasis:    h.CostBasis.Display(),
				RealizedGain: h.RealizedGain.Display(),
				HasPrice:     h.HasPrice,
			}
			if h.HasPrice {
				row.Price = h.Price.Display()
				row.PriceDate = h.PriceDate.Format("02/01/2006")
				row.MarketValue = h.MarketValue.Display()
				row.UnrealizedGain = h.UnrealizedGain.Display()
				row.UnrealizedPositive = h.UnrealizedGain.Amount().IsPositive()
				row.UnrealizedNegative = h.UnrealizedGain.Amount().IsNegative()
			}
			holdings = append(holdings, row)
		}
		realized = rg.Display()
	}

	var rows []web.TxRow
	if txns, lErr := deps.Transactions.List(req.Context(), acct.ID); lErr != nil {
		logLoad(req, "investment transactions", lErr)
		errMsg, code = loadErrorMsg, http.StatusInternalServerError
	} else {
		for _, t := range txns {
			sign := "-"
			if t.Incoming {
				sign = "+"
			}
			rows = append(rows, web.TxRow{
				ID:          t.ID,
				Type:        string(t.Type),
				Date:        t.Date.Format("02/01/2006"),
				Description: t.Description,
				Security:    t.Security,
				Quantity:    money.FormatDecimal(t.Quantity),
				Price:       money.FormatDecimal(t.Price),
				Signed:      sign + money.New(t.Amount, acct.Currency).Display(),
				Incoming:    t.Incoming,
				Editable:    false, // trades corrected via delete + re-add
			})
		}
	}

	// Tradeable securities: same currency as the account (secondary — degrade).
	var securities []web.SecurityChoice
	if secs, sErr := deps.Securities.List(req.Context()); sErr != nil {
		logLoad(req, "investment securities dropdown", sErr)
	} else {
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

// dashboardPage renders the post-login KPI card row (Story 5.2, FR-11/UX-DR2):
// Net Worth, Portfolio Value, Total Gain/Loss and Cash in the Display Currency,
// each with a period-change delta against the prior-sample baseline. All figures
// and flags come pre-computed from the valuation service (AD-1/AD-10) — this
// handler only formats money and copies flags into the view. A load failure
// surfaces a graceful banner (oversold ledger gets a specific hint), never a
// blank page, mirroring investmentsPage.
func dashboardPage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		d, err := deps.Valuation.Dashboard(req.Context())
		if err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			_ = web.DashboardPage(shellData(deps, req.Context(), "dashboard"), web.DashboardView{ErrMsg: "Não foi possível carregar seu painel agora. Tente novamente."}).Render(req.Context(), w)
			return
		}

		view := web.DashboardView{
			Cards: []web.KPICardView{
				kpiCard("Patrimônio líquido", "networth", d.NetWorth),
				kpiCard("Valor da carteira", "portfolio", d.Portfolio),
				kpiCard("Ganho/perda total", "gainloss", d.GainLoss),
				kpiCard("Caixa", "cash", d.Cash),
			},
		}
		if len(d.Missing) > 0 {
			codes := make([]string, len(d.Missing))
			for i, c := range d.Missing {
				codes[i] = string(c)
			}
			view.MissingCodes = strings.Join(codes, ", ")
		}
		if len(d.Unpriced) > 0 {
			view.UnpricedSymbols = strings.Join(d.Unpriced, ", ")
		}
		if len(d.Oversold) > 0 {
			view.OversoldSymbols = strings.Join(d.Oversold, ", ")
		}

		// Value-over-time trend chart (Story 5.3). A series failure degrades to an
		// empty chart card (the KPIs still render) — with a distinct "couldn't load"
		// message so an error is never mistaken for "no history yet". The active
		// allocation dimension (by) is threaded into the range links so switching
		// the range preserves the chosen breakdown (Story 5.4).
		rng := chartRange(req.URL.Query().Get("range"))
		by := valuation.AllocBy(req.URL.Query().Get("by"))
		points, sErr := deps.Valuation.ValueSeries(req.Context(), chartFrom(rng))
		view.Chart = buildChart(points, rng, by)
		if sErr != nil {
			view.Chart = buildChart(nil, rng, by)
			view.Chart.Empty = "Não foi possível carregar o gráfico de evolução agora. Tente novamente."
		}

		// Allocation breakdown (Story 5.4). Same graceful degradation: an error
		// shows a distinct "couldn't load" message, never the "no holdings" empty.
		alloc, aErr := deps.Valuation.Allocation(req.Context(), by)
		view.Allocation = buildAllocation(alloc, rng)
		if aErr != nil {
			view.Allocation = buildAllocation(valuation.Allocation{By: by}, rng)
			view.Allocation.Empty = "Não foi possível carregar sua alocação agora. Tente novamente."
		}

		// Insight call-out (Story 5.5, UX-DR6) — the month-over-month Net Worth
		// change. A load failure simply hides it (the page still renders).
		if ins, iErr := deps.Valuation.Insight(req.Context()); iErr == nil {
			view.Insight = buildInsight(ins)
		}

		// Recurring nudge (Story 9.2) — how many templates are due to post. A load
		// failure just hides the nudge (the page still renders).
		if deps.Recurring != nil {
			if dueRows, dErr := deps.Recurring.Due(req.Context()); dErr == nil {
				view.DueCount = len(dueRows)
			} else {
				logLoad(req, "dashboard recurring due", dErr)
			}
		}

		// Recent-activity widget (Story 5.5, UX-DR5) — the newest ledger rows,
		// reusing the register read + row mapping; take the top recentTxLimit. A
		// load failure leaves it empty (the widget renders its empty state).
		if regRows, rErr := deps.Transactions.Register(req.Context(), transaction.RegisterFilter{}); rErr == nil {
			if len(regRows) > recentTxLimit {
				regRows = regRows[:recentTxLimit]
			}
			view.Recent = mapRegisterRows(regRows)
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = web.DashboardPage(shellData(deps, req.Context(), "dashboard"), view).Render(req.Context(), w)
	}
}

// chartRange normalizes the ?range= query value to a supported key, defaulting
// to "1y" (a sensible window once over a year of history exists; with less it
// naturally shows everything).
func chartRange(v string) string {
	switch v {
	case "1m", "3m", "1y", "all":
		return v
	default:
		return "1y"
	}
}

// chartFrom maps a range key to the window-start passed to ValueSeries. "all"
// returns the zero time (full history). The service normalizes the date.
func chartFrom(rng string) time.Time {
	now := time.Now()
	switch rng {
	case "1m":
		return now.AddDate(0, -1, 0)
	case "3m":
		return now.AddDate(0, -3, 0)
	case "1y":
		return now.AddDate(-1, 0, 0)
	default: // "all"
		return time.Time{}
	}
}

// chart viewBox geometry: a 1000×300 box with padding so the line clears the edges.
const (
	chartW   = 1000
	chartH   = 300
	chartPad = 24
)

// buildChart turns the Net Worth series into ready-to-render SVG geometry
// (presentation only — AD-1; the financial figures arrive as money.Money). It
// maps each point to integer viewBox coordinates (value→y via decimal ratio, no
// float in the core) and emits the line polyline + filled area path, axis labels,
// the range toggle, and the partial note. Fewer than two points → the empty state.
func buildChart(points []valuation.SeriesPoint, active, by string) web.ChartView {
	ranges := []web.ChartRange{{Key: "1m", Label: "1M"}, {Key: "3m", Label: "3M"}, {Key: "1y", Label: "1Y"}, {Key: "all", Label: "All"}}
	for i := range ranges {
		ranges[i].Href = "/?range=" + ranges[i].Key + "&by=" + by // preserve the allocation dimension (5.4)
		ranges[i].Active = ranges[i].Key == active
	}
	cv := web.ChartView{Range: active, Ranges: ranges}
	if len(points) > 0 {
		cv.Display = string(points[0].Value.Currency())
	}
	if len(points) < 2 {
		cv.Empty = "Ainda não há histórico suficiente — adicione preços e taxas de câmbio ao longo do tempo e sua evolução aparecerá aqui."
		return cv
	}

	cur := points[0].Value.Currency()
	lo, hi := points[0].Value.Amount(), points[0].Value.Amount()
	for _, p := range points {
		a := p.Value.Amount()
		if a.LessThan(lo) {
			lo = a
		}
		if a.GreaterThan(hi) {
			hi = a
		}
		if p.Partial {
			cv.Partial = true
		}
	}
	span := hi.Sub(lo)
	drawH := decimal.NewFromInt(chartH - 2*chartPad)
	baseline := chartH - chartPad
	n := len(points)

	var line strings.Builder
	var area strings.Builder
	cps := make([]web.ChartPoint, n)
	for i, p := range points {
		x := chartPad + (chartW-2*chartPad)*i/(n-1)
		y := chartH / 2 // flat line when every value is equal
		if !span.IsZero() {
			ratio := hi.Sub(p.Value.Amount()).Div(span) // 0 at the max (top), 1 at the min (bottom)
			y = chartPad + int(ratio.Mul(drawH).IntPart())
		}
		if i > 0 {
			line.WriteByte(' ')
		}
		fmt.Fprintf(&line, "%d,%d", x, y)
		if i == 0 {
			fmt.Fprintf(&area, "M%d,%d L%d,%d", x, baseline, x, y)
		} else {
			fmt.Fprintf(&area, " L%d,%d", x, y)
		}
		cps[i] = web.ChartPoint{X: x, Y: y, Date: p.Date.Format("02/01/2006"), Value: p.Value.Display()}
	}
	fmt.Fprintf(&area, " L%d,%d Z", cps[n-1].X, baseline)

	cv.HasData = true
	cv.Line = line.String()
	cv.Area = area.String()
	cv.Points = cps
	cv.MinLabel = money.New(lo, cur).Display()
	cv.MaxLabel = money.New(hi, cur).Display()
	cv.StartLabel = cps[0].Date
	cv.EndLabel = cps[n-1].Date
	return cv
}

// Allocation donut geometry (Story 5.4, D2): a ring of radius allocR drawn with
// per-slice stroke-dasharray on overlaid circles — no trig, π is the only
// constant. This is presentation (AD-1) and lives in the http layer, outside the
// nofloat scope; the financial figures (percents, values) arrive pre-computed.
const (
	allocCenter = 100 // viewBox is "0 0 200 200"; centre at (100,100)
	allocR      = 70  // ring radius
	allocStroke = 30  // ring thickness
)

// allocPi is π to enough digits for sub-pixel arc lengths (http-layer geometry,
// not the financial core — NFR-5 is unaffected).
var allocPi = decimal.RequireFromString("3.14159265358979")

// allocPalette is the categorical slice colour set (Story 5.4) — calm, distinct
// hues defined as --color-alloc-N theme tokens and safelisted in input.css. It is
// NOT the gain/loss palette (reserved). Cycled by slice index.
var allocPalette = []string{"alloc-1", "alloc-2", "alloc-3", "alloc-4", "alloc-5", "alloc-6", "alloc-7", "alloc-8"}

// buildAllocation turns the invested-value breakdown into ready-to-render donut
// geometry + a legend (presentation only — AD-1; percents/values are computed by
// domain.Allocate). The ?by= toggle links preserve the active chart range. No
// groups → the empty state. The arcs use the reconciled integer percents (which
// sum to 100), so the ring is whole.
func buildAllocation(a valuation.Allocation, rng string) web.AllocationView {
	bys := []web.AllocBy{{Key: "security", Label: "Ativo"}, {Key: "account", Label: "Conta"}}
	active := valuation.AllocBy(a.By)
	for i := range bys {
		bys[i].Href = "/?range=" + rng + "&by=" + bys[i].Key
		bys[i].Active = bys[i].Key == active
	}
	av := web.AllocationView{By: active, Bys: bys, Display: string(a.Display)}
	if len(a.Missing) > 0 {
		codes := make([]string, len(a.Missing))
		for i, c := range a.Missing {
			codes[i] = string(c)
		}
		av.Partial = true
		av.MissingCodes = strings.Join(codes, ", ")
	}
	if len(a.Groups) == 0 {
		av.Empty = "Ainda não há posições investidas para alocar — adicione ativos, transações e preços e sua distribuição aparecerá aqui."
		return av
	}

	av.HasData = true
	av.Total = a.Total.Display()
	circumference := allocPi.Mul(decimal.NewFromInt(2 * allocR)) // 2πr
	hundred := decimal.NewFromInt(100)
	cum := 0
	slices := make([]web.AllocSliceView, len(a.Groups))
	for i, g := range a.Groups {
		arc := decimal.NewFromInt(int64(g.Percent)).Mul(circumference).Div(hundred)
		gap := circumference.Sub(arc)
		offset := decimal.NewFromInt(int64(cum)).Mul(circumference).Div(hundred).Neg()
		base := allocPalette[i%len(allocPalette)]
		slices[i] = web.AllocSliceView{
			DashArray:  arc.StringFixed(3) + " " + gap.StringFixed(3),
			DashOffset: offset.StringFixed(3),
			Stroke:     "stroke-" + base,
			Swatch:     "bg-" + base,
			Key:        g.Key,
			Percent:    g.Percent,
			Value:      g.Value.Display(),
		}
		cum += g.Percent
	}
	av.Slices = slices
	return av
}

// recentTxLimit caps the dashboard's recent-activity widget to the newest N
// ledger rows (Story 5.5, UX-DR5); the full list lives at /transactions.
const recentTxLimit = 5

// buildInsight frames the month-over-month Net Worth insight into the bold accent
// call-out (Story 5.5, UX-DR6). The percentage is the canonical domain figure
// (valuation.Insight via domain.PercentChange); this only composes the sentence
// and copies the direction flags (AD-1 — no math). No baseline → a calm fallback.
func buildInsight(ins valuation.Insight) web.InsightView {
	if !ins.HasData {
		return web.InsightView{
			Empty: "Adicione transações e preços ao longo do mês e a evolução do seu patrimônio aparecerá aqui.",
		}
	}
	// "Seu patrimônio subiu/caiu X,X% neste mês" (estável quando sem variação). The
	// percentage is the canonical domain figure, shown at 1 dp in Brazilian format.
	text := "Seu patrimônio está estável neste mês"
	if ins.Up || ins.Down {
		verb := "subiu"
		if ins.Down {
			verb = "caiu"
		}
		text = "Seu patrimônio " + verb + " " + money.FormatDecimalFixed(ins.Pct.Abs(), 1) + "% neste mês"
	}
	return web.InsightView{
		HasData:  true,
		Text:     text,
		NetWorth: ins.NetWorth.Display(),
		Up:       ins.Up,
		Down:     ins.Down,
		Partial:  ins.Partial,
	}
}

// kpiCard maps a valuation.KPI into its pre-formatted view row: the money string
// + gain/loss flags for the Amount primitive, and the period-change delta. The
// web layer does no math (AD-1).
func kpiCard(label, icon string, k valuation.KPI) web.KPICardView {
	// When a gain/loss flag is set, the Amount primitive supplies the +/− glyph,
	// so pass the MAGNITUDE to avoid a double sign (e.g. "−-100.0000 BRL"). The
	// unflagged value cards keep their signed string, so a negative Net Worth
	// still shows its own "−".
	disp := k.Value.Display()
	if k.Positive || k.Negative {
		disp = k.Value.Abs().Display()
	}
	return web.KPICardView{
		Label: label,
		Icon:  icon,
		Amount: web.MoneyText{
			Display:  disp,
			Positive: k.Positive,
			Negative: k.Negative,
		},
		Delta: deltaText(k),
	}
}

// deltaText formats a KPI's period change for display: a magnitude percentage
// (the ▲/▼ arrow carries direction) with up/down flags, or the "—" empty state
// when no comparable prior sample exists.
func deltaText(k valuation.KPI) web.DeltaText {
	if !k.HasDelta {
		return web.DeltaText{None: true}
	}
	return web.DeltaText{
		Display: money.FormatDecimalFixed(k.DeltaPct.Abs(), 1) + "%",
		Up:      k.DeltaUp,
		Down:    k.DeltaDown,
	}
}

// investmentsPage renders the cross-account portfolio & Net Worth view (Story
// 4.4): Display-Currency Net Worth + Portfolio value (convert-then-sum, AD-12),
// per-currency realized G/L, per-holding native valuation, and the partial-total
// notices (missing rate / unpriced). The layer only renders — all money is
// formatted by domain/service (AD-10/AD-1). A top-level load failure surfaces a
// graceful banner rather than a blank page (oversold ledger gets a specific hint).
func investmentsPage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		p, err := deps.Valuation.Portfolio(req.Context())
		if err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			_ = web.InvestmentsPage(shellData(deps, req.Context(), "investments"), web.InvestmentsView{ErrMsg: "Não foi possível carregar sua carteira agora. Tente novamente."}).Render(req.Context(), w)
			return
		}

		view := web.InvestmentsView{
			NetWorth:       p.NetWorth.Display(),
			PortfolioValue: p.PortfolioValue.Display(),
			Display:        string(p.Display),
		}
		for _, m := range p.RealizedByCurrency {
			view.Realized = append(view.Realized, web.RealizedChip{
				Amount:   m.Display(),
				Positive: m.Amount().IsPositive(),
				Negative: m.Amount().IsNegative(),
			})
		}
		if len(p.Missing) > 0 {
			codes := make([]string, len(p.Missing))
			for i, c := range p.Missing {
				codes[i] = string(c)
			}
			view.MissingCodes = strings.Join(codes, ", ")
		}
		if len(p.Unpriced) > 0 {
			view.UnpricedSymbols = strings.Join(p.Unpriced, ", ")
		}
		if len(p.Oversold) > 0 {
			view.OversoldSymbols = strings.Join(p.Oversold, ", ")
		}
		for _, h := range p.Holdings {
			row := web.PortfolioHoldingRow{
				Account:   h.AccountName,
				Symbol:    h.Symbol,
				Name:      h.Name,
				Currency:  string(h.Currency),
				Quantity:  money.FormatDecimal(h.Quantity),
				CostBasis: h.CostBasis.Display(),
				HasPrice:  h.HasPrice,
			}
			if h.HasPrice {
				row.Price = h.Price.Display()
				row.PriceDate = h.PriceDate.Format("02/01/2006")
				row.Valuation = h.Valuation.Display()
				row.UnrealizedGain = h.UnrealizedGain.Display()
				row.UnrealizedPositive = h.UnrealizedGain.Amount().IsPositive()
				row.UnrealizedNegative = h.UnrealizedGain.Amount().IsNegative()
			}
			view.Holdings = append(view.Holdings, row)
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = web.InvestmentsPage(shellData(deps, req.Context(), "investments"), view).Render(req.Context(), w)
	}
}

// shellData builds the shared shell state, including the current Display
// Currency when a Settings service is wired.
func shellData(deps Deps, ctx context.Context, active string) web.ShellData {
	dc := ""
	if deps.Settings != nil {
		// The greeting currency is secondary on every page — a failure degrades to
		// a blank currency (logged), never a 500 (that would take down all pages).
		if c, err := deps.Settings.DisplayCurrency(ctx); err != nil {
			log.Printf("http: shell display currency: %v", err)
		} else {
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

// exportData streams the owner's authored-data backup as a JSON file download
// (Story 6.1, FR-15). The backup service assembles a consistent snapshot (AD-2 —
// authored rows only); this handler only sets the download headers and encodes.
// A service error yields a graceful 500 with no partial body (the JSON is only
// written once assembly succeeds).
func exportData(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		exp, err := deps.Backup.Export(req.Context())
		if err != nil {
			http.Error(w, "Não foi possível exportar seus dados agora. Tente novamente.", http.StatusInternalServerError)
			return
		}
		filename := "financas-export-" + time.Now().UTC().Format("2006-01-02") + ".json"
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(exp)
	}
}

// restoreMaxBytes caps the uploaded export size accepted by /restore (owner-scale
// data; a generous ceiling that still bounds memory).
const restoreMaxBytes = 32 << 20 // 32 MiB

// restoreData replaces the instance's authored data from an uploaded 6.1 export
// (Story 6.2). The action is destructive, so it requires an explicit confirm
// checkbox; the restore itself is atomic (one transaction) — a bad file is
// rejected with a specific reason and leaves the instance unchanged. PRG on
// success.
func restoreData(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		// Bound the whole request body (defense-in-depth against a huge upload),
		// and tell the owner specifically when their file is over the cap rather
		// than letting a truncated read look like a corrupt backup.
		req.Body = http.MaxBytesReader(w, req.Body, restoreMaxBytes)
		if err := req.ParseMultipartForm(restoreMaxBytes); err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				renderSettings(deps, w, req, "Esse arquivo de backup é grande demais para restaurar.", true, http.StatusBadRequest)
				return
			}
			renderSettings(deps, w, req, "Não foi possível ler o envio. Escolha um arquivo de backup válido.", true, http.StatusBadRequest)
			return
		}
		if req.PostFormValue("confirm") != "on" {
			renderSettings(deps, w, req, "Marque a caixa para confirmar — restaurar substitui todos os seus dados atuais pelo conteúdo do backup.", true, http.StatusBadRequest)
			return
		}
		f, _, err := req.FormFile("file")
		if err != nil {
			renderSettings(deps, w, req, "Escolha um arquivo de backup para restaurar.", true, http.StatusBadRequest)
			return
		}
		defer f.Close()
		raw, err := io.ReadAll(io.LimitReader(f, restoreMaxBytes))
		if err != nil {
			renderSettings(deps, w, req, "Não foi possível ler o arquivo de backup. Tente novamente.", true, http.StatusBadRequest)
			return
		}
		if _, err := deps.Backup.Restore(req.Context(), raw); err != nil {
			renderSettings(deps, w, req, restoreErrorMessage(err), true, http.StatusBadRequest)
			return
		}
		http.Redirect(w, req, "/settings?restored=1", http.StatusSeeOther)
	}
}

// restoreErrorMessage turns a restore service error into owner-facing copy. Every
// failure path is atomic, so each message can truthfully say nothing changed.
func restoreErrorMessage(err error) string {
	switch {
	case errors.Is(err, backup.ErrUnsupportedSchema):
		return "Esse arquivo não é um backup do Financas — nada foi alterado."
	case errors.Is(err, backup.ErrUnsupportedVersion):
		return "Esse backup foi feito por uma versão incompatível do Financas — nada foi alterado."
	case errors.Is(err, backup.ErrMalformed):
		return "Esse arquivo de backup é inválido ou está incompleto — nada foi alterado."
	default:
		return "Não foi possível restaurar a partir desse arquivo — nada foi alterado."
	}
}

func settingsForm(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		notice := ""
		if req.URL.Query().Get("restored") != "" {
			notice = "Seus dados foram restaurados do backup."
		}
		renderSettings(deps, w, req, notice, false, http.StatusOK)
	}
}

// renderSettings renders the Settings page with an optional notice (a success
// confirmation or an error reason from a restore attempt).
func renderSettings(deps Deps, w http.ResponseWriter, req *http.Request, notice string, isError bool, code int) {
	// The currency list is secondary here: a failure degrades to an empty select
	// (logged) rather than 500ing the whole Settings page — which also hosts the
	// backup/restore recovery tools that must stay reachable.
	var codes []string
	if currs, err := deps.Settings.ListCurrencies(req.Context()); err != nil {
		logLoad(req, "settings currencies", err)
	} else {
		for _, c := range currs {
			codes = append(codes, string(c))
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_ = web.SettingsPage(shellData(deps, req.Context(), "settings"), codes, notice, isError).Render(req.Context(), w)
}

func settingsSubmit(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, "requisição inválida", http.StatusBadRequest)
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
			http.Error(w, "requisição inválida", http.StatusBadRequest)
			return
		}
		username := req.PostFormValue("username")
		password := req.PostFormValue("password")

		if err := deps.Auth.Authenticate(req.Context(), username, password); err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_ = web.Login("Credenciais inválidas").Render(req.Context(), w)
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
