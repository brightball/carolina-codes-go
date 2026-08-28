package main

import (
	"context"
	"os"
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
