// Command server is the Financas HTTP entrypoint. It reads configuration from
// the environment, runs pending database migrations, opens the connection pool,
// builds the session manager and owner authenticator, wires the chi router, and
// serves until terminated.
//
// Per AD-1 main stays thin: config -> migrate -> pool -> wire -> listen. No
// business logic, SQL, or financial math lives here.
package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/alexedwards/scs/v2"

	"github.com/claudioaprado/financas/db"
	"github.com/claudioaprado/financas/internal/config"
	apphttp "github.com/claudioaprado/financas/internal/http"
	"github.com/claudioaprado/financas/internal/service/account"
	"github.com/claudioaprado/financas/internal/service/analytics"
	"github.com/claudioaprado/financas/internal/service/auth"
	"github.com/claudioaprado/financas/internal/service/backup"
	"github.com/claudioaprado/financas/internal/service/budget"
	"github.com/claudioaprado/financas/internal/service/category"
	"github.com/claudioaprado/financas/internal/service/categoryrule"
	"github.com/claudioaprado/financas/internal/service/exchangerate"
	"github.com/claudioaprado/financas/internal/service/importer"
	"github.com/claudioaprado/financas/internal/service/price"
	"github.com/claudioaprado/financas/internal/service/recurring"
	"github.com/claudioaprado/financas/internal/service/security"
	"github.com/claudioaprado/financas/internal/service/settings"
	"github.com/claudioaprado/financas/internal/service/transaction"
	"github.com/claudioaprado/financas/internal/service/valuation"
	"github.com/claudioaprado/financas/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx := context.Background()

	// Apply pending migrations before serving any request.
	if err := store.Migrate(ctx, cfg.DatabaseURL, db.Migrations); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	log.Print("migrations applied")

	pool, err := store.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer pool.Close()
	log.Print("database connected")

	// Session manager: inactivity (IdleTimeout) + absolute (Lifetime) expiry,
	// hardened cookie. The default in-memory store is used (see README for the
	// postgres-store upgrade when multi-replica/restart durability is needed).
	sessions := scs.New()
	sessions.IdleTimeout = 30 * time.Minute
	sessions.Lifetime = 12 * time.Hour
	sessions.Cookie.HttpOnly = true
	sessions.Cookie.SameSite = http.SameSiteLaxMode
	sessions.Cookie.Secure = cfg.SecureCookies
	sessions.Cookie.Path = "/"

	authn := auth.New(auth.Owner{
		Username:     cfg.OwnerUsername,
		PasswordHash: cfg.OwnerPasswordHash,
	})

	srv := &http.Server{
		Addr: ":" + cfg.Port,
		Handler: apphttp.NewRouter(apphttp.Deps{
			Sessions:      sessions,
			Auth:          authn,
			Ready:         pool.Ping,
			Settings:      settings.New(pool),
			ExchangeRates: exchangerate.New(pool),
			Prices:        price.New(pool),
			Accounts:      account.New(pool),
			Transactions:  transaction.New(pool),
			Categories:    category.New(pool),
			CategoryRules: categoryrule.New(pool),
			Budgets:       budget.New(pool),
			Analytics:     analytics.New(pool),
			Recurring:     recurring.New(pool),
			Securities:    security.New(pool),
			Imports:       importer.New(pool),
			Valuation:     valuation.New(pool),
			Backup:        backup.New(pool),
			OwnerName:     cfg.OwnerUsername,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("financas listening on :%s", cfg.Port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
