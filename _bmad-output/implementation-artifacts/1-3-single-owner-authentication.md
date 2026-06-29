---
baseline_commit: NO_VCS
---

# Story 1.3: Single-owner authentication

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the owner,
I want to sign in with my credentials and have unauthenticated access blocked,
so that my financial data is private to me.

## Acceptance Criteria

From `epics.md` → Epic 1 → Story 1.3. **Given** a configured owner credential (hashed with argon2id), **When** I submit correct credentials on the login page, **Then**:

1. A session cookie is set and I reach the authenticated area (FR-14, AD-7).
2. Any request to a non-login route without a valid session is rejected/redirected to login.
3. State-changing requests are protected by Go 1.25+ `http.CrossOriginProtection` (NFR-1).
4. The session ends on logout and after an inactivity timeout.

## Tasks / Subtasks

- [x] **Task 1 — Owner-credential & cookie config (AC: #1, #4)**
  - [x] Extend `internal/config.Config` with `OwnerUsername` (`OWNER_USERNAME`, required), `OwnerPasswordHash` (`OWNER_PASSWORD_HASH`, required — a PHC-format argon2id string, NOT a plaintext password), and `SecureCookies` (`SECURE_COOKIES`, default `false` for local http; `true` in Azure/HTTPS). Parse `SECURE_COOKIES` as a bool ("true"/"1").
  - [x] Update `Load()` validation + the missing-vars error to include the new required vars; never log the hash.
  - [x] Unit-test the new fields (present/missing/default) with `t.Setenv`.

- [x] **Task 2 — `service/auth`: argon2id credential verification (AC: #1)**
  - [x] Add `github.com/alexedwards/argon2id`. Create `internal/service/auth/auth.go`: `type Owner struct { Username, PasswordHash string }`, `New(Owner) *Authenticator`, and `Authenticate(ctx, username, password string) error` returning a sentinel `ErrInvalidCredentials` on any mismatch.
  - [x] Verify with `argon2id.ComparePasswordAndHash(password, owner.PasswordHash)`. **Run the argon2id compare even when the username is wrong** (compare against the stored hash regardless) and compare the username with `crypto/subtle.ConstantTimeCompare` — avoid a timing oracle distinguishing bad-user from bad-password. Return the same `ErrInvalidCredentials` for both.
  - [x] This layer does NOT touch HTTP or cookies — it only decides credential validity (AD-1; capability map: `service/auth`).
  - [x] Tests: correct creds pass; wrong password fails; wrong username fails; malformed stored hash returns an error (not a panic). Generate the test hash at runtime with `argon2id.CreateHash` so no secret is committed.

- [x] **Task 3 — Password-hash helper (operator convenience)**
  - [x] Add `cmd/hashpw/main.go`: reads a password (arg or stdin) and prints its argon2id PHC hash via `argon2id.CreateHash(pw, argon2id.DefaultParams)`, so the owner can generate `OWNER_PASSWORD_HASH`. Add a `make hashpw` target and document it. Keep it tiny; no logging of the password.

- [x] **Task 4 — Sessions, login/logout, and route protection (AC: #1, #2, #4)**
  - [x] Add `github.com/alexedwards/scs/v2`. In `internal/http`, create the session manager wiring: `scs.New()` with `IdleTimeout` (inactivity, e.g. 30m), `Lifetime` (absolute, e.g. 12h), and `Cookie` settings `HttpOnly=true`, `SameSite=http.SameSiteLaxMode`, `Secure=cfg.SecureCookies`, `Path="/"`. Use the **in-memory store** (`scs/v2/memstore`, the default) for now — see Dev Notes for the documented postgres-store upgrade and why it's deferred.
  - [x] Convert `NewRouter` to take a `Deps` struct (`Sessions *scs.SessionManager`, `Auth Authenticator` interface, `Ready ReadyCheck`) instead of positional args. Define `type Authenticator interface { Authenticate(ctx context.Context, username, password string) error }` in `internal/http` (consumer-side interface; `service/auth.Authenticator` satisfies it). main injects the concrete impl (AD-1).
  - [x] Wrap the whole router in `sessions.LoadAndSave`. Split routes: **public** = `GET/POST /login`, `POST /logout`, `/healthz`, `/readyz`, `/static/*`; **protected** (chi group with auth middleware) = `/` and everything later. Auth middleware checks `sessions.GetBool(ctx, "authenticated")`; if false, redirect `GET` to `/login` (302) and reject others.
  - [x] Handlers: `GET /login` renders the login form (templ); `POST /login` reads username/password, calls `Auth.Authenticate`, and on success `sessions.RenewToken(ctx)` (prevent fixation) + `sessions.Put(ctx, "authenticated", true)` then redirect to `/`; on failure re-render with a generic "invalid credentials" message (HTTP 401, no detail leak). `POST /logout` calls `sessions.Destroy(ctx)` and redirects to `/login`.
  - [x] Add `web/login.templ` (minimal: username + password fields, posts to `/login`). Real styling/shell is Story 1.4 — keep it plain but functional; link the existing `/static/css/app.css`.
  - [x] Update `cmd/server/main.go` to build the session manager, the authenticator from config, and pass `Deps`. Update existing `internal/http/router_test.go` to the new `Deps` signature (construct a test session manager + a stub authenticator); keep `/healthz`/`/readyz` assertions.

- [x] **Task 5 — CSRF via `http.CrossOriginProtection` (AC: #3)**
  - [x] Add Go 1.25+ `http.NewCrossOriginProtection()` as middleware wrapping the router (`r.Use` adapting `cop.Handler`). It guards unsafe methods (POST/PUT/…) using Fetch-metadata/Origin; safe GETs pass, and the same-origin login/logout POSTs pass. No per-form token needed.
  - [x] If local dev needs it, document `AddTrustedOrigin`, but do NOT add bypasses by default.
  - [x] Test: a cross-origin-looking POST (e.g. `Sec-Fetch-Site: cross-site`) to a state-changing route is rejected (403); a same-origin POST passes the CSRF layer.

- [x] **Task 6 — Verify end-to-end & docs (all ACs)**
  - [x] `go build ./...`, `go vet ./...`, `go test ./...`, `make nofloat` all clean.
  - [x] httptest integration flow (in-memory store, no DB needed): unauthenticated `GET /` → 302 `/login`; `POST /login` bad creds → 401; good creds → 302 `/` + Set-Cookie; authenticated `GET /` → 200; `POST /logout` → session cleared, subsequent `GET /` → 302 `/login`. Drive cookies through an `http.Client` jar or by copying the Set-Cookie header across requests.
  - [x] Live smoke (`docker compose up -d db`; export config incl. `OWNER_USERNAME`, `OWNER_PASSWORD_HASH` from `make hashpw`, `SESSION_SECRET`): `make run`, confirm `/login` renders, login redirects, `/` requires auth, logout works.
  - [x] Update `README.md` (auth setup: generating the hash, env vars, idle/absolute timeouts, CSRF), `.env.example` (`OWNER_USERNAME`, `OWNER_PASSWORD_HASH`, `SECURE_COOKIES`), and `docker-compose.yml` `app` env (dev owner creds + `SECURE_COOKIES=false`).

## Dev Notes

### Scope boundary — what this story does NOT do (read first)

- **NO owner/credential table in the database.** The single owner's username + argon2id hash are **configured via env** (AD-8, AD-7: single-tenancy is an invariant, not a column). No migration is added by this story.
- **NO domain features** (accounts, transactions, etc.) — Epic 2+. The only protected route today is the placeholder `/`.
- **NO real app shell / nav / styling** — Story 1.4. The login page is plain-but-functional; the post-login landing stays the Story 1.1 placeholder (now behind auth).
- **Sessions use the in-memory store**, not the database — see the deferral note below. This satisfies every AC; persistence/replica-safety is a documented later upgrade.

### Session store: in-memory now, postgres later (deliberate)

`scs` defaults to an in-memory store. It satisfies all four ACs (cookie, enforcement, idle + absolute timeout, logout) and keeps this story free of a sessions migration and an extra `database/sql` handle. **Caveat to record in the README:** in-memory sessions are lost on restart/redeploy (owner re-logs-in) and are not shared across replicas. For Azure Container Apps (AD-8), run a single replica, OR upgrade to `github.com/alexedwards/scs/postgresstore` with a `sessions` migration when multi-replica/restart-durability is needed. Keep the store assignment a single line (`sessions.Store = …`) so the swap is trivial. Do not build the postgres store now.

### Previous-story intelligence (Stories 1.1 + 1.2)

[Source: 1-1-…md, 1-2-config-database-foundation.md]

- **Build with `GOTOOLCHAIN=local`** (go.mod pins `1.26.3`). Go 1.26 includes `http.CrossOriginProtection` (added 1.25) — available.
- **`internal/config.Load()`** already validates env and fails fast; extend it, don't replace it. Keep the "name the missing vars, never log secrets" behavior.
- **`internal/http.NewRouter`** currently takes a `ReadyCheck` func and exposes `/healthz` (dependency-free) + `/readyz`; it renders `web.Placeholder()` at `/`. Changing the signature to a `Deps` struct is expected — **preserve** `/healthz` (no session/auth dependency), `/readyz`, `/static/*` serving, and the placeholder index (now behind auth). Update `router_test.go` accordingly.
- **`cmd/server/main.go`** wires config → migrate → pool → router; insert session-manager + authenticator construction into that chain.
- **Compose DB is on host `5433`** (container 5432); `SECURE_COOKIES=false` for local http. `make hashpw` is the way to produce `OWNER_PASSWORD_HASH` for `.env`/compose.
- **`money`/decimal and `nofloat`** unaffected; keep `make nofloat` in the verify set. No floats anywhere.
- Repo still has **no commits** (`baseline_commit: NO_VCS`).

### Architecture invariants this story must honor

- **AD-7 — Single owner, authenticated everywhere.** Exactly one owner; every non-login route requires an authenticated session; credentials hashed with **argon2id**; no tenant/owner column. [Source: ARCHITECTURE-SPINE.md#AD-7]
- **AD-1 — Layered direction.** `service/auth` decides credential validity (no HTTP); `http` owns cookies/sessions/redirects and depends on an `Authenticator` interface it defines; `main` injects the concrete service. [Source: ARCHITECTURE-SPINE.md#AD-1, #Capability Map "Auth (FR-14)"]
- **Conventions:** Go 1.25+ `http.CrossOriginProtection` for CSRF; auth middleware guards all non-login routes. [Source: ARCHITECTURE-SPINE.md#Consistency Conventions]

### Latest-tech gotchas

- **`http.CrossOriginProtection` (Go 1.25+):** `cop := http.NewCrossOriginProtection()`; use `cop.Handler(next)` as middleware. It rejects unsafe-method cross-origin requests via Sec-Fetch-Site/Origin; GET/HEAD pass. Same-origin form POSTs pass with no token. Optionally `cop.AddTrustedOrigin("https://…")`. [Knowledge cutoff: Jan 2026]
- **scs v2:** wrap router in `sessionManager.LoadAndSave`. On login: `RenewToken(ctx)` (session-fixation defense) then `Put(ctx,"authenticated",true)`. Check with `GetBool(ctx,"authenticated")`. Logout: `Destroy(ctx)`. Set `IdleTimeout`, `Lifetime`, and `Cookie.{HttpOnly,SameSite,Secure,Path}`. The in-memory store is the zero-config default. [Knowledge cutoff: Jan 2026]
- **argon2id (`github.com/alexedwards/argon2id`):** `CreateHash(pw, argon2id.DefaultParams)` → PHC string; `ComparePasswordAndHash(pw, hash) (bool, error)`. Constant-time internally; still guard username compare with `subtle.ConstantTimeCompare` and always run the hash compare to avoid a user-enumeration timing side channel. [Knowledge cutoff: Jan 2026]

### Security checklist (the reviewer will look for these)

- Cookie: `HttpOnly`, `SameSite=Lax`, `Secure` in prod, no sensitive data in the cookie (scs stores only a token).
- `RenewToken` on privilege change (login) to prevent fixation.
- Generic auth-failure message + 401 (no "user not found" vs "bad password" distinction); constant-time + always-hash to kill timing enumeration.
- CSRF middleware actually wraps state-changing routes (verified by a test).
- No plaintext password or hash logged anywhere; password never placed in a URL/query.

### Project Structure Notes

New: `internal/service/auth/auth.go` (+ `auth_test.go`), `cmd/hashpw/main.go`, `web/login.templ` (+ generated `web/login_templ.go`).
Updated: `internal/config/config.go` (+test), `internal/http/router.go` (+`Deps`, login/logout/auth-middleware/session/CSRF) (+`router_test.go`), `cmd/server/main.go`, `Makefile` (`hashpw`), `README.md`, `.env.example`, `docker-compose.yml`. No structural variance from the established tree.

### Testing standards

[Source: 1-1/1-2 established `testing` + `httptest`; scs memstore makes auth flows DB-free]

- `service/auth`: pure unit tests (correct/wrong/malformed); generate hashes at runtime.
- `config`: `t.Setenv` tests for new vars.
- `http`: httptest flow over the real router with an in-memory scs manager + stub authenticator — unauth redirect, login success/failure, logout, and a CSRF-rejection test. `go test ./...` must stay green with no database.
- Keep `go vet` and `make nofloat` clean.

### References

- [Source: ARCHITECTURE-SPINE.md#AD-7] — single-owner auth, argon2id, no tenant column
- [Source: ARCHITECTURE-SPINE.md#AD-1 / Capability Map] — `service/auth` + `http` middleware split
- [Source: ARCHITECTURE-SPINE.md#Consistency Conventions] — `http.CrossOriginProtection`, auth middleware on all non-login routes
- [Source: epics.md#Story 1.3] — acceptance criteria
- [Source: 1-2-config-database-foundation.md] — config/router/main wiring to extend; compose port 5433; nofloat

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Opus 4.8, 1M context)

### Debug Log References

- `go build ./...`, `go vet ./...`, `make nofloat` → clean.
- Unit/integration suite (no DB): `config`, `service/auth`, and `http` (healthz, readyz, unauth redirect, bad login 401, login→home→logout flow, CSRF cross-origin 403) all pass.
- Live HTTP smoke (server on :8091 + compose db on :5433): (1) unauth `/`→303 `/login`; (2) `/login`→200 form; (3) good login→303 `/` +cookie; (4) authed `/`→200; (5) bad login→401; (6) logout→303 `/login`; (7) `/` after logout→303 `/login`. Startup logged migrations applied → database connected → listening.

### Completion Notes List

All four acceptance criteria verified (unit + live):
- **AC1 — session cookie + authenticated area:** `POST /login` with valid creds renews the session token (anti-fixation), marks `authenticated`, sets the scs cookie, and redirects to `/`; `/` then renders. Verified live (steps 3–4).
- **AC2 — unauthenticated access blocked:** `requireAuth` middleware redirects unauthenticated GETs to `/login` (303) and 401s other methods (steps 1, 7; `TestRequireAuthRedirect`).
- **AC3 — CSRF:** `http.NewCrossOriginProtection().Handler` wraps all routes; a `Sec-Fetch-Site: cross-site` POST is rejected 403 (`TestCSRFRejectsCrossOrigin`), same-origin/non-browser POSTs pass.
- **AC4 — session ends on logout + inactivity:** `POST /logout` destroys the session (steps 6–7; `TestLoginLogoutFlow`); inactivity via scs `IdleTimeout=30m` + absolute `Lifetime=12h`.

Decisions / variances (intentional):
- **Owner credential is env-configured** (`OWNER_USERNAME`, `OWNER_PASSWORD_HASH` argon2id PHC) — no owner table/column (AD-7). `cmd/hashpw` (`make hashpw`) generates the hash.
- **`service/auth.Authenticate` is timing-safe:** always runs the argon2id compare and uses `subtle.ConstantTimeCompare` on the username, returning one `ErrInvalidCredentials` for both wrong-user and wrong-password (no enumeration oracle).
- **`http` defines the `Authenticator` interface** (consumer side) and is injected the `service/auth` impl by `main`; `internal/http` imports no persistence types — `/readyz` uses an injected `ReadyCheck` (pool.Ping). Router signature is now `NewRouter(Deps)`.
- **Sessions: scs in-memory store** (satisfies all ACs; DB-free tests). Documented postgres-store upgrade for multi-replica/restart durability deferred (README caveat). No sessions migration added.
- **No CSRF token in the login form** — `http.CrossOriginProtection` uses Fetch-metadata/Origin, so same-origin POSTs pass without a token.
- **Compose dev owner** = `owner` / `financas`; the argon2id hash is stored with `$`→`$$` escaping so compose doesn't interpolate it.
- Story 1.1's invariants preserved: `/healthz` stays dependency-free; `/static/*` + placeholder `/` (now behind auth) still served; `nofloat` still clean.

Reviewer notes: no `sprint-status.yaml` → status tracked in this file only. Changes staged but **not committed** (left for the owner).

### File List

New:
- `internal/service/auth/auth.go`, `internal/service/auth/auth_test.go`
- `cmd/hashpw/main.go`
- `web/login.templ`, `web/login_templ.go` (generated)

Modified:
- `internal/config/config.go`, `internal/config/config_test.go` (owner creds + `SECURE_COOKIES`)
- `internal/http/router.go`, `internal/http/router_test.go` (`Deps`, sessions, login/logout, auth middleware, CSRF)
- `cmd/server/main.go` (session manager + authenticator wiring)
- `Makefile` (`hashpw` target), `README.md` (auth docs), `.env.example` (owner/cookie vars)
- `docker-compose.yml` (`app` dev owner creds + `SECURE_COOKIES`)
- `web/static/css/app.css` (rebuilt — login styles), `go.mod`, `go.sum` (scs/v2, argon2id)

## Change Log

| Date | Change |
| --- | --- |
| 2026-06-28 | Story 1.3 implemented: single-owner argon2id auth (`service/auth` + `cmd/hashpw`), scs sessions with idle+absolute timeouts and login/logout, `requireAuth` route protection, and Go 1.25 `http.CrossOriginProtection` CSRF. All 4 ACs verified (unit + live HTTP flow). Status → review. |
