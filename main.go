package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	language      = "Go"
	apiVersion    = "0.2.0"
	framework     = "net/http"
	createdYear   = 2026
	schemaVersion = 1
)

var endpoints = []map[string]any{
	{"method": "GET", "path": "/", "query": []string{}},
	{"method": "GET", "path": "/health", "query": []string{}},
	{"method": "GET", "path": "/v1/years", "query": []string{}},
	{"method": "GET", "path": "/v1/speakers", "query": []string{"year"}},
	{"method": "GET", "path": "/v1/speakers/:slug", "query": []string{}},
	{"method": "GET", "path": "/v1/speakers/:year/:slug", "query": []string{}},
	{"method": "GET", "path": "/v1/sponsors", "query": []string{"year"}},
	{"method": "GET", "path": "/v1/sponsors/:slug", "query": []string{}},
	{"method": "GET", "path": "/v1/sponsors/:year/:slug", "query": []string{}},
}

var (
	sqlCount     atomic.Int64
	connectCount atomic.Int64
)

func resetCounts() {
	sqlCount.Store(0)
	connectCount.Store(0)
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func dbQuery(ctx context.Context, q querier, sql string, args ...any) (pgx.Rows, error) {
	sqlCount.Add(1)
	return q.Query(ctx, sql, args...)
}

func dbQueryRow(ctx context.Context, q querier, sql string, args ...any) pgx.Row {
	sqlCount.Add(1)
	return q.QueryRow(ctx, sql, args...)
}

func openPool(ctx context.Context, dbURL string) (*pgxpool.Pool, error) {
	connectCount.Add(1)
	return pgxpool.New(ctx, dbURL)
}

func listenAddr(port string) string {
	return "[::]:" + port
}

func main() {
	ctx := context.Background()
	dbURL := getenv("DATABASE_URL", "postgres://postgres:postgres@127.0.0.1:5432/carolina_dev")
	pool, err := openPool(ctx, dbURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	handler := newHandler(pool)

	port := getenv("PORT", "4002")
	go register(port)
	log.Printf("carolina-codes-go listening on :%s", port)
	log.Fatal(http.ListenAndServe(listenAddr(port), handler))
}

func newHandler(pool querier) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]any{
			"language":         language,
			"language_version": runtime.Version(),
			"api_version":      apiVersion,
			"framework":        framework,
			"created_year":     createdYear,
			"schema_version":   schemaVersion,
			"endpoints":        endpoints,
		})
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /v1/years", func(w http.ResponseWriter, r *http.Request) {
		rows, err := dbQuery(r.Context(), pool, "SELECT year, slug, name, status FROM v1_years ORDER BY year DESC")
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		var out []map[string]any
		for rows.Next() {
			var year int
			var slug, name, status string
			if err := rows.Scan(&year, &slug, &name, &status); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			out = append(out, map[string]any{"year": year, "slug": slug, "name": name, "status": status})
		}
		writeJSON(w, map[string]any{"data": out})
	})
	mux.HandleFunc("GET /v1/speakers", func(w http.ResponseWriter, r *http.Request) {
		var year *int
		if raw := r.URL.Query().Get("year"); raw != "" {
			n, _ := strconv.Atoi(raw)
			year = &n
		}
		out, err := listSpeakers(r.Context(), pool, year)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, map[string]any{"data": out})
	})
	mux.HandleFunc("GET /v1/speakers/{year}/{slug}", func(w http.ResponseWriter, r *http.Request) {
		year, _ := strconv.Atoi(r.PathValue("year"))
		slug := r.PathValue("slug")
		speaker, err := loadSpeaker(r.Context(), pool, slug)
		if err != nil {
			http.Error(w, `{"error":"not_found"}`, 404)
			return
		}
		talks := loadTalks(r.Context(), pool, slug, &year)
		if len(talks) == 0 {
			http.Error(w, `{"error":"not_found"}`, 404)
			return
		}
		years := talkYears(r.Context(), pool, slug)
		speaker["year"] = year
		speaker["years"] = years
		speaker["other_years"] = exceptYear(years, year)
		speaker["talks"] = talks
		speaker["languages"] = uniqTalkField(talks, "languages")
		speaker["topics"] = uniqTalkField(talks, "topics")
		writeJSON(w, map[string]any{"data": speaker})
	})
	mux.HandleFunc("GET /v1/speakers/{slug}", func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		speaker, err := loadSpeaker(r.Context(), pool, slug)
		if err != nil {
			http.Error(w, `{"error":"not_found"}`, 404)
			return
		}
		talks := loadTalks(r.Context(), pool, slug, nil)
		speaker["talks"] = talks
		speaker["years"] = talkYears(r.Context(), pool, slug)
		writeJSON(w, map[string]any{"data": speaker})
	})
	mux.HandleFunc("GET /v1/sponsors", func(w http.ResponseWriter, r *http.Request) {
		if year := r.URL.Query().Get("year"); year != "" {
			n, _ := strconv.Atoi(year)
			rows, err := dbQuery(r.Context(), pool,
				"SELECT "+yearSponsorColumns+" FROM v1_year_sponsors WHERE year = $1 ORDER BY name", n)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			defer rows.Close()
			writeJSON(w, map[string]any{"data": yearSponsorsFromRows(rows)})
			return
		}
		rows, err := dbQuery(r.Context(), pool, "SELECT "+sponsorColumns+" FROM v1_sponsors ORDER BY name")
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		writeJSON(w, map[string]any{"data": sponsorsFromRows(rows)})
	})
	mux.HandleFunc("GET /v1/sponsors/{year}/{slug}", func(w http.ResponseWriter, r *http.Request) {
		year, _ := strconv.Atoi(r.PathValue("year"))
		slug := r.PathValue("slug")
		sponsor, err := loadYearSponsor(r.Context(), pool, year, slug)
		if err != nil {
			http.Error(w, `{"error":"not_found"}`, 404)
			return
		}
		years := sponsorYears(r.Context(), pool, slug)
		sponsor["years"] = years
		sponsor["other_years"] = exceptYear(years, year)
		writeJSON(w, map[string]any{"data": sponsor})
	})
	mux.HandleFunc("GET /v1/sponsors/{slug}", func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		sponsor, err := loadSponsor(r.Context(), pool, slug)
		if err != nil {
			http.Error(w, `{"error":"not_found"}`, 404)
			return
		}
		writeJSON(w, map[string]any{"data": sponsor})
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Polyglot-Language", language)
		w.Header().Set("X-Polyglot-Framework", framework)
		mux.ServeHTTP(w, r)
	})
}

