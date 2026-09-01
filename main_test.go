package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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
	speaker, err := loadSpeaker(context.Background(), pool, "diana-pham")
	if err != nil {
		t.Fatalf("loadSpeaker: %v", err)
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
	sponsor, err := loadYearSponsor(context.Background(), pool, 2026, "flywheel")
	if err != nil {
		t.Fatalf("loadYearSponsor: %v", err)
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
	sponsor, err := loadSponsor(context.Background(), pool, "flywheel")
	if err != nil {
		t.Fatalf("loadSponsor: %v", err)
	}
	if _, ok := sponsor["twitter_url"]; !ok {
		t.Fatalf("missing twitter_url on sponsor payload, got %#v", sponsor)
	}
}

func livePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:postgres@127.0.0.1:5432/carolina_dev"
	}
	pool, err := openPool(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("openPool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("postgres ping failed: %v", err)
	}
	return pool
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
	pool := livePool(t)
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
	pool := livePool(t)
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

func TestYearTalksIncludeLanguages(t *testing.T) {
	pool := testPool(t)
	year := 2026
	talks := loadTalks(context.Background(), pool, "paul-sullivan", &year)
	if len(talks) == 0 {
		t.Fatal("expected 2026 talks for paul-sullivan")
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
