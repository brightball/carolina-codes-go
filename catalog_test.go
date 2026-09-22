package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	catalogOnce sync.Once
	catalogPool *pgxpool.Pool
	catalogName string
	catalogDrop func()
	catalogErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if catalogPool != nil {
		catalogPool.Close()
	}
	if catalogDrop != nil {
		catalogDrop()
	}
	os.Exit(code)
}

func privateCatalog(t *testing.T) *pgxpool.Pool {
	t.Helper()
	catalogOnce.Do(func() {
		catalogPool, catalogName, catalogDrop, catalogErr = createPrivateCatalog()
	})
	if catalogErr != nil {
		t.Fatalf("seed v1 catalog: %v", catalogErr)
	}
	t.Logf("private catalog %s", catalogName)
	return catalogPool
}

func createPrivateCatalog() (*pgxpool.Pool, string, func(), error) {
	adminURL := os.Getenv("DATABASE_URL")
	if adminURL == "" {
		adminURL = "postgres://postgres:postgres@127.0.0.1:5432/carolina_dev?sslmode=disable"
	}
	adminName, err := databaseName(adminURL)
	if err != nil {
		return nil, "", nil, err
	}
	dbName := fmt.Sprintf("ccgo_%d_%d", os.Getpid(), time.Now().UnixNano()%1_000_000_000)
	if dbName == adminName || !strings.HasPrefix(dbName, "ccgo_") {
		return nil, "", nil, fmt.Errorf("refusing to seed database %s", dbName)
	}
	appURL, err := withDatabase(adminURL, dbName)
	if err != nil {
		return nil, "", nil, err
	}
	drop := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, adminURL)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		ident := pgx.Identifier{dbName}.Sanitize()
		_, _ = conn.Exec(ctx, "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return nil, "", nil, err
	}
	ident := pgx.Identifier{dbName}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE DATABASE "+ident)
	closeAdmin := admin.Close(ctx)
	if err != nil {
		return nil, "", nil, err
	}
	if closeAdmin != nil {
		return nil, "", nil, closeAdmin
	}

	conn, err := pgx.Connect(ctx, appURL)
	if err != nil {
		drop()
		return nil, "", nil, err
	}
	for _, stmt := range catalogSchema {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			_ = conn.Close(ctx)
			drop()
			return nil, "", nil, fmt.Errorf("%s: %w", stmt, err)
		}
	}
	if err := conn.Close(ctx); err != nil {
		drop()
		return nil, "", nil, err
	}

	pool, err := openPool(ctx, appURL)
	if err != nil {
		drop()
		return nil, "", nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		drop()
		return nil, "", nil, err
	}
	return pool, dbName, drop, nil
}

func databaseName(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	name := strings.TrimPrefix(u.Path, "/")
	if name == "" {
		return "", fmt.Errorf("database url %q has no database name", raw)
	}
	return name, nil
}

func withDatabase(raw, name string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "", fmt.Errorf("unsupported database url scheme %q", u.Scheme)
	}
	u.Path = "/" + name
	return u.String(), nil
}