type scanner interface {
	Scan(dest ...any) error
}

type rowIter interface {
	Next() bool
	Scan(dest ...any) error
}

const speakerColumns = "slug, first_name, last_name, name, tagline, bio, company, location, photo_path, twitter_url, linkedin_url, website_url, github_url, featured"

func scanSpeaker(row scanner) (map[string]any, error) {
	var slug, first, last, name string
	var tagline, bio, company, location, photo, twitter, linkedin, website, github *string
	var featured bool
	if err := row.Scan(&slug, &first, &last, &name, &tagline, &bio, &company, &location, &photo, &twitter, &linkedin, &website, &github, &featured); err != nil {
		return nil, err
	}
	return map[string]any{
		"slug": slug, "first_name": first, "last_name": last, "name": name,
		"tagline": tagline, "bio": bio, "company": company, "location": location,
		"photo_path": photo, "twitter_url": twitter, "linkedin_url": linkedin,
		"website_url": website, "github_url": github, "featured": featured,
	}, nil
}

func speakersFromRows(rows rowIter) []map[string]any {
	var out []map[string]any
	for rows.Next() {
		if s, err := scanSpeaker(rows); err == nil {
			out = append(out, s)
		}
	}
	if out == nil {
		out = []map[string]any{}
	}
	return out
}

func loadSpeaker(ctx context.Context, q querier, slug string) (map[string]any, error) {
	row := dbQueryRow(ctx, q,
		"SELECT "+speakerColumns+" FROM v1_speakers WHERE slug = $1",
		slug)
	return scanSpeaker(row)
}

func loadTalks(ctx context.Context, q querier, slug string, year *int) []map[string]any {
	query := "SELECT slug, title, description, format, youtube_id, year, languages, topics FROM v1_talks WHERE speaker_slug = $1"
	args := []any{slug}
	if year != nil {
		query += " AND year = $2"
		args = append(args, *year)
	}
	trows, err := dbQuery(ctx, q, query, args...)
	if err != nil {
		return []map[string]any{}
	}
	defer trows.Close()
	talks := []map[string]any{}
	for trows.Next() {
		var tslug, title string
		var desc, format, yt *string
		var yr int
		var langs, topics []string
		if err := trows.Scan(&tslug, &title, &desc, &format, &yt, &yr, &langs, &topics); err == nil {
			if langs == nil {
				langs = []string{}
			}
			if topics == nil {
				topics = []string{}
			}
			talks = append(talks, map[string]any{
				"slug": tslug, "title": title, "description": desc,
				"format": format, "youtube_id": yt, "year": yr,
				"languages": langs, "topics": topics,
			})
		}
	}
	return talks
}

