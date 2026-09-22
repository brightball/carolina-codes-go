package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:postgres@127.0.0.1:5432/carolina_dev"
	}
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(context.Background()); err != nil {
		t.Skipf("postgres ping failed: %v", err)
	}
	return pool
}

func TestLoadSpeakerIncludesProfileLinks(t *testing.T) {
	pool := testPool(t)
	requireV1Catalog(t, pool)
	speaker, err := loadSpeaker(context.Background(), pool, "diana-pham")
	if err != nil {
		t.Skipf("CMS fixture speaker diana-pham unavailable: %v", err)
	}
	if speaker["linkedin_url"] == nil || speaker["linkedin_url"] == "" {
		t.Fatalf("expected linkedin_url on year-scoped speaker payload, got %#v", speaker)
	}
	for _, key := range []string{"twitter_url", "website_url", "github_url"} {
		if _, ok := speaker[key]; !ok {
			t.Fatalf("missing %s on speaker payload", key)
		}
	}
}

func TestLoadYearSponsorIncludesSocialURLs(t *testing.T) {
	pool := testPool(t)
	requireV1Catalog(t, pool)
	sponsor, err := loadYearSponsor(context.Background(), pool, 2026, "flywheel")
	if err != nil {
		t.Skipf("CMS fixture year sponsor flywheel unavailable: %v", err)
	}
	twitter, _ := sponsor["twitter_url"].(*string)
	linkedin, _ := sponsor["linkedin_url"].(*string)
	if (twitter == nil || *twitter == "") && (linkedin == nil || *linkedin == "") {
		t.Fatalf("expected flywheel social URL on year-scoped payload, got %#v", sponsor)
	}
	for _, key := range []string{"youtube_url", "instagram_url", "facebook_url"} {
		if _, ok := sponsor[key]; !ok {
			t.Fatalf("missing %s on sponsor payload", key)
		}
	}
}

func TestLoadSponsorIncludesSocialURLs(t *testing.T) {
	pool := testPool(t)
	requireV1Catalog(t, pool)
	sponsor, err := loadSponsor(context.Background(), pool, "flywheel")
	if err != nil {
		t.Skipf("CMS fixture sponsor flywheel unavailable: %v", err)
	}
	if _, ok := sponsor["twitter_url"]; !ok {
		t.Fatalf("missing twitter_url on sponsor payload, got %#v", sponsor)
	}
}

func requireV1Catalog(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(), "SELECT 1 FROM v1_years LIMIT 1").Scan(&n)
	if err != nil {
		t.Skipf("v1_* views unavailable: %v", err)
	}
}

func TestListenAddrIsIPv6(t *testing.T) {
	if got := listenAddr("4002"); got != "[::]:4002" {
		t.Fatalf("listenAddr = %q, want [::]:4002", got)
	}
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if strings.Contains(body, `ListenAndServe(":"+port`) {
		t.Fatal("ListenAndServe still uses IPv4-unspecified :port")
	}
	if !strings.Contains(body, "listenAddr(port)") {
		t.Fatal("expected ListenAndServe(listenAddr(port))")
	}
}