func TestSeededV1Routes(t *testing.T) {
	pool := privateCatalog(t)
	rec := &recordingQuerier{q: pool}
	handler := newHandler(rec)
	bootConnects := connectCount.Load()

	resetCounts()
	before := len(rec.queries())
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"ok":true`) {
		t.Fatalf("health %d %s", rr.Code, rr.Body.String())
	}
	root := httptest.NewRecorder()
	handler.ServeHTTP(root, httptest.NewRequest(http.MethodGet, "/", nil))
	if root.Code != 200 {
		t.Fatalf("index %d %s", root.Code, root.Body.String())
	}
	assertIndexJSON(t, root.Body.String())
	if sqlCount.Load() != 0 || len(rec.queries()) != before {
		t.Fatalf("health or index touched SQL: count=%d queries=%v", sqlCount.Load(), rec.queries())
	}
	if connectCount.Load() != 0 {
		t.Fatalf("health or index opened a pool: %d", connectCount.Load())
	}

	yearsRaw := mustData(t, handler, "/v1/years")
	var years []map[string]any
	if err := json.Unmarshal(yearsRaw, &years); err != nil {
		t.Fatal(err)
	}
	if len(years) < 2 || asInt(t, years[0]["year"]) != 2026 || asInt(t, years[1]["year"]) != 2025 {
		t.Fatalf("years %#v", years)
	}

	speakersRaw := mustData(t, handler, "/v1/speakers")
	var speakers []map[string]any
	if err := json.Unmarshal(speakersRaw, &speakers); err != nil {
		t.Fatal(err)
	}
	if len(speakers) < 3 {
		t.Fatalf("speakers %#v", speakers)
	}

	resetCounts()
	connectCount.Store(bootConnects)
	yearRaw := mustData(t, handler, "/v1/speakers?year=2026")
	var yearSpeakers []map[string]any
	if err := json.Unmarshal(yearRaw, &yearSpeakers); err != nil {
		t.Fatal(err)
	}
	sql := sqlCount.Load()
	n := len(yearSpeakers)
	if n < 3 {
		t.Fatalf("year speakers %d", n)
	}
	if sql <= 0 || sql > 4 || sql >= int64(2*n) {
		t.Fatalf("year listing sql=%d speakers=%d", sql, n)
	}
	assertYearsDesc(t, yearSpeakers)
	if connectCount.Load() != bootConnects {
		t.Fatalf("year listing opened a pool: %d -> %d", bootConnects, connectCount.Load())
	}
	ada := findSlug(t, yearSpeakers, "ada-lovelace")
	if !containsAll(stringList(ada["languages"]), "elixir", "go") {
		t.Fatalf("ada languages %#v", ada["languages"])
	}
	if !containsAll(stringList(ada["topics"]), "compilers", "history") {
		t.Fatalf("ada topics %#v", ada["topics"])
	}
	for _, sp := range yearSpeakers {
		if _, ok := sp["languages"]; !ok {
			t.Fatalf("missing languages on %#v", sp["slug"])
		}
		if _, ok := sp["topics"]; !ok {
			t.Fatalf("missing topics on %#v", sp["slug"])
		}
	}

	speakerRaw := mustData(t, handler, "/v1/speakers/ada-lovelace")
	var speaker map[string]any
	if err := json.Unmarshal(speakerRaw, &speaker); err != nil {
		t.Fatal(err)
	}
	if speaker["linkedin_url"] == nil || speaker["linkedin_url"] == "" {
		t.Fatalf("speaker detail %#v", speaker)
	}
	if _, ok := speaker["talks"]; !ok {
		t.Fatal("speaker detail missing talks")
	}

	detailRaw := mustData(t, handler, "/v1/speakers/2026/ada-lovelace")
	var detail map[string]any
	if err := json.Unmarshal(detailRaw, &detail); err != nil {
		t.Fatal(err)
	}
	if !containsAll(stringList(detail["languages"]), "elixir", "go") {
		t.Fatalf("year speaker languages %#v", detail["languages"])
	}
	other := intList(detail["other_years"])
	if len(other) != 1 || other[0] != 2025 {
		t.Fatalf("other_years %#v", detail["other_years"])
	}
	yearsDesc := intList(detail["years"])
	if len(yearsDesc) < 2 || yearsDesc[0] < yearsDesc[1] {
		t.Fatalf("years %#v", detail["years"])
	}

	sponsorsRaw := mustData(t, handler, "/v1/sponsors")
	var sponsors []map[string]any
	if err := json.Unmarshal(sponsorsRaw, &sponsors); err != nil {
		t.Fatal(err)
	}
	fly := findSlug(t, sponsors, "flywheel")
	if fly["twitter_url"] == nil || fly["twitter_url"] == "" {
		t.Fatalf("sponsor %#v", fly)
	}

	yearSponsorsRaw := mustData(t, handler, "/v1/sponsors?year=2026")
	var yearSponsors []map[string]any
	if err := json.Unmarshal(yearSponsorsRaw, &yearSponsors); err != nil {
		t.Fatal(err)
	}
	if len(yearSponsors) < 2 {
		t.Fatalf("year sponsors %#v", yearSponsors)
	}
	for _, sp := range yearSponsors {
		if sp["tier"] == nil || sp["tier"] == "" {
			t.Fatalf("missing tier on %#v", sp)
		}
	}
	if findSlug(t, yearSponsors, "flywheel")["tier"] != "gold" {
		t.Fatalf("flywheel tier %#v", findSlug(t, yearSponsors, "flywheel")["tier"])
	}

	oneSponsor := mustObject(t, handler, "/v1/sponsors/flywheel")
	if oneSponsor["twitter_url"] == "" || oneSponsor["twitter_url"] == nil {
		t.Fatalf("sponsor detail %#v", oneSponsor)
	}
	yearSponsor := mustObject(t, handler, "/v1/sponsors/2026/flywheel")
	if yearSponsor["tier"] != "gold" {
		t.Fatalf("year sponsor %#v", yearSponsor)
	}
	sponsorOther := intList(yearSponsor["other_years"])
	if len(sponsorOther) != 1 || sponsorOther[0] != 2025 {
		t.Fatalf("sponsor other_years %#v", yearSponsor["other_years"])
	}

	for _, path := range []string{
		"/v1/speakers/missing-person",
		"/v1/speakers/2026/missing-person",
		"/v1/speakers/1999/ada-lovelace",
		"/v1/sponsors/missing-org",
		"/v1/sponsors/2026/missing-org",
		"/v1/sponsors/1999/flywheel",
	} {
		assertNotFound(t, handler, path)
	}

	assertV1SQL(t, rec.queries())
	if got := connectCount.Load(); got != bootConnects {
		t.Fatalf("routes opened another pool: %d -> %d", bootConnects, got)
	}
}

func mustData(t *testing.T, h http.Handler, path string) json.RawMessage {
	t.Helper()
	rr := doReq(t, h, path)
	if rr.Code != 200 {
		t.Fatalf("%s status %d body %s", path, rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("%s content-type %s", path, ct)
	}
	if rr.Header().Get("X-Polyglot-Language") != language || rr.Header().Get("X-Polyglot-Framework") != framework {
		t.Fatalf("%s polyglot headers", path)
	}
	var wrap struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &wrap); err != nil {
		t.Fatalf("%s json: %v body %s", path, err, rr.Body.String())
	}
	if len(wrap.Data) == 0 || string(wrap.Data) == "null" {
		t.Fatalf("%s missing data: %s", path, rr.Body.String())
	}
	return wrap.Data
}

func mustObject(t *testing.T, h http.Handler, path string) map[string]any {
	t.Helper()
	raw := mustData(t, h, path)
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	return obj
}

func doReq(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
}

func assertNotFound(t *testing.T, h http.Handler, path string) {
	t.Helper()
	rr := doReq(t, h, path)
	if rr.Code != 404 {
		t.Fatalf("%s status %d body %s", path, rr.Code, rr.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("%s 404 is not JSON: %s", path, rr.Body.String())
	}
	if payload["error"] != "not_found" {
		t.Fatalf("%s 404 %#v", path, payload)
	}
}

func findSlug(t *testing.T, rows []map[string]any, slug string) map[string]any {
	t.Helper()
	for _, row := range rows {
		if row["slug"] == slug {
			return row
		}
	}
	t.Fatalf("missing slug %s in %#v", slug, rows)
	return nil
}

func stringList(v any) []string {
	switch vals := v.(type) {
	case []string:
		return vals
	case []any:
		out := make([]string, 0, len(vals))
		for _, val := range vals {
			s, ok := val.(string)
			if ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func intList(v any) []int {
	switch vals := v.(type) {
	case []int:
		return vals
	case []any:
		out := make([]int, 0, len(vals))
		for _, val := range vals {
			switch n := val.(type) {
			case float64:
				out = append(out, int(n))
			case int:
				out = append(out, n)
			}
		}
		return out
	default:
		return nil
	}
}

func asInt(t *testing.T, v any) int {
	t.Helper()
	n, ok := v.(float64)
	if !ok {
		t.Fatalf("want number, got %#v", v)
	}
	return int(n)
}

func containsAll(got []string, want ...string) bool {
	have := map[string]bool{}
	for _, g := range got {
		have[g] = true
	}
	for _, w := range want {
		if !have[w] {
			return false
		}
	}
	return true
}

var relationRE = regexp.MustCompile(`(?i)\b(?:from|join)\s+([a-zA-Z_][\w\.]*)`)

func assertV1SQL(t *testing.T, queries []string) {
	t.Helper()
	if len(queries) == 0 {
		t.Fatal("catalog routes issued no SQL")
	}
	for _, query := range queries {
		found := false
		for _, match := range relationRE.FindAllStringSubmatch(query, -1) {
			found = true
			base := match[1]
			if i := strings.LastIndex(base, "."); i >= 0 {
				base = base[i+1:]
			}
			base = strings.Trim(base, `"`)
			if !strings.HasPrefix(base, "v1_") {
				t.Fatalf("query %q reads %s", query, base)
			}
		}
		if !found {
			t.Fatalf("query has no FROM/JOIN target: %s", query)
		}
	}
}

