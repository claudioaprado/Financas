# syntax=docker/dockerfile:1

# --- build stage -------------------------------------------------------------
# Compiles the server from committed source. Code generation (templ, Tailwind)
# is a dev-time step whose outputs (*_templ.go, web/static/css/app.css) are
# committed to the repo, so the image build is a deterministic `go build` with
# no Node and no codegen toolchain. Run `make generate css` before building.
FROM golang:1.26-alpine AS build
WORKDIR /src

# Cache module downloads first.
COPY go.mod go.sum ./
RUN go mod download

# Build a static, stripped binary; assets are embedded via go:embed.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# --- runtime stage -----------------------------------------------------------
# Single, minimal, non-root image: just the static binary (assets embedded).
# Config and secrets come from the environment only (AD-8).
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/server /app/server
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/server"]
