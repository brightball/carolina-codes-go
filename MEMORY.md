# Memory

Operational facts for this Go API. Decisions live in `DECISIONS.md`. Update this file when a command, port, database, or contract location changes. This file is not a decision log.

## Run

Local default port is 4002. The container and Fly port is 8080. Live Postgres is 16. The database name is `carolina_dev`.

```bash
go mod tidy
DATABASE_URL=postgres://postgres:postgres@127.0.0.1:5432/carolina_dev \
CAROLINA_URL=http://127.0.0.1:4000 \
POLYGLOT_REGISTER_TOKEN=dev \
PUBLIC_BASE_URL=http://127.0.0.1:4002 \
PORT=4002 \
go run .
```

Registration no-ops when `CAROLINA_URL` is empty or the CMS is down. The process still serves HTTP.

## Test

`make test` runs `go test -race ./...`. Handler and unit tests that use a fake catalog do not need Postgres. Live HTTP against the CMS views needs Postgres 16 and `carolina_dev`. Fixture tests skip when those views are missing.

`make sast`, `make vuln`, `make gitleaks`, and `make lint` are the other gates. `make check` runs all five. `mise run test|sast|vuln|gitleaks|lint|check` calls the same Makefile targets. `make hooks` installs pre-commit.

## Contract

CMS `priv/api/openapi.yaml` and `priv/api/AGENTS.md`. This repo does not vendor OpenAPI or `db/`. Query only the `v1_*` views named in `AGENTS.md`.

## Versions

Go 1.25 (`go 1.25.0` in `go.mod`, mise pin `1.25.14`, image `golang:1.25`). HTTP is standard-library `net/http` and has no separate module version. SQL is `github.com/jackc/pgx/v5` at `v5.9.2`.