func listSpeakers(ctx context.Context, q querier, year *int) ([]map[string]any, error) {
	query := "SELECT " + speakerColumns + " FROM v1_speakers"
	args := []any{}
	if year != nil {
		query += " WHERE slug IN (SELECT speaker_slug FROM v1_talks WHERE year = $1)"
		args = append(args, *year)
	}
	query += " ORDER BY last_name, first_name"
	rows, err := dbQuery(ctx, q, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := speakersFromRows(rows)
	if year != nil {
		out = attachYearTags(ctx, q, out, *year)
	}
	return out, nil
}

func attachYearTags(ctx context.Context, q querier, speakers []map[string]any, year int) []map[string]any {
	if len(speakers) == 0 {
		return speakers
	}
	slugs := make([]string, 0, len(speakers))
	for _, speaker := range speakers {
		slug, _ := speaker["slug"].(string)
		slugs = append(slugs, slug)
	}
	talksBy := loadTalksForYear(ctx, q, year)
	yearsBy := loadYearsForSlugs(ctx, q, slugs)
	for _, speaker := range speakers {
		slug, _ := speaker["slug"].(string)
		talks := talksBy[slug]
		if talks == nil {
			talks = []map[string]any{}
		}
		years := yearsBy[slug]
		if years == nil {
			years = []int{}
		}
		speaker["year"] = year
		speaker["talks"] = talks
		speaker["languages"] = uniqTalkField(talks, "languages")
		speaker["topics"] = uniqTalkField(talks, "topics")
		speaker["years"] = years
	}
	return speakers
}

func loadTalksForYear(ctx context.Context, q querier, year int) map[string][]map[string]any {
	rows, err := dbQuery(ctx, q,
		"SELECT slug, title, description, format, youtube_id, year, speaker_slug, languages, topics FROM v1_talks WHERE year = $1 ORDER BY speaker_slug, year DESC",
		year)
	out := map[string][]map[string]any{}
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var tslug, title, speakerSlug string
		var desc, format, yt *string
		var yr int
		var langs, topics []string
		if err := rows.Scan(&tslug, &title, &desc, &format, &yt, &yr, &speakerSlug, &langs, &topics); err != nil {
			continue
		}
		if langs == nil {
			langs = []string{}
		}
		if topics == nil {
			topics = []string{}
		}
		out[speakerSlug] = append(out[speakerSlug], map[string]any{
			"slug": tslug, "title": title, "description": desc,
			"format": format, "youtube_id": yt, "year": yr,
			"speaker_slug": speakerSlug, "languages": langs, "topics": topics,
		})
	}
	return out
}

func loadYearsForSlugs(ctx context.Context, q querier, slugs []string) map[string][]int {
	out := map[string][]int{}
	if len(slugs) == 0 {
		return out
	}
	rows, err := dbQuery(ctx, q,
		"SELECT DISTINCT speaker_slug, year FROM v1_talks WHERE speaker_slug = ANY($1::text[]) ORDER BY speaker_slug, year DESC",
		slugs)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var slug string
		var year int
		if err := rows.Scan(&slug, &year); err == nil {
			out[slug] = append(out[slug], year)
		}
	}
	return out
}

func uniqTalkField(talks []map[string]any, key string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, talk := range talks {
		vals, _ := talk[key].([]string)
		for _, val := range vals {
			if val == "" {
				continue
			}
			if _, ok := seen[val]; ok {
				continue
			}
			seen[val] = struct{}{}
			out = append(out, val)
		}
	}
	if out == nil {
		out = []string{}
	}
	return out
}

func talkYears(ctx context.Context, q querier, slug string) []int {
	rows, err := dbQuery(ctx, q, "SELECT DISTINCT year FROM v1_talks WHERE speaker_slug = $1 ORDER BY year DESC", slug)
	if err != nil {
		return []int{}
	}
	defer rows.Close()
	var years []int
	for rows.Next() {
		var y int
		if err := rows.Scan(&y); err == nil {
			years = append(years, y)
		}
	}
	if years == nil {
		years = []int{}
	}
	return years
}

