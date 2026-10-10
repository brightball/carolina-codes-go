package main

import (
	"regexp"
	"strings"
	"testing"
)

func TestShippedDocsMatchContract(t *testing.T) {
	agents := string(readRepo(t, "AGENTS.md"))
	readme := string(readRepo(t, "README.md"))
	decisions := string(readRepo(t, "DECISIONS.md"))
	memory := string(readRepo(t, "MEMORY.md"))
	goMod := string(readRepo(t, "go.mod"))
	mise := string(readRepo(t, "mise.toml"))
	dockerfile := string(readRepo(t, "Dockerfile"))

	if strings.TrimSpace(agents) == "" || strings.TrimSpace(decisions) == "" || strings.TrimSpace(memory) == "" || strings.TrimSpace(readme) == "" {
		t.Fatal("AGENTS.md, README.md, DECISIONS.md, and MEMORY.md must all be non-empty")
	}

	requireSnippets(t, "AGENTS.md", agents, []string{
		"read-only",
		"v1_speakers",
		"v1_sponsors",
		"v1_years",
		"v1_talks",
		"v1_sponsorships",
		"v1_year_speakers",
		"v1_year_sponsors",
		"Ash",
		"base tables",
		"GET /health",
		"GET /",
		"GET /v1/years",
		"GET /v1/speakers",
		"GET /v1/speakers?year=",
		"GET /v1/speakers/{slug}",
		"GET /v1/speakers/{year}/{slug}",
		"GET /v1/sponsors",
		"GET /v1/sponsors?year=",
		"GET /v1/sponsors/{slug}",
		"GET /v1/sponsors/{year}/{slug}",
		`{ "data": [ ... ] }`,
		"POST {CAROLINA_URL}/internal/api-endpoints/register",
		"Authorization: Bearer",
		"no heartbeat",
		"If `CAROLINA_URL` is empty or the POST fails",
		"keep serving",
		"DATABASE_URL",
		"CAROLINA_URL",
		"POLYGLOT_REGISTER_TOKEN",
		"PUBLIC_BASE_URL",
		"PORT",
		"Go",
		"net/http",
		"Not gin, echo, or chi",
		"github.com/jackc/pgx/v5",
		"priv/api/openapi.yaml",
		"priv/api/AGENTS.md",
		"does not vendor OpenAPI or `db/`",
		"Postgres 16",
		"carolina_dev",
		"4002",
		"8080",
		"[::]",
		"6PN",
		"listens without waiting on a Postgres ping or registration",
		"MinConns` 0",
		"capped `MaxConns`",
		`{"ok": true}`,
		"does not touch the database",
		`auto_stop_machines = "suspend"`,
		"min_machines_running = 0",
		"256mb",
		"GOMAXPROCS=1",
		"GOMEMLIMIT=200MiB",
		"golang:1.26.9",
		"static non-root",
		"scratch",
		"CGO_ENABLED=0",
		"go test -race",
		"gosec",
		"govulncheck",
		"gitleaks",
		"golangci-lint",
		"`make`",
		"`mise`",
		"pre-commit",
		"workspace root",
		"CMS git remote",
		"Read `DECISIONS.md` before an architectural change",
		"append a dated entry",
		"Read `MEMORY.md`",
		"Update `MEMORY.md` when those operational facts change",
		"`MEMORY.md` is not a second decision log",
	})

	requireSnippets(t, "DECISIONS.md", decisions, []string{
		"Append-only",
		"net/http",
		"gin, echo, or chi",
		"github.com/jackc/pgx/v5",
		"v1_speakers",
		"v1_year_speakers",
		"v1_year_sponsors",
		"Ash",
		"Listen before dial",
		"background",
		"MinConns",
		"no heartbeat",
		"[::]",
		"6PN",
		`auto_stop_machines = "stop"`,
		"not suspend",
		"min_machines_running = 0",
		"256mb",
		"GOMAXPROCS=1",
		"GOMEMLIMIT=200MiB",
		"scratch",
		"golang:1.25",
		"CGO_ENABLED=0",
		"go test -race",
		"gosec",
		"govulncheck",
		"gitleaks",
		"golangci-lint",
		"Go 1.25",
		"not a second decision log",
	})
	assertDecisionEntries(t, decisions)

	requireSnippets(t, "MEMORY.md", memory, []string{
		"go run .",
		"make test",
		"go test -race",
		"4002",
		"8080",
		"Postgres 16",
		"carolina_dev",
		"priv/api/openapi.yaml",
		"priv/api/AGENTS.md",
		"fake catalog do not need Postgres",
		"not a decision log",
		"DECISIONS.md",
	})
	if strings.Contains(memory, "Status:") || strings.Contains(memory, "Choice:") || strings.Contains(memory, "Alternatives:") {
		t.Fatal("MEMORY.md must stay operational and must not become a second decision log")
	}

	goVersion := mustSubmatch(t, `(?m)^go\s+(\d+\.\d+\.\d+)\s*$`, goMod)
	pgxVersion := mustSubmatch(t, `(?m)^require\s+github\.com/jackc/pgx/v5\s+(v\d+\.\d+\.\d+)\s*$`, goMod)
	miseGo := mustSubmatch(t, `(?m)^go\s*=\s*"([^"]+)"\s*$`, mise)
	image := mustSubmatch(t, `(?m)^FROM\s+(golang:\d+\.\d+)`, dockerfile)
	langMinor := goVersion[:strings.LastIndex(goVersion, ".")]

	requireSnippets(t, "README.md", readme, []string{
		"Go " + langMinor,
		"go " + goVersion,
		miseGo,
		image,
		"net/http",
		"no separate module version",
		"github.com/jackc/pgx/v5",
		pgxVersion,
	})
	if strings.Contains(strings.ToLower(readme), "crac") {
		t.Fatal("README.md must not document CRaC")
	}

	for _, nameBody := range []struct {
		name string
		body string
	}{
		{"AGENTS.md", agents},
		{"README.md", readme},
		{"DECISIONS.md", decisions},
		{"MEMORY.md", memory},
	} {
		assertNoPrivateData(t, nameBody.name, nameBody.body)
	}
}

