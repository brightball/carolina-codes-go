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
	apiVersion    = "0.1.0"
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
	{"method": "GET", "path": "/v1/sponsors", "query": []string{"year"}},
	{"method": "GET", "path": "/v1/sponsors/:slug", "query": []string{}},
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
		q := "SELECT slug, first_name, last_name, name, tagline, bio, company, location, photo_path, featured FROM v1_speakers"
		args := []any{}
		if year := r.URL.Query().Get("year"); year != "" {
			q += " WHERE slug IN (SELECT speaker_slug FROM v1_talks WHERE year = $1)"
			n, _ := strconv.Atoi(year)
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
		writeJSON(w, map[string]any{"data": out})
	})
	mux.HandleFunc("GET /v1/speakers/{slug}", func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		row := pool.QueryRow(r.Context(),
			"SELECT slug, first_name, last_name, name, tagline, bio, company, location, photo_path, featured FROM v1_speakers WHERE slug = $1",
			slug)
		speaker, err := scanSpeaker(row)
		if err != nil {
			http.Error(w, `{"error":"not_found"}`, 404)
			return
		}
		talks := []map[string]any{}
		trows, err := pool.Query(r.Context(),
			"SELECT slug, title, description, format, youtube_id, year FROM v1_talks WHERE speaker_slug = $1", slug)
		if err == nil {
			defer trows.Close()
			for trows.Next() {
				var tslug, title string
				var desc, format, yt *string
				var year int
				if err := trows.Scan(&tslug, &title, &desc, &format, &yt, &year); err == nil {
					talks = append(talks, map[string]any{
						"slug": tslug, "title": title, "description": desc,
						"format": format, "youtube_id": yt, "year": year,
					})
				}
			}
		}
		speaker["talks"] = talks
		writeJSON(w, map[string]any{"data": speaker})
	})
	mux.HandleFunc("GET /v1/sponsors", func(w http.ResponseWriter, r *http.Request) {
		q := "SELECT slug, name, website, logo_path, description FROM v1_sponsors"
		args := []any{}
		if year := r.URL.Query().Get("year"); year != "" {
			q += " WHERE slug IN (SELECT sponsor_slug FROM v1_sponsorships WHERE year = $1)"
			n, _ := strconv.Atoi(year)
			args = append(args, n)
		}
		q += " ORDER BY name"
		rows, err := pool.Query(r.Context(), q, args...)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		writeJSON(w, map[string]any{"data": sponsorsFromRows(rows)})
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

func scanSpeaker(row scanner) (map[string]any, error) {
	var slug, first, last, name string
	var tagline, bio, company, location, photo *string
	var featured bool
	if err := row.Scan(&slug, &first, &last, &name, &tagline, &bio, &company, &location, &photo, &featured); err != nil {
		return nil, err
	}
	return map[string]any{
		"slug": slug, "first_name": first, "last_name": last, "name": name,
		"tagline": tagline, "bio": bio, "company": company, "location": location,
		"photo_path": photo, "featured": featured,
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
