#!/usr/bin/env bash
#
# Financas — first-time Azure provisioning runbook (owner-run; requires `az login`).
# Creates the resource group + container registry, builds the image in ACR from
# the repo Dockerfile (no local Docker needed), then deploys the Bicep
# (PostgreSQL Flexible Server + Container Apps env + the app). Idempotent: safe
# to re-run; ongoing image deploys can use .github/workflows/deploy.yml instead.
#
# Required environment variables (the script refuses to run without the secrets):
#   PG_ADMIN_PASSWORD    strong password for the PostgreSQL admin
#   SESSION_SECRET       e.g.  openssl rand -base64 32
#   OWNER_PASSWORD_HASH  argon2id hash from:  make hashpw
# Optional (with defaults):
#   RG=financas-rg  LOCATION=eastus  NAME_PREFIX=financas  OWNER_USERNAME=owner
#   ACR_NAME=<derived>  IMAGE_TAG=<git short sha|latest>
set -euo pipefail

RG="${RG:-financas-rg}"
LOCATION="${LOCATION:-eastus}"
NAME_PREFIX="${NAME_PREFIX:-financas}"
OWNER_USERNAME="${OWNER_USERNAME:-owner}"
IMAGE_TAG="${IMAGE_TAG:-$(git rev-parse --short HEAD 2>/dev/null || echo latest)}"

: "${PG_ADMIN_PASSWORD:?set PG_ADMIN_PASSWORD}"
: "${SESSION_SECRET:?set SESSION_SECRET (e.g. openssl rand -base64 32)}"
: "${OWNER_PASSWORD_HASH:?set OWNER_PASSWORD_HASH (run: make hashpw)}"

# ACR names are globally unique and alphanumeric only.
ACR_NAME="${ACR_NAME:-$(echo "${NAME_PREFIX}acr$(date +%s)" | tr -cd 'a-z0-9' | cut -c1-50)}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel 2>/dev/null || cd "$SCRIPT_DIR/../.." && pwd)"

echo "==> [1/5] Resource group: $RG ($LOCATION)"
az group create -n "$RG" -l "$LOCATION" -o none

echo "==> [2/5] Container registry: $ACR_NAME"
az acr create -g "$RG" -n "$ACR_NAME" --sku Basic --admin-enabled true -o none
LOGIN_SERVER="$(az acr show -g "$RG" -n "$ACR_NAME" --query loginServer -o tsv)"

echo "==> [3/5] Build image in ACR from the repo Dockerfile (tag: $IMAGE_TAG)"
az acr build -r "$ACR_NAME" -t "financas:${IMAGE_TAG}" "$REPO_ROOT" -o none

echo "==> [4/5] Deploy infrastructure (PostgreSQL + Container App)"
az deployment group create -g "$RG" \
  --name main \
  --template-file "$SCRIPT_DIR/main.bicep" \
  --parameters \
    namePrefix="$NAME_PREFIX" \
    acrName="$ACR_NAME" \
    image="${LOGIN_SERVER}/financas:${IMAGE_TAG}" \
    pgAdminPassword="$PG_ADMIN_PASSWORD" \
    sessionSecret="$SESSION_SECRET" \
    ownerUsername="$OWNER_USERNAME" \
    ownerPasswordHash="$OWNER_PASSWORD_HASH" \
  -o none

echo "==> [5/5] Done"
URL="$(az deployment group show -g "$RG" -n main --query properties.outputs.appUrl.value -o tsv)"
echo "    App URL: $URL"
echo "    Verify:  curl -i $URL/healthz   # expect 200"
echo "    Sign in: open $URL/login   (user: $OWNER_USERNAME)"
echo "    Logs:    az containerapp logs show -g $RG -n ${NAME_PREFIX}-app --follow"