type recordingQuerier struct {
	q    querier
	mu   sync.Mutex
	sqls []string
}

func (r *recordingQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	r.record(sql)
	return r.q.Query(ctx, sql, args...)
}

func (r *recordingQuerier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	r.record(sql)
	return r.q.QueryRow(ctx, sql, args...)
}

func (r *recordingQuerier) record(sql string) {
	r.mu.Lock()
	r.sqls = append(r.sqls, sql)
	r.mu.Unlock()
}

func (r *recordingQuerier) queries() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.sqls))
	copy(out, r.sqls)
	return out
}

var catalogSchema = []string{
	`CREATE TABLE seed_years (
		year int PRIMARY KEY,
		slug text NOT NULL,
		name text NOT NULL,
		status text NOT NULL
	)`,
	`CREATE VIEW v1_years AS SELECT year, slug, name, status FROM seed_years`,
	`CREATE TABLE seed_speakers (
		slug text PRIMARY KEY,
		first_name text NOT NULL,
		last_name text NOT NULL,
		name text NOT NULL,
		tagline text,
		bio text,
		company text,
		location text,
		photo_path text,
		twitter_url text,
		linkedin_url text,
		website_url text,
		github_url text,
		featured boolean NOT NULL
	)`,
	`CREATE VIEW v1_speakers AS
		SELECT slug, first_name, last_name, name, tagline, bio, company, location, photo_path,
			twitter_url, linkedin_url, website_url, github_url, featured
		FROM seed_speakers`,
	`CREATE TABLE seed_talks (
		slug text PRIMARY KEY,
		title text NOT NULL,
		description text,
		format text,
		youtube_id text,
		year int NOT NULL,
		speaker_slug text NOT NULL,
		languages text[] NOT NULL,
		topics text[] NOT NULL
	)`,
	`CREATE VIEW v1_talks AS
		SELECT slug, title, description, format, youtube_id, year, speaker_slug, languages, topics
		FROM seed_talks`,
	`CREATE TABLE seed_sponsors (
		slug text PRIMARY KEY,
		name text NOT NULL,
		website text,
		logo_path text,
		description text,
		twitter_url text,
		linkedin_url text,
		youtube_url text,
		instagram_url text,
		facebook_url text
	)`,
	`CREATE VIEW v1_sponsors AS
		SELECT slug, name, website, logo_path, description,
			twitter_url, linkedin_url, youtube_url, instagram_url, facebook_url
		FROM seed_sponsors`,
	`CREATE TABLE seed_year_sponsors (
		slug text NOT NULL,
		name text NOT NULL,
		website text,
		logo_path text,
		description text,
		blurb text,
		tier text,
		featured boolean NOT NULL,
		year int NOT NULL,
		twitter_url text,
		linkedin_url text,
		youtube_url text,
		instagram_url text,
		facebook_url text,
		PRIMARY KEY (slug, year)
	)`,
	`CREATE VIEW v1_year_sponsors AS
		SELECT slug, name, website, logo_path, description, blurb, tier, featured, year,
			twitter_url, linkedin_url, youtube_url, instagram_url, facebook_url
		FROM seed_year_sponsors`,
	`CREATE TABLE seed_sponsorships (
		sponsor_slug text NOT NULL,
		year int NOT NULL,
		PRIMARY KEY (sponsor_slug, year)
	)`,
	`CREATE VIEW v1_sponsorships AS SELECT sponsor_slug, year FROM seed_sponsorships`,
	`INSERT INTO seed_years (year, slug, name, status) VALUES
		(2026, 'ccc-2026', 'Carolina Codes 2026', 'published'),
		(2025, 'ccc-2025', 'Carolina Codes 2025', 'archived')`,
	`INSERT INTO seed_speakers (
		slug, first_name, last_name, name, tagline, bio, company, location, photo_path,
		twitter_url, linkedin_url, website_url, github_url, featured
	) VALUES
		('ada-lovelace', 'Ada', 'Lovelace', 'Ada Lovelace', 'poetical science', 'bio', 'Analytical', 'London', '/ada.jpg', 'https://x.com/ada', 'https://linkedin.com/in/ada', 'https://ada.example', 'https://github.com/ada', true),
		('grace-hopper', 'Grace', 'Hopper', 'Grace Hopper', 'cobol', 'bio', 'Navy', 'Arlington', '/grace.jpg', NULL, 'https://linkedin.com/in/grace', NULL, NULL, false),
		('katherine-johnson', 'Katherine', 'Johnson', 'Katherine Johnson', 'orbits', 'bio', 'NASA', 'Hampton', '/kj.jpg', NULL, NULL, 'https://kj.example', NULL, true)`,
	`INSERT INTO seed_talks (slug, title, description, format, youtube_id, year, speaker_slug, languages, topics) VALUES
		('ada-2026', 'Analytical Engine', 'notes', 'talk', 'yt-ada-2026', 2026, 'ada-lovelace', ARRAY['elixir','go']::text[], ARRAY['compilers','history']::text[]),
		('ada-2025', 'Notes on the Engine', 'older', 'talk', NULL, 2025, 'ada-lovelace', ARRAY['go']::text[], ARRAY['history']::text[]),
		('grace-2026', 'Compilers', 'cobol', 'talk', 'yt-grace', 2026, 'grace-hopper', ARRAY['go']::text[], ARRAY['compilers']::text[]),
		('kj-2026', 'Trajectories', 'math', 'talk', 'yt-kj', 2026, 'katherine-johnson', ARRAY['elixir']::text[], ARRAY['math']::text[])`,
	`INSERT INTO seed_sponsors (
		slug, name, website, logo_path, description, twitter_url, linkedin_url, youtube_url, instagram_url, facebook_url
	) VALUES
		('flywheel', 'Flywheel', 'https://flywheel.example', '/fw.png', 'hosting', 'https://x.com/flywheel', 'https://linkedin.com/company/flywheel', NULL, NULL, NULL),
		('northwind', 'Northwind', 'https://northwind.example', '/nw.png', 'supplies', NULL, NULL, 'https://youtube.com/northwind', NULL, NULL)`,
	`INSERT INTO seed_year_sponsors (
		slug, name, website, logo_path, description, blurb, tier, featured, year,
		twitter_url, linkedin_url, youtube_url, instagram_url, facebook_url
	) VALUES
		('flywheel', 'Flywheel', 'https://flywheel.example', '/fw.png', 'hosting', 'gold blurb', 'gold', true, 2026, 'https://x.com/flywheel', 'https://linkedin.com/company/flywheel', NULL, NULL, NULL),
		('flywheel', 'Flywheel', 'https://flywheel.example', '/fw.png', 'hosting', 'silver blurb', 'silver', false, 2025, 'https://x.com/flywheel', 'https://linkedin.com/company/flywheel', NULL, NULL, NULL),
		('northwind', 'Northwind', 'https://northwind.example', '/nw.png', 'supplies', 'bronze blurb', 'bronze', false, 2026, NULL, NULL, 'https://youtube.com/northwind', NULL, NULL)`,
	`INSERT INTO seed_sponsorships (sponsor_slug, year) VALUES
		('flywheel', 2026),
		('flywheel', 2025),
		('northwind', 2026)`,
}
