# Carolina Codes — Go API

Read-only `net/http` + `pgx` API for the Carolina Code Conference polyglot site.

Language **Go 1.25** (`go 1.25.0` in `go.mod`, mise pin `1.25.14`, container image `golang:1.25`). The framework is standard-library `net/http` and has no separate module version. SQL is `github.com/jackc/pgx/v5` at `v5.9.2`.

Queries PostgreSQL **v1 views**. Listens before dialing Postgres, and registers with Elixir in the background.

```bash
go mod tidy
DATABASE_URL=postgres://postgres:postgres@127.0.0.1:5432/carolina_dev \
CAROLINA_URL=http://127.0.0.1:4000 \
POLYGLOT_REGISTER_TOKEN=dev \
PUBLIC_BASE_URL=http://127.0.0.1:4002 \
PORT=4002 \
go run .
```

## Quality gates

Pinned in `mise.toml` (and the matching `Makefile` versions). Install tools with `mise install`.

```bash
make test        # go test -race ./...
make sast        # gosec ./...
make vuln        # govulncheck ./...
make gitleaks    # gitleaks detect --source .
make lint        # golangci-lint run
make check       # all of the above
make hooks       # install local pre-commit hooks
```

`mise run test|sast|vuln|gitleaks|lint|check` calls the same Makefile targets.

Pre-commit runs the same five checks (`go test -race`, `gosec`, `govulncheck`, `gitleaks`, `golangci-lint`). Install once with `make hooks` (needs `pre-commit` on PATH). Emergency skip: `SKIP=test,sast,vuln,gitleaks,lint git commit`.
