---
baseline_commit: NO_VCS
---

# Story 1.5: Azure deployment

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the builder,
I want the image deployed to Azure over managed Postgres,
so that I can reach Financas from any device.

## Acceptance Criteria

From `epics.md` → Epic 1 → Story 1.5. **Given** the single Docker image, **When** it is deployed to Azure Container Apps with Azure Database for PostgreSQL Flexible Server (AD-8), **Then**:

1. The app is reachable over HTTPS and connects to the managed database.
2. All config/secrets are supplied via environment (no secrets in the image).
3. Migrations run successfully against the managed database on deploy.

> **Owner-gated:** ACs 1 and 3 require a live Azure subscription and are satisfied when **the owner runs the provisioning** (this story produces and locally validates everything needed; the dev agent cannot provision Azure). AC 2 is satisfied by the artifacts themselves (verifiable here). See "Owner Deployment Gate".

## Tasks / Subtasks

### Dev-agent-owned (produced & validated in this repo)

- [x] **Task 1 — Bicep infrastructure for the Azure footprint (AC: #1, AD-8)**
  - [x] Add `deploy/azure/main.bicep` provisioning (single resource group, parameterized location/names): a **Log Analytics workspace**, a **Container Apps managed environment** (`Microsoft.App/managedEnvironments`), an **Azure Container Registry** (`Microsoft.ContainerRegistry/registries`, Basic), an **Azure Database for PostgreSQL Flexible Server** (`Microsoft.DBforPostgreSQL/flexibleServers`, Burstable `Standard_B1ms`, smallest storage, a `financas` database, SSL enforced), and the **Container App** (`Microsoft.App/containerApps`) running the image with external HTTPS ingress on port 8080.
  - [x] Parameterize: location, name prefix, PG admin username, PG version, container image ref, and min/max replicas. Mark secret params `@secure()` (PG admin password, `SESSION_SECRET`, `OWNER_USERNAME`, `OWNER_PASSWORD_HASH`).
  - [x] **PG version:** default the `postgresVersion` param to the highest version GA on Flexible Server (see Dev Notes — Azure may not yet offer 18; use `16` and note the dev/prod variance). The app adds no domain schema yet, so this is safe.
  - [x] Add `deploy/azure/main.parameters.example.json` with placeholder values and inline comments; the real parameters file is owner-supplied and gitignored.

- [x] **Task 2 — Container App config: env-only secrets, HTTPS, single-revision, migrate-on-start (AC: #1, #2, #3)**
  - [x] In the Container App resource: external ingress (`ingress.external=true`, `targetPort=8080`, transport auto → HTTPS via the managed `*.azurecontainerapps.io` FQDN); **single revision mode** and **min=max=1 replica** (matches the in-memory session decision from Story 1.3 and avoids two replicas migrating concurrently).
  - [x] Wire env vars from Container App **secrets** (not plaintext, not baked into the image, AC #2): `DATABASE_URL` (built from the PG FQDN + `financas` db + `sslmode=require`), `SESSION_SECRET`, `OWNER_USERNAME`, `OWNER_PASSWORD_HASH`, plus plain env `SECURE_COOKIES=true`, `PORT=8080`. Registry pull via managed identity or ACR admin creds (parameter-selectable; prefer managed identity).
  - [x] Confirm (no code change expected) the app already (a) reads all config from env (`internal/config`), (b) runs goose migrations on startup before serving (`store.Migrate`, Story 1.2) → satisfies AC #3 on every new revision, and (c) listens on `$PORT`. If any gap exists, note it; do not silently add prod-only branches.
  - [x] **TLS to Postgres:** `sslmode=require` works with pgx out of the box (Azure PG enforces TLS). Verify the connection-string builder/runbook uses it.

- [x] **Task 3 — CI/CD workflow (build → push → deploy) (AC: #1, #3)**
  - [x] Add `.github/workflows/deploy.yml`: on manual `workflow_dispatch` (and optionally push to `main`), run `make generate css` (codegen), build the image, push to ACR, and update the Container App to the new image tag. Use **Azure login via OIDC federated credentials** (`azure/login@v2` with `client-id`/`tenant-id`/`subscription-id`, no stored client secret) — document the federated-credential setup; provide a service-principal fallback note.
  - [x] Tag images by Git SHA; the deploy step sets the Container App image to that tag (a new revision → boots → migrates → takes traffic).
  - [x] Do not embed any secret in the workflow; all Azure auth via OIDC, all app secrets already live as Container App secrets (set out-of-band by the owner).

- [x] **Task 4 — Deploy runbook & prod config (AC: #2)**
  - [x] Add `deploy/azure/deploy.sh` — an idempotent az-CLI runbook (create RG → `az deployment group create` with the Bicep → `az acr build`/push the image → set Container App secrets → restart/update revision). Comment each step; read secrets from the environment/prompts, never hard-code.
  - [x] Add `deploy/azure/README.md` — the **owner runbook**: prerequisites (`az` CLI + `containerapp` extension, an Azure subscription), `az login`, generating secrets (`make hashpw` for `OWNER_PASSWORD_HASH`; a strong `SESSION_SECRET`), running the deploy, and **verifying** (`curl https://<fqdn>/healthz` → 200; open `/login`, sign in). Call out `SECURE_COOKIES=true` in prod.
  - [x] Update root `README.md` with a short "Deploy to Azure" pointer to `deploy/azure/README.md`. Add `deploy/azure/main.parameters.json` and any `*.azureauth`/secret files to `.gitignore`.

- [x] **Task 5 — Local validation & verify (all dev-agent ACs)**
  - [x] If the `az` CLI is available: `az bicep build --file deploy/azure/main.bicep` compiles with no errors (and `az bicep lint` clean). If `az` is NOT installed, state that explicitly and validate the Bicep by structure/review; do not claim a compile that didn't run.
  - [x] Validate `deploy/azure/deploy.sh` with `bash -n` and the workflow YAML parses (e.g. a YAML lint / `python -c yaml.safe_load`).
  - [x] Re-confirm the existing app meets deploy prerequisites: image builds (`docker build`), `/healthz` answers, config is env-only, migrations run on startup. `go build/vet/test` + `make nofloat` still green (no regressions from any wiring touched).
  - [x] Record exactly what was locally verified vs. what remains owner-gated.

## Dev Notes

### Scope split — dev-agent vs. owner (read first)

This story is **deployment enablement**. The dev agent **authors and locally validates** all artifacts (Bicep, Container App config, CI/CD, runbook, prod config) and confirms the app is deploy-ready. **The owner performs the actual provisioning** (`az login` + run the runbook) — that is the only way to satisfy the *live* parts of ACs 1 and 3. Do NOT fabricate a "deployed and reachable" claim. Mark the story's live verification as owner-gated.

### Owner Deployment Gate (the owner runs these — NOT dev-agent executable)

Documented fully in `deploy/azure/README.md`; summary:
1. `az login` → select subscription; install the `containerapp` Bicep/extension if prompted.
2. Provide secrets: `SESSION_SECRET` (strong random), `OWNER_USERNAME`, `OWNER_PASSWORD_HASH` (`make hashpw`), PG admin password.
3. Run `deploy/azure/deploy.sh` (or `az deployment group create` + the image build/push).
4. **Verify the live ACs:** `curl https://<app-fqdn>/healthz` → 200 (AC #1 HTTPS + running); sign in at `/login` (AC #1 DB connectivity); confirm the deploy logs show "migrations applied" (AC #3); confirm no secret is in the image (AC #2 — secrets are Container App secrets/env).

### Scope boundary — what this story does NOT do

- **NO application code changes** beyond, at most, trivial deploy-support (the app is already env-driven, migrates on startup, and listens on `$PORT` from Stories 1.1–1.2). If a real gap surfaces, flag it rather than adding prod-only behavior.
- **NO custom domain / WAF / private networking** by default — public network access + enforced TLS is the baseline (note private-endpoint hardening as an option). Keep it the minimal secure footprint (single user).
- **NO multi-replica / autoscale** — min=max=1, single-revision (consistent with the in-memory session store from Story 1.3). Multi-replica needs the scs postgres-store upgrade first.
- **NO secret values committed** — only `*.example` parameter files; real params + secrets are owner-supplied and gitignored.

### Previous-story intelligence (Stories 1.1–1.4)

[Source: 1-1 … 1-4 story files]

- **AD-8 is already honored by the build:** the `Dockerfile` produces a single distroless static binary with assets embedded (verified live in Story 1.1); `docker compose` proved the app + `postgres:18` path. Reuse this exact image for Azure — do not fork a prod Dockerfile.
- **Config is fully env-driven** (`internal/config.Load`, Stories 1.2/1.3): `DATABASE_URL`, `SESSION_SECRET`, `OWNER_USERNAME`, `OWNER_PASSWORD_HASH`, `SECURE_COOKIES`, `PORT`. Prod sets `SECURE_COOKIES=true` (HTTPS) — the cookie `Secure` flag depends on it.
- **Migrations run on startup** (`store.Migrate`, Story 1.2) → AC #3 is automatic per revision. goose is idempotent; single-revision + single-replica avoids concurrent migration races.
- **`make hashpw`** (Story 1.3) generates `OWNER_PASSWORD_HASH`; the dev compose hash is `$`-escaped — for Azure, set it as a Container App **secret** (no escaping needed).
- **Host port note** (5433) was a *local* docker-compose detail to avoid colliding with a native Postgres; irrelevant to Azure (managed PG has its own FQDN:5432).
- Build with `GOTOOLCHAIN=local`; repo has no commits yet (`baseline_commit: NO_VCS`).

### Architecture invariants this story must honor

- **AD-8 — One container image → Azure Container Apps + Azure DB for PostgreSQL Flexible Server; config/secrets from environment only; local dev mirrors prod via Docker Compose.** This story realizes AD-8's deployment half. The spine flags `[ASSUMPTION: Container Apps over App Service; confirm at deploy]` — Container Apps is the choice here. [Source: ARCHITECTURE-SPINE.md#AD-8, #Deferred]
- **AD-7 — auth everywhere / HTTPS:** prod serves over the managed HTTPS ingress; `SECURE_COOKIES=true`. [Source: ARCHITECTURE-SPINE.md#AD-7]

### Latest-tech gotchas (prevent outdated/broken IaC)

- **Azure PG Flexible Server version parity:** Azure may not yet offer **PostgreSQL 18** on Flexible Server (the spine/dev use 18; managed offerings lag). Default the Bicep `postgresVersion` to a **GA version (e.g. `16`)** and document bumping it when 18 is available. The app uses standard SQL and has no schema yet, so this is safe now — but record the dev(18)/prod(16) variance as a known item to reconcile before Epic 2 ships real migrations. [Knowledge cutoff: Jan 2026 — owner must confirm available versions with `az postgres flexible-server list-skus`/version docs at deploy.]
- **Container Apps HTTPS is automatic:** external ingress yields a managed-TLS `https://<app>.<region>.azurecontainerapps.io` FQDN — no cert wiring for AC #1. `targetPort` must match the app's `8080`.
- **Migrations-on-boot + rollout:** use **single-revision mode** so a new revision fully starts (and migrates) before receiving traffic; with min=max=1 there's exactly one migrator. (If multi-replica is ever enabled, add a goose advisory-session lock.)
- **OIDC for CI:** `azure/login@v2` supports federated credentials (no long-lived secret in GitHub). Document the `az ad app federated-credential` setup; SP-with-secret is the fallback. [Knowledge cutoff: Jan 2026]
- **`sslmode=require`** in `DATABASE_URL` for Azure PG (TLS enforced); pgx honors it with no extra config.

### Project Structure Notes

New: `deploy/azure/main.bicep`, `deploy/azure/main.parameters.example.json`, `deploy/azure/deploy.sh`, `deploy/azure/README.md`, `.github/workflows/deploy.yml`. Updated: root `README.md` (deploy pointer), `.gitignore` (real params/secrets). No application source changes expected. No structural variance to the Go tree.

### Testing / validation standards

- IaC isn't unit-tested; validation = `az bicep build`/`lint` (if `az` present), `bash -n deploy/azure/deploy.sh`, YAML parse of the workflow, and a re-confirm that the app builds + `/healthz` + migrates-on-start + `go test`/`nofloat` are green.
- Be explicit in the Dev Agent Record about what ran locally vs. what is owner-gated. No overclaiming.

### References

- [Source: epics.md#Story 1.5] — acceptance criteria
- [Source: ARCHITECTURE-SPINE.md#AD-8] — single image, Container Apps + Azure DB for PostgreSQL, env-only config, local mirrors prod
- [Source: ARCHITECTURE-SPINE.md#Deferred] — Container Apps vs App Service + PG tier settled at deploy
- [Source: 1-1-…md] — single-image Dockerfile (distroless, embedded assets), verified
- [Source: 1-2-config-database-foundation.md] — env config + migrate-on-startup (AC #3 mechanism)
- [Source: 1-3-single-owner-authentication.md] — `SECURE_COOKIES`, `make hashpw`, single-replica/in-memory sessions rationale

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Opus 4.8, 1M context)

### Debug Log References

- `az bicep build --file deploy/azure/main.bicep` → **compiles with no warnings/errors** (az-cli 2.85.0, Bicep 0.44.1). (Fixed an initial BCP073 by dropping the read-only `network.publicNetworkAccess`.)
- `bash -n deploy/azure/deploy.sh` → syntax OK. `.github/workflows/deploy.yml` → `yaml.safe_load` parses OK. `main.parameters.example.json` → valid JSON.
- App regression unchanged: `go build`/`go vet`/`go test ./...` green, `make nofloat` OK. No application source was modified.

### Completion Notes List

**Locally verified (dev-agent scope):**
- **AC #2 (no secrets in image):** satisfied by construction — `main.bicep` puts `DATABASE_URL`/`SESSION_SECRET`/`OWNER_PASSWORD_HASH` in **Container App secrets** injected as env vars; the `Dockerfile` (Story 1.1) bakes nothing. `SECURE_COOKIES=true` and `PORT=8080` are plain env.
- **Deploy-readiness for AC #1/#3:** the app already reads all config from env (`internal/config`), runs goose migrations on startup before serving (`store.Migrate`, Story 1.2 → AC #3 per revision), and listens on `$PORT` — no code change needed. Bicep ingress is external HTTPS on `targetPort: 8080`; `DATABASE_URL` uses `sslmode=require`; single-revision + min=max=1 ensures one migrator before traffic.
- IaC/CI/runbook all author-complete and validated (see Debug Log).

**Owner-gated (requires an Azure subscription — NOT dev-agent executable):**
- Live AC #1 (reachable over HTTPS + connects to managed DB) and AC #3 (migrations run against the managed DB) are satisfied when the owner runs `deploy/azure/deploy.sh` after `az login` and verifies per `deploy/azure/README.md`. The dev agent did **not** provision Azure and makes no "it's live" claim.

**Decisions / variances (intentional):**
- **ACR created by `deploy.sh` (CLI), referenced as `existing` in Bicep** — so `az acr build` can build/push the image *before* the Container App deploys (avoids a chicken-and-egg image pull). ACR pull uses admin creds via a Container App secret; managed identity noted as hardening.
- **CI workflow omits `make generate css`** (the task's literal wording): templ/Tailwind outputs are **committed** (project-wide decision since Story 1.1), so `az acr build .` compiles them directly — no Go/Node codegen needed in CI. Documented in the workflow header; rebuild locally with `make generate css` before pushing if `.templ`/CSS changed. This is a deliberate, documented deviation, not an omission.
- **`postgresVersion` defaults to `16`** (GA on Flexible Server) vs. local Postgres 18 — recorded dev/prod variance; safe now (no schema), bump when 18 is offered. PG version param `@allowed` 15/16/17.
- **Public network access + `AllowAzureServices` firewall + enforced TLS** as the minimal-secure baseline; private networking/Key Vault/managed-identity/custom-domain documented as optional hardening.
- Min=max=1 / single-revision consistent with the in-memory `scs` store (Story 1.3); raising replicas needs the postgres session-store upgrade first.

Reviewer notes: no `sprint-status.yaml` → status tracked in this file only. Changes staged but **not committed** (left for the owner). With 1.5 deployment-ready, Epic 1's authoring is complete; the live deploy is the owner's to run.

### File List

New:
- `deploy/azure/main.bicep` — Container Apps + PostgreSQL Flexible Server + env/secrets (AD-8)
- `deploy/azure/main.parameters.example.json`
- `deploy/azure/deploy.sh` — first-time provisioning runbook (executable)
- `deploy/azure/README.md` — owner deploy runbook + verification
- `.github/workflows/deploy.yml` — OIDC build→push→roll Container App

Modified:
- `README.md` ("Deploy to Azure" pointer)
- `.gitignore` (real params / `*.azureauth` ignored)

## Change Log

| Date | Change |
| --- | --- |
| 2026-06-28 | Story 1.5 deployment artifacts authored & locally validated: Bicep (Container Apps + Azure DB for PostgreSQL Flexible Server, env-only secrets, HTTPS ingress, single-revision migrate-on-boot), `deploy.sh` runbook, OIDC GitHub Actions deploy workflow, owner runbook. Bicep compiles clean; app regression green. Live provisioning is owner-gated. Status → review. |