func sponsorYears(ctx context.Context, q querier, slug string) []int {
	rows, err := dbQuery(ctx, q, "SELECT DISTINCT year FROM v1_sponsorships WHERE sponsor_slug = $1 ORDER BY year DESC", slug)
	if err != nil {
		return []int{}
	}
	defer rows.Close()
	var years []int
	for rows.Next() {
		var y int
		if err := rows.Scan(&y); err == nil {
			years = append(years, y)
		}
	}
	if years == nil {
		years = []int{}
	}
	return years
}

func exceptYear(years []int, year int) []int {
	out := []int{}
	for _, y := range years {
		if y != year {
			out = append(out, y)
		}
	}
	return out
}

const yearSponsorColumns = "slug, name, website, logo_path, description, blurb, tier, featured, year, twitter_url, linkedin_url, youtube_url, instagram_url, facebook_url"
const sponsorColumns = "slug, name, website, logo_path, description, twitter_url, linkedin_url, youtube_url, instagram_url, facebook_url"

func scanYearSponsor(row scanner) (map[string]any, error) {
	var slug, name string
	var website, logo, desc, blurb, tier, twitter, linkedin, youtube, instagram, facebook *string
	var featured bool
	var year int
	if err := row.Scan(&slug, &name, &website, &logo, &desc, &blurb, &tier, &featured, &year, &twitter, &linkedin, &youtube, &instagram, &facebook); err != nil {
		return nil, err
	}
	return map[string]any{
		"slug": slug, "name": name, "website": website, "logo_path": logo,
		"description": desc, "blurb": blurb, "tier": tier, "featured": featured, "year": year,
		"twitter_url": twitter, "linkedin_url": linkedin, "youtube_url": youtube,
		"instagram_url": instagram, "facebook_url": facebook,
	}, nil
}

func scanSponsor(row scanner) (map[string]any, error) {
	var slug, name string
	var website, logo, desc, twitter, linkedin, youtube, instagram, facebook *string
	if err := row.Scan(&slug, &name, &website, &logo, &desc, &twitter, &linkedin, &youtube, &instagram, &facebook); err != nil {
		return nil, err
	}
	return map[string]any{
		"slug": slug, "name": name, "website": website, "logo_path": logo, "description": desc,
		"twitter_url": twitter, "linkedin_url": linkedin, "youtube_url": youtube,
		"instagram_url": instagram, "facebook_url": facebook,
	}, nil
}

func loadYearSponsor(ctx context.Context, q querier, year int, slug string) (map[string]any, error) {
	row := dbQueryRow(ctx, q, "SELECT "+yearSponsorColumns+" FROM v1_year_sponsors WHERE year = $1 AND slug = $2", year, slug)
	return scanYearSponsor(row)
}

func loadSponsor(ctx context.Context, q querier, slug string) (map[string]any, error) {
	row := dbQueryRow(ctx, q, "SELECT "+sponsorColumns+" FROM v1_sponsors WHERE slug = $1", slug)
	return scanSponsor(row)
}

func yearSponsorsFromRows(rows rowIter) []map[string]any {
	var out []map[string]any
	for rows.Next() {
		if s, err := scanYearSponsor(rows); err == nil {
			out = append(out, s)
		}
	}
	if out == nil {
		out = []map[string]any{}
	}
	return out
}

func sponsorsFromRows(rows rowIter) []map[string]any {
	var out []map[string]any
	for rows.Next() {
		if s, err := scanSponsor(rows); err == nil {
			out = append(out, s)
		}
	}
	if out == nil {
		out = []map[string]any{}
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func register(port string) {
	url := os.Getenv("CAROLINA_URL")
	token := os.Getenv("POLYGLOT_REGISTER_TOKEN")
	if url == "" || token == "" {
		return
	}
	base := getenv("PUBLIC_BASE_URL", "http://127.0.0.1:"+port)
	body, _ := json.Marshal(map[string]any{
		"language":         language,
		"language_version": runtime.Version(),
		"api_version":      apiVersion,
		"framework":        framework,
		"created_year":     createdYear,
		"schema_version":   schemaVersion,
		"base_url":         base,
		"endpoints":        endpoints,
	})
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(url, "/")+"/internal/api-endpoints/register", strings.NewReader(string(body)))
	if err != nil {
		log.Printf("register: %v", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("register: %v", err)
		return
	}
	defer resp.Body.Close()
	log.Printf("registered with elixir: %s", resp.Status)
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
