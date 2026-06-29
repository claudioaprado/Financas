# Deploy Financas to Azure

Single container image → **Azure Container Apps** + **Azure Database for
PostgreSQL Flexible Server** (AD-8). Config and secrets are supplied via
environment only; nothing sensitive is baked into the image. The app runs its
goose migrations on startup, so each new revision migrates before serving.

> These steps require **your Azure subscription**. The repo ships the
> infrastructure (`main.bicep`), a one-shot provisioning script (`deploy.sh`),
> and an ongoing-deploy GitHub Action (`../../.github/workflows/deploy.yml`).

## Prerequisites

- [Azure CLI](https://learn.microsoft.com/cli/azure/) (`az version`) and `az login`
- The Bicep CLI (`az bicep install`)
- This repo checked out (the image is built from its `Dockerfile`)

## First-time provisioning

Generate the secrets, then run the script:

```bash
# Owner password hash (argon2id) — from the repo root:
make hashpw                         # prompts; copy the printed hash

export PG_ADMIN_PASSWORD='<a strong password>'
export SESSION_SECRET="$(openssl rand -base64 32)"
export OWNER_PASSWORD_HASH='<paste the make hashpw output>'
export OWNER_USERNAME='owner'       # optional (default: owner)
# Optional: RG, LOCATION, NAME_PREFIX, ACR_NAME, IMAGE_TAG

az login
./deploy/azure/deploy.sh
```

The script: creates the resource group + container registry, builds the image
in ACR from the `Dockerfile` (no local Docker needed), then deploys the Bicep
(PostgreSQL + Container Apps environment + the app). It prints the HTTPS app URL.

## Verify (the live acceptance criteria)

```bash
URL="https://<app>.<region>.azurecontainerapps.io"   # printed by deploy.sh
curl -i "$URL/healthz"        # AC#1: 200 over HTTPS, app running
az containerapp logs show -g financas-rg -n financas-app --follow | grep "migrations applied"   # AC#3
open "$URL/login"             # AC#1: sign in (DB connectivity) as the owner
```

AC#2 (no secrets in the image) holds by construction: secrets are Container App
secrets injected as env vars (`DATABASE_URL`, `SESSION_SECRET`,
`OWNER_PASSWORD_HASH`); the image carries none.

## Ongoing deploys (GitHub Actions)

After first-time provisioning, configure the repo for the **Deploy to Azure**
workflow and run it (Actions → Deploy to Azure → Run):

- **Secrets:** `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_SUBSCRIPTION_ID`
  (an Entra app with a **federated credential** for this repo — OIDC, no stored
  client secret). See `az ad app federated-credential create`.
- **Variables:** `AZURE_RESOURCE_GROUP` (e.g. `financas-rg`), `ACR_NAME`,
  `CONTAINER_APP_NAME` (e.g. `financas-app`).

The workflow builds a SHA-tagged image in ACR and rolls the Container App to it.

## Notes & options

- **PostgreSQL version:** `postgresVersion` defaults to `16` (widely GA on
  Flexible Server). The local dev stack uses Postgres 18; bump this to 18 once
  Flexible Server offers it (`az postgres flexible-server list-skus -l <loc>`).
  Safe today — the app has no schema yet.
- **Single replica / single revision:** matches the in-memory session store. For
  multiple replicas, switch `scs` to the Postgres store (a sessions migration)
  first, then raise `maxReplicas`.
- **Hardening (optional):** private networking / VNet integration for Postgres,
  a Key Vault for secrets (Container App `keyVaultUrl` secret refs), managed
  identity for ACR pull instead of admin credentials, and a custom domain.
- **Cost:** Burstable `Standard_B1ms` Postgres + a scale-to-one Container App is
  the minimal footprint; delete with `az group delete -n financas-rg`.
