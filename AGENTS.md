# Carolina Codes — Go API

Operating manual for the read-only Go HTTP API in this repository. The starter at `carolina-codes-api-starter` describes a forkable layout (`src/`, Compose Postgres 18, a local `openapi.yaml`, `db/`, and `tests/test_catalog.py`). This tree is the finished Go sibling. Treat **this repo** as the workspace root. Do not fold it into the CMS git remote (`github.com/brightball/carolina-codes`). Do not assume `../elixir` or another sibling checkout exists.

Do not change handlers to match starter wording this tree no longer uses. `GET /health` returns `{"ok": true}`. It does not return `{ "status": "ok" }`.

## Agent memory

Go and standard-library `net/http` have no framework-specific ADR layout. This repo does not keep a `docs/adr/` tree.

- Read `DECISIONS.md` before an architectural change. When a durable choice changes, append a dated entry (date, status, choice, alternatives, why). Do not rewrite an accepted entry; supersede it with a new one. Git history is the changelog.
- Read `MEMORY.md` for commands, ports, and the contract location. Update `MEMORY.md` when those operational facts change. `MEMORY.md` is not a second decision log.

`go.mod`, `mise.toml`, and the `Dockerfile` stay the source of truth for versions. The README quotes them.

## Purpose

The Phoenix app keeps at most one language API warm and reads speakers and sponsors from it. This process must:

1. Query PostgreSQL **v1 views** only. Never Ash tables or base tables.
2. Expose the v1 REST routes below as ordinary JSON. Do not implement Ash JSON:API (`application/vnd.api+json`).
3. Register once at boot with the Elixir site (no heartbeat). If `CAROLINA_URL` is empty or the POST fails, log and keep serving.

The contract is the CMS `priv/api/openapi.yaml` plus `priv/api/AGENTS.md`. This repo does not vendor OpenAPI or `db/`.

## Language and runtime

| Piece | Shipped choice |
| --- | --- |
| Language | Go 1.26 (`go 1.26.9` in `go.mod`, mise pin `1.26.9`, image `golang:1.26.9`) |
| Framework | Standard-library `net/http` (`http.NewServeMux`). Not gin, echo, or chi. `net/http` has no module version. |
| SQL | `github.com/jackc/pgx/v5` |
| Image | `golang:1.26.9` build to a static non-root `scratch` binary (`CGO_ENABLED=0`, `USER 65534:65534`) |
| Listen | `[::]` so Fly 6PN can connect. Local default port is 4002. The container and Fly port is 8080. |

The process listens without waiting on a Postgres ping or registration. The pool uses `MinConns` 0 and a capped `MaxConns`. Health does not touch the database.

## Environment

| Variable | Example | Role |
| --- | --- | --- |
| `DATABASE_URL` | `postgres://postgres:postgres@127.0.0.1:5432/carolina_dev` | SQL views |
| `CAROLINA_URL` | `http://127.0.0.1:4000` | Elixir site (optional; register no-ops if empty or down) |
| `POLYGLOT_REGISTER_TOKEN` | `dev` | Bearer token for register |
| `PUBLIC_BASE_URL` | `http://127.0.0.1:4002` | URL Elixir will call |
| `PORT` | `4002` locally, `8080` in the container and on Fly | Listen port |

Live Postgres is Postgres 16 against database `carolina_dev`. Handler and unit tests that use a fake catalog do not need Postgres. See `MEMORY.md` for run and test commands.

## SQL views (query these only)

`v1_speakers`, `v1_sponsors`, `v1_years`, `v1_talks`, `v1_sponsorships`, `v1_year_speakers`, `v1_year_sponsors`.

Do not `SELECT` from Ash tables or from base tables such as `speakers`, `organizations`, or `talks`. The views are the API. The CMS owns the view definitions. There is no `db/` seed SQL in this repo.

Year-scoped speaker rows include `languages` and `topics`. Year-scoped sponsor rows include `tier` and `blurb`.

## Required HTTP routes

Read-only HTTP. List bodies are wrapped as `{ "data": [ ... ] }`. Detail bodies are `{ "data": { ... } }`. Unknown slugs return 404.

- `GET /health` — liveness, `{"ok": true}`, no database
- `GET /` — identity (`language` Go, `framework` `net/http`, versions, endpoints)
- `GET /v1/years`
- `GET /v1/speakers` and `GET /v1/speakers?year=`
- `GET /v1/speakers/{slug}` and `GET /v1/speakers/{year}/{slug}`
- `GET /v1/sponsors` and `GET /v1/sponsors?year=`
- `GET /v1/sponsors/{slug}` and `GET /v1/sponsors/{year}/{slug}`

`photo_path` and `logo_path` are web paths. Return the path. This repo does not serve image bytes.

## Register on boot (once)

`POST {CAROLINA_URL}/internal/api-endpoints/register`

```
Authorization: Bearer {POLYGLOT_REGISTER_TOKEN}
Content-Type: application/json
```

The JSON body includes `language`, `language_version`, `api_version`, `framework`, `created_year`, `schema_version` (1), `base_url` (`PUBLIC_BASE_URL`), and `endpoints` (objects with `method`, `path`, and `query`). There is no heartbeat. Elixir keep-alives the warm API.

Registration runs in the background after the pool is configured. If `CAROLINA_URL` is empty or the POST fails (connection refused, 4xx/5xx), log and keep serving.

## Fly

Stay scale-to-zero. `auto_stop_machines = "suspend"`, `min_machines_running = 0`, 256mb, `GOMAXPROCS=1`, `GOMEMLIMIT=200MiB`. The HTTP check is `GET /health` on port 8080. Do not deploy from a docs-only change unless asked.

## Quality gates

`go test -race`, gosec, govulncheck, gitleaks, and golangci-lint, via `make`, `mise`, and pre-commit. `make check` runs all five. Install hooks with `make hooks`.

## Layout

| Path | Role |
| --- | --- |
| `AGENTS.md` | This operating manual |
| `DECISIONS.md` | Append-only decision ledger |
| `MEMORY.md` | Operational facts (not decisions) |
| `README.md` | Install, versions, run, and test commands |
| `main.go` | `net/http` server, pgx pool, routes, register |
| `go.mod` | Module path, `go 1.26.9`, `github.com/jackc/pgx/v5` |
| `Dockerfile` | `golang:1.26.9` to static non-root `scratch` |
| `fly.toml` | Scale-to-zero Fly config |
| `Makefile`, `mise.toml` | Quality-gate pins and tasks |

## Checklist

- Routes above return JSON. Lists use `{ "data": [ ... ] }`. Unknown slugs are 404.
- Queries use only the v1 views named above. No writes. No Ash table names.
- Register runs once at process start and no-ops when the Elixir site is down.
- `GET /health` is cheap and does not touch the database.
- Listen address stays `[::]`. Pool `MinConns` stays 0.
- A durable choice change appends to `DECISIONS.md`. An operational fact change updates `MEMORY.md`.
