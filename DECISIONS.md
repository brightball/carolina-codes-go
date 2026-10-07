# Decisions

Append-only ledger for this Go + standard-library `net/http` + pgx service. Go has no framework-specific ADR layout, so durable choices live here instead of a `docs/adr/` tree. Each entry records the date, status, the choice, alternatives, and why. Git history is the changelog. Supersede an entry by appending a new dated one. Do not rewrite an accepted entry.

`MEMORY.md` is operational memory (commands, ports, contract location). It is not a second decision log. Do not record decisions there.

## 2026-08-27 — Standard-library net/http

- Status: accepted
- Choice: Serve HTTP with stdlib `net/http` (`http.NewServeMux` and method patterns). Do not add gin, echo, or chi.
- Alternatives: gin, echo, chi, or another third-party router.
- Why: The route set is small and fixed. The standard library matches the Go 1.22+ pattern mux and keeps the direct module graph to pgx. `net/http` has no module version to pin.

## 2026-08-27 — pgx and v1 views only

- Status: accepted
- Choice: Query PostgreSQL only through `github.com/jackc/pgx/v5`, and only the views `v1_speakers`, `v1_sponsors`, `v1_years`, `v1_talks`, `v1_sponsorships`, `v1_year_speakers`, and `v1_year_sponsors`. Never Ash tables or base tables. The HTTP contract is the CMS `priv/api/openapi.yaml` and `priv/api/AGENTS.md`. This repo does not vendor OpenAPI or `db/`.
- Alternatives: `database/sql` with lib/pq, querying Ash tables, or copying the starter `openapi.yaml` and `db/` into this remote.
- Why: The CMS owns the schema. The views are the public SQL contract. pgx speaks Postgres arrays and the pool settings this process needs. Vendoring the starter catalog would drift from the CMS.

## 2026-09-22 — Listen before dial and background register

- Status: accepted
- Choice: Open the pool with `MinConns` 0 and a capped `MaxConns`, start listening without a Postgres ping, and register once in a background goroutine. `POST {CAROLINA_URL}/internal/api-endpoints/register` with `Authorization: Bearer`. If `CAROLINA_URL` is empty or the POST fails, keep serving. There is no heartbeat. Health does not touch the database and returns `{"ok": true}`.
- Alternatives: Block startup on `pool.Ping` and a successful register call, or retry registration on a timer.
- Why: Fly scale-to-zero cold start must pass `GET /health` when Postgres or the CMS is slow. Registration has been best-effort since the first server on 2026-08-27. Skipping the ping landed 2026-09-22.

## 2026-09-22 — IPv6 listen and Fly scale-to-zero

- Status: accepted
- Choice: Listen on `[::]` for Fly 6PN. Stay scale-to-zero: `auto_stop_machines = "stop"` (not suspend), `min_machines_running = 0`, 256mb, `GOMAXPROCS=1`, `GOMEMLIMIT=200MiB`. Local default port is 4002. The container and Fly port is 8080.
- Alternatives: Bind IPv4 `0.0.0.0` only, `auto_stop_machines = "suspend"`, or keep a machine running.
- Why: 6PN dials the IPv6 address (listen change on 2026-09-01). Autostop with `stop` has been set since 2026-08-27. A 256mb shared CPU needs one OS thread and a heap limit under the machine size (caps on 2026-09-22).

## 2026-09-22 — Scratch image

- Status: accepted
- Choice: Multi-stage build from `golang:1.25` to a static non-root `scratch` binary (`CGO_ENABLED=0`, stripped, `USER 65534:65534`), with the Alpine CA bundle copied in.
- Alternatives: The earlier `golang:1.23` build copied onto `alpine:3.20`, a CGO-enabled binary, or a distroless image with a shell.
- Why: Cold start and a shell-less static binary. `scratch` has no CA store of its own, so the image copies `ca-certificates.crt` for the register client.

## 2026-09-22 — Quality gates on Go 1.25

- Status: accepted
- Choice: Keep the API on Go 1.25 (`go 1.25.0` in `go.mod`, mise pin `1.25.14`, image `golang:1.25`). Gates are `go test -race`, gosec, govulncheck, gitleaks, and golangci-lint, run through `make`, `mise`, and pre-commit.
- Alternatives: Drop the race detector in CI, or bump the API module to a newer Go so govulncheck and golangci-lint build without `GOTOOLCHAIN=auto`.
- Why: `-race` matches this single-process server. govulncheck v1.8.0 and golangci-lint v2.13.2 need a newer toolchain to compile. `GOTOOLCHAIN=auto` fetches that toolchain for the tool install only. The module and the release image stay on 1.25.
