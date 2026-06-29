package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/internal/money"
	"github.com/claudioaprado/financas/internal/service/account"
	"github.com/claudioaprado/financas/internal/service/exchangerate"
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

func (s *stubAccounts) List(_ context.Context, includeArchived bool) ([]account.Account, error) {
	out := []account.Account{}
	for _, a := range s.accts {
		if includeArchived || !a.Archived {
			out = append(out, a)
		}
	}
	return out, nil
}

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
