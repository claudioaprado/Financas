# Financas — developer task runner. See README.md for prerequisites.
# Codegen tool versions are pinned here so generation is reproducible.

SQLC_VERSION  := 1.27.0
GOOSE_VERSION := v3.21.1

.PHONY: help generate templ sqlc css build run test vet tidy up down migrate nofloat hashpw

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

generate: templ sqlc ## Run all code generation (templ + sqlc)

templ: ## Generate Go from *.templ (committed as *_templ.go)
	go tool templ generate

sqlc: ## Generate type-safe queries from db/query into internal/store (via the pinned sqlc Docker image — its source build fails on macOS SDKs)
	docker run --rm -v "$(CURDIR):/src" -w /src sqlc/sqlc:$(SQLC_VERSION) generate

css: node_modules ## Build the Tailwind stylesheet web/static/css/app.css (committed)
	npm run build:css

node_modules: package.json ## Install the dev-time Tailwind toolchain
	npm install

build: ## Compile the server binary into bin/server
	go build -o bin/server ./cmd/server

run: ## Run the server locally (PORT defaults to 8080)
	go run ./cmd/server

test: ## Run the test suite
	go test ./...

vet: ## Run go vet across all packages
	go vet ./...

nofloat: ## NFR-5: fail if float32/float64 appears in the financial core
	@if grep -rnE 'float(32|64)' internal/money internal/domain internal/service internal/store --include='*.go' | grep -vE '_test\.go|_templ\.go'; then \
		echo "NFR-5 violation: floating-point found in the financial core (see above)"; exit 1; \
	else echo "nofloat: OK — no float32/float64 in internal/{money,domain,service,store}"; fi

tidy: ## Tidy module dependencies
	go mod tidy

up: ## Start app + PostgreSQL 18 via docker compose (builds the image)
	docker compose up --build

down: ## Stop and remove docker compose services
	docker compose down

migrate: ## Apply goose migrations (needs DATABASE_URL) — used from Story 1.2
	go run github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION) -dir db/migrations postgres "$(DATABASE_URL)" up

hashpw: ## Print an argon2id hash for OWNER_PASSWORD_HASH (reads the password from stdin)
	@go run ./cmd/hashpw
