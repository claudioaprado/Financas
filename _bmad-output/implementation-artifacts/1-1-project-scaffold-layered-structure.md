---
baseline_commit: NO_VCS
---

# Story 1.1: Project scaffold & layered structure

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the builder,
I want the Go project scaffolded in the layered "onion" structure with local tooling,
so that every later story has a consistent place to live and a one-command local environment.

## Acceptance Criteria

From `epics.md` → Epic 1 → Story 1.1. **Given** an empty repository, **When** the scaffold is created, **Then**:

1. The Go module exists with packages `cmd/server`, `internal/{domain,money,service,store,http}`, `web/`, `db/migrations`, `db/query` (AD-1).
2. A chi server starts and serves a `/healthz` route returning HTTP 200.
3. `docker-compose up` starts the app plus a PostgreSQL 18 container, and `Dockerfile` builds a single image.
4. goose, sqlc, templ, and Tailwind are wired with a documented build command.

## Tasks / Subtasks

- [x] **Task 1 — Initialize the Go module and onion package skeleton (AC: #1)**
  - [x] `go mod init` with module path `github.com/claudioaprado/financas` (single-owner; adjust only if a different remote is intended).
  - [x] Pin Go directive `go 1.26.4` in `go.mod`. _(Variance: set to `1.26.3` — the installed toolchain — to avoid a toolchain auto-download; see Completion Notes.)_
  - [x] Create the directory tree exactly per the spine's Structural Seed (see Dev Notes): `cmd/server/`, `internal/{domain,money,service,store,http}/`, `web/`, `db/migrations/`, `db/query/`.
  - [x] Add a minimal `doc.go` (package clause + one-line purpose comment) to each empty `internal/*` package so the tree compiles and the dependency direction is documented at the package level. Do NOT add real logic — later stories fill these.
  - [x] Add a `.gitignore` covering Go build artifacts, `tmp/`, `*_templ.go` is NOT ignored (generated code is committed — see Dev Notes), the Tailwind output CSS decision (see Dev Notes), `.env`, and local Docker volumes.

- [x] **Task 2 — chi HTTP server with `/healthz` (AC: #2)**
  - [x] Add chi v5 dependency (`github.com/go-chi/chi/v5`).
  - [x] In `internal/http/`, create a `Router()`/`NewRouter()` constructor that builds a `chi.Mux`, mounts a `GET /healthz` handler returning `200 OK` with a tiny body (e.g. `ok`). No DB call — `/healthz` must succeed before Story 1.2 wires the pool.
  - [x] In `cmd/server/main.go`, read the listen address from `PORT` (default `8080`), build the router from `internal/http`, and call `http.ListenAndServe` (or `http.Server` with timeouts). Keep main thin: config-read → wire → listen. No business logic, no SQL, no financial math (AD-1).
  - [x] Verify locally: `go run ./cmd/server` then `curl -i localhost:8080/healthz` returns `200`. _(Verified via `httptest` unit test `TestHealthz`; live container verification under Task 4.)_

- [x] **Task 3 — Wire the build tooling: templ, sqlc, goose, Tailwind (AC: #4)**
  - [x] **templ**: add as a Go tool dependency via `go get -tool github.com/a-h/templ/cmd/templ@latest` (Go 1.24+ `go tool` directive — records it in `go.mod`). Add a placeholder `web/layout.templ` (minimal `templ Hello()` or base shell stub) and run `templ generate` to confirm `_templ.go` output is produced and compiles. Generated `*_templ.go` files ARE committed. _(templ v0.3.1020; `web/layout_templ.go` generated and compiled.)_
  - [x] **sqlc**: add `sqlc.yaml` at repo root configured for engine `postgresql`, queries `db/query`, schema `db/migrations`, output package `store` into `internal/store` (sqlc v2 config). Because there are no queries/migrations yet, codegen may legitimately produce nothing — the AC is that it is *wired and documented*, not that it emits code this story. Add sqlc as a `go tool` dependency too if it installs cleanly; otherwise document the pinned binary version in the README. _(Wired via pinned `go run sqlc@v1.27.0` in the Makefile to keep the module graph lean; not executed — no queries yet.)_
  - [x] **goose**: create `db/migrations/` and document the goose invocation. Story 1.1 only needs the directory + tooling wired; do NOT author schema migrations here (that is Story 1.2+). Optionally add a no-op/example migration that is clearly marked, or leave a `db/migrations/.gitkeep`. Prefer the goose *library* (`github.com/pressly/goose/v3`) for embedded run-on-startup later; CLI invocation is fine for local dev now. _(`db/migrations/.gitkeep` + pinned `make migrate` target.)_
  - [x] **Tailwind 4.x**: set up the CSS-first pipeline (see Dev Notes — Tailwind 4 has NO `tailwind.config.js` by default). Add `web/static/css/input.css` with `@import "tailwindcss";`, and document the standalone Tailwind CLI build command producing `web/static/css/app.css`. Decide and document whether `app.css` is committed or built (see Dev Notes recommendation). _(Tailwind v4.3.1 via a dev-only `package.json`; `app.css` built and committed.)_
  - [x] Add a `Makefile` (or `Taskfile`) with documented targets: `generate` (templ generate + sqlc generate), `css` (Tailwind build), `build`, `run`, `up`/`down` (docker compose), `migrate`. This is the "documented build command" the AC requires.

- [x] **Task 4 — Single-image Dockerfile + docker-compose with PostgreSQL 18 (AC: #3)**
  - [x] Multi-stage `Dockerfile`: build stage runs `templ generate`, Tailwind build, then `go build` of `./cmd/server`; final stage is a minimal image (distroless/static or alpine) containing ONLY the binary + embedded assets. Embed `web/` static + generated templ via Go `embed` so the final image is a single self-contained binary (AD-8: "single image: server + embedded templ/static assets"). _(Codegen outputs are committed, so the Dockerfile only compiles — no Node/codegen in the image; final stage is distroless static nonroot.)_
  - [x] `docker-compose.yml`: two services — `app` (built from the Dockerfile) and `db` (`postgres:18`). Wire `app`→`db` via `DATABASE_URL` env and `depends_on`. Use a named volume for Postgres data. Expose app on `8080`. The app need not successfully connect to the DB this story (connection is Story 1.2) — but `docker-compose up` MUST bring up both containers and `/healthz` MUST answer 200.
  - [x] Add `.env.example` documenting `PORT`, `DATABASE_URL`, `SESSION_SECRET` (the last two consumed in later stories) — config/secrets via environment only (AD-8). Do NOT commit a real `.env`.
  - [x] Verify: `docker compose up --build` → both containers start → `curl localhost:8080/healthz` returns 200. _(Verified: image built, `postgres:18` healthy, `/healthz` → 200 body `ok`; clean `down -v`.)_

- [x] **Task 5 — README with the documented build/run commands (AC: #4)**
  - [x] Write `README.md` covering: prerequisites (Go 1.26.4, Docker, Tailwind CLI), the generate/build/run/test commands (point at the Makefile targets), how to run with docker-compose, and the package-layering rule (AD-1) so contributors keep the dependency direction.

## Dev Notes

### Scope boundary — what this story does NOT do (read first)

This story is **scaffold + tooling only**. Keep a hard line against Story 1.2 and beyond:

- **NO** pgx connection pool, **NO** running migrations on startup, **NO** `Money` type / decimal package implementation — all of that is **Story 1.2** (`Config & database foundation with decimal money`). `/healthz` must return 200 *without* a database. [Source: epics.md#Story 1.2]
- **NO** auth, sessions, CSRF middleware — **Story 1.3**. [Source: epics.md#Story 1.3]
- **NO** real app shell / greeting / nav / design tokens — **Story 1.4 & 5.1**. A bare templ stub is fine here. [Source: epics.md#Story 1.4]
- **NO** Azure deployment config — **Story 1.5**. The Dockerfile must be Azure-Container-Apps-friendly (single image, env-only config) but no Azure resources here. [Source: epics.md#Story 1.5, ARCHITECTURE-SPINE.md#AD-8]

If a behavior is required for the scaffold to actually run end-to-end (compiles, server boots, containers come up, healthz answers), it is in scope even if not spelled out above. Don't bleed into the next stories' substance.

### Required directory structure (Structural Seed — copy exactly)

[Source: ARCHITECTURE-SPINE.md#Structural Seed]

```text
financas/
  cmd/server/            # main: config, wiring, http.ListenAndServe
  internal/
    domain/              # entities + ALL derivations; no project imports
    money/               # decimal Money type, Currency, Convert() (Story 1.2)
    service/             # use-cases; owns DB transactions (the only mutator)
    store/               # pgx + sqlc-generated queries (db/query/*.sql -> here)
    http/                # chi handlers, middleware, templ render
  web/                   # *.templ views, static assets, Tailwind input
  db/
    migrations/          # goose *.sql
    query/               # sqlc source queries
  Dockerfile
  docker-compose.yml     # app + postgres for local dev
```

### Architecture invariants this story must honor

- **AD-1 — Layered dependency direction.** Dependencies point inward only: `http → service → store`; all → `domain`/`money`; `domain`/`money` import nothing project-internal. A handler never calls the store directly; the store never imports `service`. The skeleton's `doc.go` files and any import wiring must respect this from day one. This is the structural contract every later story relies on. [Source: ARCHITECTURE-SPINE.md#AD-1]
- **AD-8 — One container image, env-only config.** Single Docker image = server + embedded templ/static assets. Config and secrets come from environment only. Local dev mirrors prod via Docker Compose. Target is Azure Container Apps + Azure DB for PostgreSQL Flexible Server (deployment itself is Story 1.5). Build the Dockerfile and compose now to that shape. [Source: ARCHITECTURE-SPINE.md#AD-8]
- **AD-4 (forward-looking).** No floating-point money will ever be allowed; the `money` package is created (as a stub package) here but implemented in 1.2. Don't introduce any `float64` for monetary/quantity anywhere. [Source: ARCHITECTURE-SPINE.md#AD-4]

### Pinned stack (verified current June 2026 — do not downgrade)

[Source: ARCHITECTURE-SPINE.md#Stack, epics.md#Stack]

| Tool | Version | Go import / install |
| --- | --- | --- |
| Go | 1.26.4 | `go 1.26.4` in go.mod |
| PostgreSQL | 18.4 | `postgres:18` image in compose |
| chi (router) | v5 | `github.com/go-chi/chi/v5` |
| pgx (driver) | v5 | `github.com/jackc/pgx/v5` (used in 1.2, not here) |
| sqlc | current | tool / pinned binary |
| goose | current | `github.com/pressly/goose/v3` |
| templ | current | `github.com/a-h/templ` (+ `cmd/templ`) |
| HTMX | 2.x | vendored JS in `web/static` (used from 1.4) |
| Tailwind CSS | 4.x | standalone CLI or `@tailwindcss/cli` |
| shopspring/decimal | current | `github.com/shopspring/decimal` (used in 1.2) |

### Latest-tech gotchas (prevent outdated implementations)

- **Tailwind CSS 4.x is CSS-first.** There is no generated `tailwind.config.js` by default. Configuration/theme lives in CSS via `@import "tailwindcss";` and an `@theme { … }` block. Use the standalone Tailwind CLI binary (no Node project required) or `@tailwindcss/cli`. Content detection is automatic in v4 — you generally do not hand-maintain a `content` array. Design tokens (rounded cards, semantic green/red palette) land in Story 5.1; here just establish the pipeline and an `input.css`. [Knowledge cutoff: Jan 2026]
- **`go tool` directive (Go 1.24+).** Prefer `go get -tool …` to record templ/sqlc/goose as tool dependencies in `go.mod` (reproducible, no global installs), invoked via `go tool templ generate`, etc. Go 1.26 supports this fully. Fall back to pinned binaries only if a tool doesn't install cleanly as a module tool. [Knowledge cutoff: Jan 2026]
- **templ generates committed `*_templ.go`.** Run `templ generate` in the Dockerfile build stage AND keep generated files in the repo so `go build` works without the tool. Do not gitignore `*_templ.go`.
- **`go:embed` for the single image.** Embed `web/static` (and the built `app.css`) into the binary so the final Docker stage carries no loose asset files (AD-8 "embedded static assets"). If you choose to build `app.css` during the Docker build, ensure the embed path still resolves at compile time.
- **`http.CrossOriginProtection`** (Go 1.25+ stdlib CSRF) is referenced by AD-7/Story 1.3 — do NOT add it here, just don't preclude it.

### Tailwind output-file decision (make it and document it)

Two valid options — pick one and write it in the README:
- **(Recommended) Commit the built `web/static/css/app.css`** and run the Tailwind build in the Dockerfile. Simplest reproducibility; reviewers see the CSS. Add `app.css` to `.gitignore` ONLY if you instead always build it in CI/Docker.
- Build-only (gitignore `app.css`): cleaner repo, but every `go run` needs a prior `make css`. For a single-owner project the committed-artifact path is less friction.

### Testing standards

[Source: ARCHITECTURE-SPINE.md (Errors/Testing conventions); no formal test framework mandated]

- Use Go's standard `testing` package + `net/http/httptest`. No third-party test framework is mandated by the spine.
- **Minimum for this story:** a table-free `httptest` test in `internal/http` asserting `GET /healthz` → `200`. This is the one behavioral AC that is unit-testable now.
- `go vet ./...` and `go build ./...` must pass clean. If `golangci-lint` is easy to add, wire a `make lint` target, but it is not required by the AC.
- Keep tests next to code (`router_test.go` beside `router.go`).

### Project Structure Notes

- The directory tree above is dictated verbatim by the architecture spine's Structural Seed — there is **no variance** permitted; later stories assume these exact paths (e.g. sqlc reads `db/query`, writes `internal/store`; goose reads `db/migrations`). [Source: ARCHITECTURE-SPINE.md#Structural Seed]
- This is a greenfield repo (`git` not yet initialized per environment scan). The dev agent should `git init` as part of scaffolding so generated/committed-artifact decisions are real.
- Module path: default to `github.com/claudioaprado/financas`. If the owner has not chosen a remote, any stable reverse-DNS-style path is fine, but keep it consistent — changing it later rewrites every import.

### References

- [Source: _bmad-output/planning-artifacts/architecture/architecture-Financas-2026-06-28/ARCHITECTURE-SPINE.md#AD-1] — layered dependency direction
- [Source: _bmad-output/planning-artifacts/architecture/architecture-Financas-2026-06-28/ARCHITECTURE-SPINE.md#AD-8] — single container image, env-only config, Azure target
- [Source: _bmad-output/planning-artifacts/architecture/architecture-Financas-2026-06-28/ARCHITECTURE-SPINE.md#Structural Seed] — exact directory tree
- [Source: _bmad-output/planning-artifacts/architecture/architecture-Financas-2026-06-28/ARCHITECTURE-SPINE.md#Stack] — pinned versions
- [Source: _bmad-output/planning-artifacts/epics.md#Story 1.1] — acceptance criteria
- [Source: _bmad-output/planning-artifacts/epics.md#Additional Requirements] — greenfield scaffold task list
- [Source: _bmad-output/planning-artifacts/prds/prd-Financas-2026-06-28/addendum.md] — stack rationale, no-external-integration

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Opus 4.8, 1M context)

### Debug Log References

- `go vet ./...` → clean; `go build ./...` → clean; `go test ./...` → `ok internal/http` (`TestHealthz`), rest no-test packages.
- `make css` → Tailwind v4.3.1 built `web/static/css/app.css` (22 KB).
- Docker E2E (job `ba2m4wht0`): image `financas-app` built; `docker compose up` → `postgres:18` healthy + app up; `GET /healthz` → `200` body `ok`; `db` "ready to accept connections"; `docker compose down -v` clean. `RESULT: HEALTHZ_200_OK`.

### Completion Notes List

All four acceptance criteria verified:
- **AC1 (layered structure):** module `github.com/claudioaprado/financas` with `cmd/server` + `internal/{domain,money,service,store,http}` + `web/` + `db/migrations` + `db/query`. Each inner package carries a `doc.go` documenting its AD-1 role; `go build ./...` compiles the whole tree.
- **AC2 (chi `/healthz`):** `internal/http.NewRouter()` serves `GET /healthz` → 200 with no DB dependency; covered by `TestHealthz`.
- **AC3 (Docker single image + compose):** multi-stage `Dockerfile` → distroless static nonroot single binary (assets embedded via `go:embed`); `docker-compose.yml` runs app + `postgres:18`; verified live (see Debug Log).
- **AC4 (tooling wired + documented build command):** templ (go tool), sqlc (`sqlc.yaml` + pinned `go run`), goose (pinned `make migrate`), Tailwind v4 (dev-only `package.json`) — all driven by a documented `Makefile`; README documents prerequisites, commands, and the AD-1 layering rule.

Decisions / variances (all intentional):
- **Go directive `1.26.3`** (installed toolchain) rather than the spine's `1.26.4`, to avoid a `GOTOOLCHAIN` auto-download. Patch-level only; bump to 1.26.4 once that toolchain is installed. The Dockerfile builds on `golang:1.26-alpine` (tracks the latest 1.26 patch).
- **`postgres:18` volume mount at `/var/lib/postgresql`** (not the legacy `/var/lib/postgresql/data`) — PostgreSQL 18+ rejects the old mount point on first start (stores data in a major-version subdirectory). This was caught and fixed during live verification.
- **sqlc wired via pinned `go run` (v1.27.0), not a `go tool` dependency**, to keep the module graph lean; not executed this story (no queries yet). templ IS a `go tool` dep since its runtime is a direct dependency of generated code.
- **Codegen outputs committed** (`web/layout_templ.go`, `web/static/css/app.css`) so the Docker image only compiles — no Node/codegen toolchain in the runtime path; deterministic single binary (AD-8).
- **Tailwind v4 needs a local `package.json`/`node_modules`** for `@import "tailwindcss"` to resolve (bare `npx` fails); `make css` auto-installs the pinned toolchain. `node_modules` is gitignored.
- Scope held to scaffold only: no pgx pool, no migrations-on-startup, no `Money` type, no auth — all deferred to Stories 1.2/1.3 per the story's scope boundary.

Reviewer notes: no `sprint-status.yaml` exists, so status is tracked in this file only. Changes are staged but **not committed** (left for the owner).

### File List

New files:
- `go.mod`, `go.sum`
- `cmd/server/main.go`
- `internal/http/router.go`, `internal/http/router_test.go`
- `internal/domain/doc.go`, `internal/money/doc.go`, `internal/service/doc.go`, `internal/store/doc.go`
- `web/embed.go`, `web/layout.templ`, `web/layout_templ.go` (generated)
- `web/static/css/input.css`, `web/static/css/app.css` (generated), `web/static/js/.gitkeep`
- `db/migrations/.gitkeep`, `db/query/.gitkeep`
- `sqlc.yaml`, `Makefile`, `package.json`, `package-lock.json`
- `Dockerfile`, `docker-compose.yml`, `.dockerignore`
- `.gitignore`, `.env.example`, `README.md`

## Change Log

| Date | Change |
| --- | --- |
| 2026-06-28 | Story 1.1 implemented: layered Go scaffold, chi `/healthz`, templ+sqlc+goose+Tailwind tooling, single-image Dockerfile + docker-compose (PostgreSQL 18). All 4 ACs verified (unit test + live Docker E2E). Status → review. |