func TestHealthDoesNotQueryOrConnect(t *testing.T) {
	resetCounts()
	rr := httptest.NewRecorder()
	newHandler(nil).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"ok":true`) {
		t.Fatalf("body %s", rr.Body.String())
	}
	if sqlCount.Load() != 0 {
		t.Fatalf("health ran SQL: %d", sqlCount.Load())
	}
	if connectCount.Load() != 0 {
		t.Fatalf("health opened postgres: %d", connectCount.Load())
	}
}

func TestRegisterDoesNotQueryCatalog(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	i := strings.Index(body, "func register(")
	if i < 0 {
		t.Fatal("register missing")
	}
	fn := body[i:]
	if j := strings.Index(fn, "\nfunc getenv"); j > 0 {
		fn = fn[:j]
	}
	for _, needle := range []string{"dbQuery", "openPool", "pgxpool.New"} {
		if strings.Contains(fn, needle) {
			t.Fatalf("register uses %s", needle)
		}
	}
}

func speakerYears(sp map[string]any) []int {
	switch years := sp["years"].(type) {
	case []int:
		return years
	case []any:
		out := make([]int, 0, len(years))
		for _, y := range years {
			switch n := y.(type) {
			case float64:
				out = append(out, int(n))
			case int:
				out = append(out, n)
			case json.Number:
				i, _ := n.Int64()
				out = append(out, int(i))
			}
		}
		return out
	default:
		return nil
	}
}

func assertYearsDesc(t *testing.T, speakers []map[string]any) {
	t.Helper()
	foundMulti := false
	for _, sp := range speakers {
		years := speakerYears(sp)
		if len(years) < 2 {
			continue
		}
		foundMulti = true
		for i := 1; i < len(years); i++ {
			if years[i-1] < years[i] {
				t.Fatalf("years not DESC for %v: %v", sp["slug"], years)
			}
		}
	}
	if !foundMulti {
		t.Fatal("expected a speaker with >=2 years")
	}
}

func TestYearListingSQLBoundedAndYearsDesc(t *testing.T) {
	pool := privateCatalog(t)
	bootConnects := connectCount.Load()
	resetCounts()
	connectCount.Store(bootConnects)
	year := 2026
	out, err := listSpeakers(context.Background(), pool, &year)
	if err != nil {
		t.Fatalf("listSpeakers: %v", err)
	}
	sql := sqlCount.Load()
	n := len(out)
	t.Logf("year list sql=%d speakers=%d connects=%d", sql, n, connectCount.Load())
	if n < 3 {
		t.Fatalf("expected N>=3 speakers, got %d", n)
	}
	if sql <= 0 {
		t.Fatal("listing ran no SQL")
	}
	if sql >= int64(2*n) {
		t.Fatalf("sql %d grew like 2N for N=%d", sql, n)
	}
	if sql > 4 {
		t.Fatalf("sql %d should be speakers+talks+years", sql)
	}
	assertYearsDesc(t, out)
	if connectCount.Load() != bootConnects {
		t.Fatalf("listing opened a new session: boot=%d now=%d", bootConnects, connectCount.Load())
	}
	resetCounts()
	connectCount.Store(bootConnects)
	if _, err := listSpeakers(context.Background(), pool, &year); err != nil {
		t.Fatalf("second listSpeakers: %v", err)
	}
	if connectCount.Load() != bootConnects {
		t.Fatalf("second listing opened a new session: %d -> %d", bootConnects, connectCount.Load())
	}
}

func TestHandlerYearListingUsesShippedPath(t *testing.T) {
	pool := privateCatalog(t)
	bootConnects := connectCount.Load()
	resetCounts()
	connectCount.Store(bootConnects)
	handler := newHandler(pool)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/speakers?year=2026", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	n := len(payload.Data)
	sql := sqlCount.Load()
	t.Logf("handler year list sql=%d speakers=%d connects=%d", sql, n, connectCount.Load())
	if n < 3 {
		t.Fatalf("handler returned %d speakers", n)
	}
	if sql <= 0 {
		t.Fatal("handler listing ran no SQL")
	}
	if sql >= int64(2*n) {
		t.Fatalf("handler sql %d grew like 2N for N=%d", sql, n)
	}
	if sql > 4 {
		t.Fatalf("handler sql %d", sql)
	}
	assertYearsDesc(t, payload.Data)
	if connectCount.Load() != bootConnects {
		t.Fatalf("handler listing opened a new session: boot=%d now=%d", bootConnects, connectCount.Load())
	}
	resetCounts()
	connectCount.Store(bootConnects)
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/v1/speakers?year=2026", nil))
	if rr2.Code != 200 {
		t.Fatalf("second status %d body %s", rr2.Code, rr2.Body.String())
	}
	if connectCount.Load() != bootConnects {
		t.Fatalf("second handler listing opened a new session: %d -> %d", bootConnects, connectCount.Load())
	}
}

func TestQualityGatesCoverFiveChecks(t *testing.T) {
	pre, err := os.ReadFile(".pre-commit-config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	preBody := string(pre)
	if !strings.Contains(preBody, "gitleaks") {
		t.Fatal("pre-commit config missing gitleaks by name")
	}
	for _, needle := range []string{
		"id: test",
		"id: sast",
		"id: vuln",
		"id: gitleaks",
		"id: lint",
		"make test",
		"make sast",
		"make vuln",
		"make gitleaks",
		"make lint",
	} {
		if !strings.Contains(preBody, needle) {
			t.Fatalf("pre-commit config missing %q", needle)
		}
	}

	wf, err := os.ReadFile(".gitea/workflows/precommit.yml")
	if err != nil {
		t.Fatal(err)
	}
	wfBody := string(wf)
	if strings.Contains(wfBody, "git init") {
		t.Fatal("Gitea workflow must not git init")
	}
	if strings.Contains(wfBody, "init.defaultBranch") {
		t.Fatal("Gitea workflow must not set init.defaultBranch")
	}
	if !strings.Contains(wfBody, "gitleaks") {
		t.Fatal("Gitea workflow missing gitleaks by name")
	}

	mk, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatal(err)
	}
	mkBody := string(mk)
	if !strings.Contains(mkBody, "go install github.com/zricethezav/gitleaks/v8@") {
		t.Fatal("Makefile gitleaks fallback must go-install github.com/zricethezav/gitleaks/v8")
	}
	if !strings.Contains(mkBody, "go test -race ./...") {
		t.Fatal("Makefile test target must run go test -race ./...")
	}
	if strings.Contains(mkBody, "github.com/gitleaks/gitleaks/v8") {
		t.Fatal("Makefile uses github.com/gitleaks/gitleaks/v8 which does not match v8.30.1 go.mod")
	}

	jobs := giteaJobNames(wfBody)
	checkNames := []string{"test", "sast", "vuln", "gitleaks", "lint"}
	for _, want := range checkNames {
		if _, ok := jobs[want]; !ok {
			t.Fatalf("Gitea workflow missing job %q; have %v", want, jobKeys(jobs))
		}
	}
	prepareName := firstStageJobName(jobs, checkNames)
	if prepareName == "" {
		t.Fatalf("Gitea workflow missing a distinct first-stage job; have %v", jobKeys(jobs))
	}
	prepareSrc := jobSourceText(t, jobs[prepareName])
	if !strings.Contains(prepareSrc, "git clone") {
		t.Fatal("first-stage job must token-clone the repo")
	}
	if !strings.Contains(prepareSrc, "GITHUB_SHA") {
		t.Fatal("first-stage job must clone GITHUB_SHA")
	}
	if !strings.Contains(prepareSrc, "x-access-token") {
		t.Fatal("first-stage job must clone over HTTPS with the job token")
	}
	if !strings.Contains(prepareSrc, "go mod download") {
		t.Fatal("first-stage job must fetch Go modules")
	}
	if !strings.Contains(prepareSrc, "go install github.com/zricethezav/gitleaks/v8@v8.30.1") {
		t.Fatal("first-stage job must go-install github.com/zricethezav/gitleaks/v8@v8.30.1 (v8.30.1 go.mod path)")
	}
	if !strings.Contains(prepareSrc, "github.com/securego/gosec/v2/cmd/gosec") {
		t.Fatal("first-stage job must install gosec")
	}
	if !strings.Contains(prepareSrc, "golang.org/x/vuln/cmd/govulncheck") {
		t.Fatal("first-stage job must install govulncheck")
	}
	if !strings.Contains(prepareSrc, "github.com/golangci/golangci-lint") {
		t.Fatal("first-stage job must install golangci-lint")
	}
	if strings.Contains(wfBody, "github.com/gitleaks/gitleaks/v8") || strings.Contains(prepareSrc, "github.com/gitleaks/gitleaks/v8") {
		t.Fatal("Gitea workflow uses github.com/gitleaks/gitleaks/v8 which does not match v8.30.1 go.mod")
	}
	if !strings.Contains(jobs["test"], "postgres:16") {
		t.Fatal("Gitea test job must supply Postgres 16")
	}

	wantCmd := map[string]string{
		"test":     "go test -race ./...",
		"sast":     "gosec ./...",
		"vuln":     "govulncheck ./...",
		"gitleaks": "gitleaks detect --source .",
		"lint":     "golangci-lint run",
	}
	for name, cmd := range wantCmd {
		if !strings.Contains(jobs[name], cmd) {
			t.Fatalf("Gitea job %q missing %q", name, cmd)
		}
		if !jobNeedsNamed(jobs[name], prepareName) {
			t.Fatalf("job %q must need first-stage job %q", name, prepareName)
		}
		for _, other := range checkNames {
			if other != name && jobNeedsNamed(jobs[name], other) {
				t.Fatalf("job %q must not need check job %q", name, other)
			}
		}
		if strings.Contains(jobs[name], "git clone") {
			t.Fatalf("job %q re-clones the repo instead of using the prepared environment", name)
		}
		if strings.Contains(jobs[name], "go install") {
			t.Fatalf("job %q re-installs tools instead of using the prepared environment", name)
		}
		if !strings.Contains(jobs[name], "*restore-prepared-env") && !strings.Contains(jobs[name], "ci-env.sh restore") {
			t.Fatalf("job %q does not restore the prepared environment", name)
		}
	}
	if !strings.Contains(wfBody, "ci-env.sh restore") {
		t.Fatal("workflow missing prepared-environment restore")
	}

	combined := 0
	for _, body := range jobs {
		n := 0
		for _, tool := range []string{"go test -race ./...", "gosec ./...", "govulncheck ./...", "gitleaks detect", "golangci-lint run"} {
			if strings.Contains(body, tool) {
				n++
			}
		}
		if n == 5 {
			combined++
		}
	}
	if combined > 0 {
		t.Fatal("Gitea workflow has a single job that runs every check")
	}
}

func giteaJobNames(wf string) map[string]string {
	jobsIdx := strings.Index(wf, "\njobs:")
	if jobsIdx < 0 {
		return nil
	}
	rest := wf[jobsIdx+len("\njobs:"):]
	out := map[string]string{}
	var current string
	var b strings.Builder
	flush := func() {
		if current != "" {
			out[current] = b.String()
			b.Reset()
		}
	}
	for _, line := range strings.Split(rest, "\n") {
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") && strings.HasSuffix(line, ":") {
			name := strings.TrimSuffix(strings.TrimSpace(line), ":")
			if name != "" && !strings.Contains(name, " ") {
				flush()
				current = name
				continue
			}
		}
		if current != "" {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	flush()
	return out
}

func jobKeys(jobs map[string]string) []string {
	keys := make([]string, 0, len(jobs))
	for k := range jobs {
		keys = append(keys, k)
	}
	return keys
}

func firstStageJobName(jobs map[string]string, checks []string) string {
	isCheck := map[string]bool{}
	for _, name := range checks {
		isCheck[name] = true
	}
	if _, ok := jobs["prepare"]; ok && !isCheck["prepare"] {
		return "prepare"
	}
	for name := range jobs {
		if !isCheck[name] {
			return name
		}
	}
	return ""
}

func jobNeedsNamed(body, name string) bool {
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "needs: "+name {
			return true
		}
	}
	return false
}

func workflowScripts(body string) []string {
	re := regexp.MustCompile(`scripts/[A-Za-z0-9._/-]+\.sh`)
	seen := map[string]bool{}
	var out []string
	for _, m := range re.FindAllString(body, -1) {
		if seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

func jobSourceText(t *testing.T, body string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(body)
	for _, p := range workflowScripts(body) {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read helper %s: %v", p, err)
		}
		b.WriteByte('\n')
		b.Write(data)
	}
	return b.String()
}

func TestYearTalksIncludeLanguages(t *testing.T) {
	pool := testPool(t)
	requireV1Catalog(t, pool)
	year := 2026
	talks := loadTalks(context.Background(), pool, "paul-sullivan", &year)
	if len(talks) == 0 {
		t.Skip("CMS fixture talks for paul-sullivan 2026 unavailable")
	}
	langs := uniqTalkField(talks, "languages")
	found := false
	for _, lang := range langs {
		if lang == "elixir" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected elixir on paul-sullivan 2026 talks, got %#v", langs)
	}
}
