# Carolina Codes — Go API

Read-only `net/http` + `pgx` API for the Carolina Code Conference polyglot site.

Queries PostgreSQL **v1 views**. Registers with Elixir once on boot.

```bash
go mod tidy
DATABASE_URL=postgres://postgres:postgres@127.0.0.1:5432/carolina_dev \
CAROLINA_URL=http://127.0.0.1:4000 \
POLYGLOT_REGISTER_TOKEN=dev \
PUBLIC_BASE_URL=http://127.0.0.1:4002 \
PORT=4002 \
go run .
```
