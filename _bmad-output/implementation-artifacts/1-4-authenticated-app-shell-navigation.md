---
baseline_commit: NO_VCS
---

# Story 1.4: Authenticated app shell & navigation

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the owner,
I want a clean app shell with a greeting and top navigation,
so that I can move between the app's areas in the look I want.

## Acceptance Criteria

From `epics.md` → Epic 1 → Story 1.4. **Given** I am authenticated, **When** any page renders, **Then**:

1. A responsive shell shows a greeting header ("Welcome back, {owner}") and top nav: Dashboard · Investments · Transactions · Accounts · Analytics (UX-DR1).
2. Base design tokens (rounded cards, soft shadows, type scale, semantic palette) are defined in Tailwind and applied to the shell (UX-DR7).
3. The shell is usable on desktop and mobile-width viewports.

## Tasks / Subtasks

- [x] **Task 1 — Tailwind design tokens (UX-DR7) (AC: #2)**
  - [x] In `web/static/css/input.css`, add a Tailwind v4 `@theme { … }` block (CSS-first; still no `tailwind.config.js`) defining the semantic system: `--color-gain` (green), `--color-loss` (red), `--color-accent` (one bold accent), plus neutral surface/text tokens; `--radius-card: 1rem` (~16px rounded cards); a soft card shadow (use a custom utility or `--shadow-*` token); and a type-scale anchor (large bold hero numerals). Keep it light-theme.
  - [x] These generate utilities (e.g. `text-gain`, `bg-loss`, `rounded-card`, `shadow-card`). Document the palette intent in a short comment (green = positive, red = negative, accent = call-out). Do NOT build full dashboard components — that is Epic 5 (Story 5.1 extends this token system).
  - [x] Rebuild `web/static/css/app.css` via `make css` and commit it.

- [x] **Task 2 — Responsive shell layout (UX-DR1) (AC: #1, #3)**
  - [x] Add `web/shell.templ` with a `Shell(data ShellData)` component that renders the full page chrome and accepts page content via templ `{ children... }`: an `<html>`/`<head>` (title, viewport, `/static/css/app.css`), a `<header>` greeting "Welcome back, {OwnerName}", a primary `<nav>` with the five links (Dashboard `/`, Investments `/investments`, Transactions `/transactions`, Accounts `/accounts`, Analytics `/analytics`), the current section visually marked active, and a logout control (a `POST /logout` form/button, reusing Story 1.3).
  - [x] Define the shell's Go types in `web/shell.go`: `type ShellData struct { OwnerName, Active string }` and an exported nav-items slice (`{Label, Href, Key}`) so links + active state are data-driven and consistent.
  - [x] **Responsive (no JS required):** horizontal nav on `md+`; on small screens a `<details>`/`<summary>` disclosure ("menu") that expands the links. Apply the design tokens (rounded-card surfaces, soft shadow, type scale). Use semantic landmarks (`<header>`, `<nav>`, `<main>`) and visible focus styles (NFR-4: keyboard-usable, legible contrast).

- [x] **Task 3 — Apply the shell to the authenticated area + nav targets (AC: #1)**
  - [x] Add `web/pages.templ` with a `DashboardPage(ShellData)` that wraps a minimal "Dashboard" placeholder body in `Shell` (the real KPI dashboard is Epic 5), and a `ComingSoon(ShellData, title string)` page for the not-yet-built sections.
  - [x] In `internal/http` (protected group), render `DashboardPage` at `/`, and add authenticated stub handlers for `/investments`, `/transactions`, `/accounts`, `/analytics` that render `ComingSoon` with the correct `Active` key — so the nav is fully navigable now and the active state is real (don't leave links 404ing). Mark these clearly as temporary placeholders for their epics.
  - [x] Replace the Story 1.1 `Placeholder()` usage at `/` with `DashboardPage`. You may delete `Placeholder()`/`layout.templ` if now unused, or keep it — note the choice. Login page stays OUTSIDE the shell (pre-auth).

- [x] **Task 4 — Wire the owner name through (AC: #1)**
  - [x] Add `OwnerName string` to `http.Deps`; set it from `cfg.OwnerUsername` in `cmd/server/main.go`. Handlers pass `ShellData{OwnerName: deps.OwnerName, Active: "<section>"}` to the page components. (No new identity source — reuse the configured owner from Story 1.3.)

- [x] **Task 5 — Tests, rebuild, verify, docs**
  - [x] templ render tests (call the components directly, no DB/session needed): `Shell`/`DashboardPage` output contains "Welcome back, {owner}", all five nav labels with correct hrefs, the active marker on the active section, and the logout control.
  - [x] http tests: authenticated `GET /` → 200 containing the greeting + nav; each nav target (`/investments`, …) → 200 when authenticated and **302 → /login when not** (still behind `requireAuth`). Update `router_test.go` for the new `Deps.OwnerName` and add an authenticated-shell assertion (drive a session via the existing login flow helper).
  - [x] `make css` (rebuild app.css), then `go build ./...`, `go vet ./...`, `go test ./...`, `make nofloat` all clean.
  - [x] Live smoke (compose db + run): log in, confirm the shell renders with the greeting + nav, nav links navigate between sections with the active state, the layout reflows at a mobile width, and logout works.
  - [x] Update `README.md` briefly (the shell + design-token system; note Story 5.1 extends the tokens).

## Dev Notes

### Scope boundary — what this story does NOT do (read first)

- **NO real dashboard content** — KPI cards, charts, allocation, insight call-out are Epic 5. `/` shows a shell-wrapped placeholder body.
- **NO real feature pages** — Investments/Transactions/Accounts/Analytics are stub "Coming soon" pages inside the shell (their epics build the real ones). They exist only so the nav is navigable and the active state is demonstrable.
- **NO new auth/identity** — reuse the configured owner (Story 1.3) for the greeting; no profile/settings.
- **Keep the token set lean** — define the semantic palette + card radius/shadow + a type anchor. Story 5.1 expands components (cards, badges, large-number primitives). Don't pre-build those here.

### Previous-story intelligence (Stories 1.1–1.3)

[Source: 1-1…, 1-2-config-database-foundation.md, 1-3-single-owner-authentication.md]

- **Tailwind v4 is CSS-first:** tokens live in `@theme` inside `web/static/css/input.css` (no `tailwind.config.js`). `make css` runs `npm run build:css` (auto-installs the pinned toolchain) and writes the committed `web/static/css/app.css`. Tailwind v4 auto-detects content (it already scans `.templ` files — `layout.templ`/`login.templ` classes are in the built CSS). After adding classes, **rebuild and commit app.css** or the Docker image ships stale CSS.
- **templ:** components are committed as `*_templ.go`; run `go tool templ generate` (`make templ`) after editing any `.templ`. Render via `web.Component(...).Render(ctx, w)` from handlers.
- **Router shape:** `NewRouter(Deps)` with a protected chi group guarded by `requireAuth` and a public group; `/` currently renders `web.Placeholder()`. The session manager (`scs`), `/healthz`, `/readyz`, `/static/*`, `/login`, `/logout`, and CSRF middleware must be **preserved**. Add new authenticated pages inside the existing protected group so they inherit `requireAuth`.
- **Owner name source:** `cfg.OwnerUsername` (config, Story 1.3). Thread it via `Deps.OwnerName`.
- **Logout:** `POST /logout` exists (Story 1.3); the shell's logout control posts to it. CSRF (`http.CrossOriginProtection`) allows same-origin form POSTs — no token needed.
- Build with `GOTOOLCHAIN=local`; `make nofloat` must stay green (UI work touches no money types, but keep it in the verify set). Repo has no commits (`baseline_commit: NO_VCS`).
- The existing `router_test.go` constructs `Deps` via a `testDeps` helper and drives login with `loginPost`/`sessionCookie` helpers — extend those rather than reinventing.

### Architecture invariants this story must honor

- **AD-10 / AD-1 — the UI renders; it performs no financial math.** This is pure presentation; no money/derivation logic enters `http`/`web`. (No money is even displayed yet.) [Source: ARCHITECTURE-SPINE.md#AD-10, #AD-1]
- **AD-7 — every non-login route authenticated.** All shell pages live in the protected group behind `requireAuth`. [Source: ARCHITECTURE-SPINE.md#AD-7]
- **Conventions:** templ components PascalCase; Tailwind tokens consistent across screens. [Source: ARCHITECTURE-SPINE.md#Consistency Conventions]

### UX requirements driving this story

[Source: epics.md#UX Design Requirements]

- **UX-DR1 — Dashboard-first shell:** greeting header "Welcome back, {owner}" + top nav (Dashboard · Investments · Transactions · Accounts · Analytics); light theme; responsive (desktop + mobile web).
- **UX-DR7 — Visual system / design tokens:** rounded-corner cards (~16px) with soft shadows, generous whitespace, a defined type scale (large bold numerals as hero), and a semantic palette: green = gain/positive, red = loss/negative, neutral + one bold accent. Tailwind tokens, consistent across all screens.
- **NFR-4 — Accessibility defaults:** keyboard-usable, legible contrast; semantic landmarks; visible focus.

### Latest-tech gotchas

- **Tailwind v4 `@theme`:** define design tokens as CSS custom properties inside `@theme { }`; they auto-generate matching utilities (`--color-gain` → `bg-gain`/`text-gain`; `--radius-card` → `rounded-card`). This is the v4 replacement for `theme.extend` in a JS config. Keep `@import "tailwindcss";` as the first line. [Knowledge cutoff: Jan 2026]
- **No-JS responsive menu:** a `<details>`/`<summary>` disclosure gives an accessible mobile menu with zero JavaScript; hide/show the two layouts with Tailwind `md:` breakpoints. (HTMX is available but not needed for static nav.) [Knowledge cutoff: Jan 2026]
- **templ children:** a layout component renders injected content with `{ children... }`; call it as `@web.Shell(data) { <page body> }` from page components. [Knowledge cutoff: Jan 2026]

### Project Structure Notes

New: `web/shell.templ`, `web/shell.go` (types + nav items), `web/pages.templ` (+ generated `*_templ.go`). Updated: `internal/http/router.go` (Deps.OwnerName, shell pages, nav stub routes), `internal/http/router_test.go`, `cmd/server/main.go`, `web/static/css/input.css` + rebuilt `app.css`, `README.md`. Possibly remove `web/layout.templ` (`Placeholder`) if superseded. No structural variance.

### Testing standards

- templ component render tests (string-contains on rendered HTML) — fast, no DB/session.
- http tests over the real router with an authenticated session (existing `loginPost`/`sessionCookie` helpers) for `/` and the nav stubs; assert unauth → 302 `/login`.
- `go test ./...` green with no database; `go vet` + `make nofloat` clean.

### References

- [Source: epics.md#Story 1.4] — acceptance criteria
- [Source: epics.md#UX Design Requirements] — UX-DR1 (shell), UX-DR7 (tokens), NFR-4
- [Source: ARCHITECTURE-SPINE.md#AD-1 / #AD-10] — UI renders only, no financial math
- [Source: ARCHITECTURE-SPINE.md#AD-7] — authenticated everywhere
- [Source: 1-3-single-owner-authentication.md] — router Deps, protected group, logout, owner config, templ/css workflow

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Opus 4.8, 1M context)

### Debug Log References

- `go tool templ generate` → `shell_templ.go`, `pages_templ.go` (and `login_templ.go`); `make css` rebuilt `app.css` (24 KB, new token/shell utilities).
- `go build ./...`, `go vet ./...`, `make nofloat` → clean.
- Tests pass: `web` (shell render: greeting + 5 nav labels + hrefs + active `aria-current` + logout) and `http` (shell rendered after login, nav targets 200 when authed / 302 → `/login` when not).
- Live smoke (server :8092 + db :5433, logged in as owner): greeting present, owner name shown, all five nav items present, `/accounts` → 200 with "Coming soon", `app.css` contains `rounded-card` + `gain` token.

### Completion Notes List

All three acceptance criteria verified (unit + live):
- **AC1 — greeting + top nav:** `web/shell.templ` renders "Welcome back, {OwnerName}" and the data-driven nav (Dashboard·Investments·Transactions·Accounts·Analytics) from `web.NavItems`, with the active section marked (`aria-current="page"` + accent styling) and a `POST /logout` control. `/` renders `DashboardPage`; the four other sections are authenticated `ComingSoon` stubs so the nav is fully navigable.
- **AC2 — design tokens:** Tailwind v4 `@theme` in `input.css` defines the semantic palette (`gain`/`loss`/`accent`, neutral `surface`/`ink`/`muted`), `--radius-card` (~16px) and `--shadow-card`; utilities (`rounded-card`, `shadow-card`, `text-gain`, …) are applied to the shell and built into `app.css`.
- **AC3 — responsive:** inline nav on `md+`, a no-JS `<details>`/`<summary>` menu below `md`; semantic `<header>`/`<nav>`/`<main>` landmarks and visible hover/focus affordances (NFR-4).

Decisions / variances (intentional):
- **`http` defines `renderPage` + injects `Deps.OwnerName`** (from `cfg.OwnerUsername`); the HTTP layer renders only — no financial math (AD-1/AD-10). It imports `github.com/a-h/templ` (presentation type) which is appropriate for the view layer.
- **Removed `web/layout.templ` + `Placeholder()`** (superseded by `DashboardPage`); the login page stays outside the shell (pre-auth).
- **Nav targets are real authenticated routes** rendering `ComingSoon`, not dead `#`/404 links — the AC ("move between the app's areas") and active-state are genuinely exercised. Their real pages come with Epics 2–5.
- Story 1.1–1.3 invariants preserved: `/healthz` dependency-free, `/readyz`, `/static/*`, `/login`, `/logout`, `scs` sessions, and CSRF middleware unchanged; `nofloat` still clean.
- Token set kept lean (palette + card radius/shadow); Story 5.1 adds card/badge/large-number primitives.

Reviewer notes: no `sprint-status.yaml` → status tracked in this file only. Changes staged but **not committed** (left for the owner).

### File List

New:
- `web/shell.go` (ShellData + NavItems), `web/shell.templ`, `web/shell_templ.go` (generated)
- `web/pages.templ`, `web/pages_templ.go` (generated)
- `web/shell_test.go`

Modified:
- `internal/http/router.go` (`Deps.OwnerName`, `renderPage`, shell pages + nav stub routes), `internal/http/router_test.go` (shell/nav assertions)
- `cmd/server/main.go` (`OwnerName` in Deps)
- `web/static/css/input.css` (`@theme` tokens) + rebuilt `web/static/css/app.css`
- `README.md` (app shell & design tokens)

Removed:
- `web/layout.templ`, `web/layout_templ.go` (superseded `Placeholder`)

## Change Log

| Date | Change |
| --- | --- |
| 2026-06-28 | Story 1.4 implemented: responsive authenticated app shell (greeting + data-driven top nav + logout, no-JS mobile menu), Tailwind v4 `@theme` design-token system (semantic palette, rounded-card, shadow-card), Dashboard + navigable ComingSoon section pages. All 3 ACs verified (unit + live). Status → review. |