func assertDecisionEntries(t *testing.T, body string) {
	t.Helper()
	heading := regexp.MustCompile(`(?m)^## (\d{4}-\d{2}-\d{2}) — .+$`)
	indexes := heading.FindAllStringIndex(body, -1)
	if len(indexes) < 6 {
		t.Fatalf("DECISIONS.md has %d dated entries, want at least 6", len(indexes))
	}
	for i, span := range indexes {
		end := len(body)
		if i+1 < len(indexes) {
			end = indexes[i+1][0]
		}
		entry := body[span[0]:end]
		for _, label := range []string{"Status:", "Choice:", "Alternatives:", "Why:"} {
			if !strings.Contains(entry, label) {
				t.Fatalf("decision entry missing %s:\n%s", label, entry)
			}
		}
		if !strings.Contains(entry, "accepted") {
			t.Fatalf("decision entry status is not accepted:\n%s", entry)
		}
	}
}

func assertNoPrivateData(t *testing.T, name, body string) {
	t.Helper()
	lower := strings.ToLower(body)
	for _, host := range []string{".ts.net", "zebra-hydra", "gitea.zebra"} {
		if strings.Contains(lower, host) {
			t.Fatalf("%s contains private hostname %q", name, host)
		}
	}
	assign := regexp.MustCompile(`(?i)\b([A-Za-z0-9_]*(?:TOKEN|SECRET|PASSWORD|API_KEY|APIKEY)[A-Za-z0-9_]*)\s*=\s*([^\s` + "`" + `]+)`)
	for _, match := range assign.FindAllStringSubmatch(body, -1) {
		key, val := match[1], strings.Trim(match[2], `"'`)
		if strings.EqualFold(key, "POLYGLOT_REGISTER_TOKEN") && val == "dev" {
			continue
		}
		t.Fatalf("%s has secret-looking assignment %s=%s", name, key, val)
	}
	urls := regexp.MustCompile(`postgres(?:ql)?://[^\s` + "`" + `)]+`)
	for _, raw := range urls.FindAllString(body, -1) {
		const allowed = "postgres://postgres:postgres@127.0.0.1:5432/carolina_dev"
		if raw != allowed {
			t.Fatalf("%s has non-public postgres URL %s", name, raw)
		}
	}
	for _, marker := range []string{"ghp_", "github_pat_", "sk-", "AKIA", "xoxb-", "xoxp-"} {
		if strings.Contains(body, marker) {
			t.Fatalf("%s contains secret-looking marker %q", name, marker)
		}
	}
}

func requireSnippets(t *testing.T, name, body string, snippets []string) {
	t.Helper()
	for _, snippet := range snippets {
		if !strings.Contains(body, snippet) {
			t.Fatalf("%s missing %q", name, snippet)
		}
	}
}

func mustSubmatch(t *testing.T, pattern, body string) string {
	t.Helper()
	match := regexp.MustCompile(pattern).FindStringSubmatch(body)
	if len(match) < 2 || match[1] == "" {
		t.Fatalf("pattern %s did not match", pattern)
	}
	return match[1]
}
