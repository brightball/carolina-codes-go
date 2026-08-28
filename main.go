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
	"time"

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

func main() {
	ctx := context.Background()
	dbURL := getenv("DATABASE_URL", "postgres://postgres:postgres@127.0.0.1:5432/carolina_dev")
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]any{
			"language":          language,
			"language_version":  runtime.Version(),
			"api_version":       apiVersion,
			"framework":         framework,
			"created_year":      createdYear,
			"schema_version":    schemaVersion,
			"endpoints":         endpoints,
		})
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /v1/years", func(w http.ResponseWriter, r *http.Request) {
		rows, err := pool.Query(r.Context(), "SELECT year, slug, name, status FROM v1_years ORDER BY year DESC")
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
		q := "SELECT " + speakerColumns + " FROM v1_speakers"
		args := []any{}
		var year *int
		if raw := r.URL.Query().Get("year"); raw != "" {
			q += " WHERE slug IN (SELECT speaker_slug FROM v1_talks WHERE year = $1)"
			n, _ := strconv.Atoi(raw)
			year = &n
			args = append(args, n)
		}
		q += " ORDER BY last_name, first_name"
		rows, err := pool.Query(r.Context(), q, args...)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		out := speakersFromRows(rows)
		if year != nil {
			out = attachYearTags(r.Context(), pool, out, *year)
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
			rows, err := pool.Query(r.Context(),
				"SELECT slug, name, website, logo_path, description, blurb, tier, featured, year FROM v1_year_sponsors WHERE year = $1 ORDER BY name", n)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			defer rows.Close()
			writeJSON(w, map[string]any{"data": yearSponsorsFromRows(rows)})
			return
		}
		q := "SELECT slug, name, website, logo_path, description FROM v1_sponsors ORDER BY name"
		rows, err := pool.Query(r.Context(), q)
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
		var name string
		var website, logo, desc, blurb, tier *string
		var featured bool
		err := pool.QueryRow(r.Context(),
			"SELECT slug, name, website, logo_path, description, blurb, tier, featured, year FROM v1_year_sponsors WHERE year = $1 AND slug = $2",
			year, slug).
			Scan(&slug, &name, &website, &logo, &desc, &blurb, &tier, &featured, &year)
		if err != nil {
			http.Error(w, `{"error":"not_found"}`, 404)
			return
		}
		years := sponsorYears(r.Context(), pool, slug)
		writeJSON(w, map[string]any{"data": map[string]any{
			"slug": slug, "name": name, "website": website, "logo_path": logo,
			"description": desc, "blurb": blurb, "tier": tier, "featured": featured,
			"year": year, "years": years, "other_years": exceptYear(years, year),
		}})
	})
	mux.HandleFunc("GET /v1/sponsors/{slug}", func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		var name string
		var website, logo, desc *string
		err := pool.QueryRow(r.Context(),
			"SELECT slug, name, website, logo_path, description FROM v1_sponsors WHERE slug = $1", slug).
			Scan(&slug, &name, &website, &logo, &desc)
		if err != nil {
			http.Error(w, `{"error":"not_found"}`, 404)
			return
		}
		writeJSON(w, map[string]any{"data": map[string]any{
			"slug": slug, "name": name, "website": website, "logo_path": logo, "description": desc,
		}})
	})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Polyglot-Language", language)
		w.Header().Set("X-Polyglot-Framework", framework)
		mux.ServeHTTP(w, r)
	})

	port := getenv("PORT", "4002")
	go register(port)
	log.Printf("carolina-codes-go listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, handler))
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

func loadSpeaker(ctx context.Context, pool *pgxpool.Pool, slug string) (map[string]any, error) {
	row := pool.QueryRow(ctx,
		"SELECT "+speakerColumns+" FROM v1_speakers WHERE slug = $1",
		slug)
	return scanSpeaker(row)
}

func loadTalks(ctx context.Context, pool *pgxpool.Pool, slug string, year *int) []map[string]any {
	q := "SELECT slug, title, description, format, youtube_id, year, languages, topics FROM v1_talks WHERE speaker_slug = $1"
	args := []any{slug}
	if year != nil {
		q += " AND year = $2"
		args = append(args, *year)
	}
	trows, err := pool.Query(ctx, q, args...)
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

func attachYearTags(ctx context.Context, pool *pgxpool.Pool, speakers []map[string]any, year int) []map[string]any {
	for _, speaker := range speakers {
		slug, _ := speaker["slug"].(string)
		talks := loadTalks(ctx, pool, slug, &year)
		speaker["year"] = year
		speaker["talks"] = talks
		speaker["languages"] = uniqTalkField(talks, "languages")
		speaker["topics"] = uniqTalkField(talks, "topics")
		speaker["years"] = talkYears(ctx, pool, slug)
	}
	return speakers
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

func talkYears(ctx context.Context, pool *pgxpool.Pool, slug string) []int {
	rows, err := pool.Query(ctx, "SELECT DISTINCT year FROM v1_talks WHERE speaker_slug = $1 ORDER BY year DESC", slug)
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

func sponsorYears(ctx context.Context, pool *pgxpool.Pool, slug string) []int {
	rows, err := pool.Query(ctx, "SELECT DISTINCT year FROM v1_sponsorships WHERE sponsor_slug = $1 ORDER BY year DESC", slug)
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

func yearSponsorsFromRows(rows rowIter) []map[string]any {
	var out []map[string]any
	for rows.Next() {
		var slug, name string
		var website, logo, desc, blurb, tier *string
		var featured bool
		var year int
		if err := rows.Scan(&slug, &name, &website, &logo, &desc, &blurb, &tier, &featured, &year); err == nil {
			out = append(out, map[string]any{
				"slug": slug, "name": name, "website": website, "logo_path": logo,
				"description": desc, "blurb": blurb, "tier": tier, "featured": featured, "year": year,
			})
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
		var slug, name string
		var website, logo, desc *string
		if err := rows.Scan(&slug, &name, &website, &logo, &desc); err == nil {
			out = append(out, map[string]any{
				"slug": slug, "name": name, "website": website, "logo_path": logo, "description": desc,
			})
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
		"language":          language,
		"language_version":  runtime.Version(),
		"api_version":       apiVersion,
		"framework":         framework,
		"created_year":      createdYear,
		"schema_version":    schemaVersion,
		"base_url":          base,
		"endpoints":         endpoints,
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
