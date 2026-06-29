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
	"time"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/claudioaprado/financas/internal/money"
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

// Deps are the collaborators the router needs, injected by main.
type Deps struct {
	Sessions  *scs.SessionManager
	Auth      Authenticator
	Ready     ReadyCheck
	Settings  Settings
	OwnerName string // shown in the shell greeting (from config)
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
		pr.Get("/accounts", renderPage(deps, "accounts", func(d web.ShellData) templ.Component { return web.ComingSoon(d, "Accounts") }))
		pr.Get("/analytics", renderPage(deps, "analytics", func(d web.ShellData) templ.Component { return web.ComingSoon(d, "Analytics") }))
		pr.Get("/settings", settingsForm(deps))
		pr.Post("/settings", settingsSubmit(deps))
	})

	return r
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
