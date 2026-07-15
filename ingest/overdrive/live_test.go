package overdrive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/freeeve/libcat/ingest"
)

// thunderStub serves a fixed number of paged /media responses: each page holds
// one item and points Next at the following page until the last, mirroring the
// thunder pagination the crawl walks.
func thunderStub(t *testing.T, library string, pages int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/libraries/" + library + "/media"
		if r.URL.Path != wantPath {
			t.Errorf("path = %q, want %q", r.URL.Path, wantPath)
		}
		if r.URL.Query().Get("perPage") == "" {
			t.Error("perPage query missing")
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}
		var body mediaPage
		body.TotalItems = pages
		body.Items = []Item{{ID: fmt.Sprintf("id-%d", page), Title: fmt.Sprintf("T%d", page), OwnedCopies: page}}
		body.Links.Last.Page = pages
		if page < pages {
			body.Links.Next = &struct {
				Page int `json:"page"`
			}{Page: page + 1}
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
}

// TestLiveFetcherCrawlsAllPages checks the pager walks every page until Next is
// nil and returns all items in order.
func TestLiveFetcherCrawlsAllPages(t *testing.T) {
	srv := thunderStub(t, "qll", 3)
	defer srv.Close()
	lf := liveFetcher{baseURL: srv.URL, library: "qll", perPage: 50, client: srv.Client()}
	items, err := lf.items(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3 (one per page)", len(items))
	}
	if items[0].ID != "id-1" || items[2].ID != "id-3" {
		t.Errorf("items out of order: %+v", items)
	}
}

// TestLiveFetcherWritesCache checks the fetched pages are mirrored into the
// page-*.json layout ReadCache consumes, so a live run can seed the cache.
func TestLiveFetcherWritesCache(t *testing.T) {
	srv := thunderStub(t, "qll", 2)
	defer srv.Close()
	dir := t.TempDir()
	lf := liveFetcher{baseURL: srv.URL, library: "qll", perPage: 50, writeDir: dir, client: srv.Client()}
	if _, err := lf.items(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"page-00001.json", "page-00002.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected mirrored %s: %v", name, err)
		}
	}
	// The mirror round-trips: ReadCache reads the same items back.
	items, err := ReadCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Errorf("ReadCache of the mirror = %d items, want 2", len(items))
	}
}

// TestNewSelectsSourceMode checks the factory routes to cache vs live vs error.
func TestNewSelectsSourceMode(t *testing.T) {
	if _, err := New(ingest.Config{}); err == nil {
		t.Error("neither cache nor library should error")
	}
	dir := t.TempDir()
	if _, err := New(ingest.Config{Source: dir}); err != nil {
		t.Errorf("cache mode should build: %v", err)
	}
	if _, err := New(ingest.Config{Params: map[string]string{"library": "qll"}}); err != nil {
		t.Errorf("live mode should build: %v", err)
	}
}

// TestLiveFetcherThroughProvider checks live mode works end to end via the
// provider factory (Params), honouring the owned-only filter over the feed.
func TestLiveFetcherThroughProvider(t *testing.T) {
	srv := thunderStub(t, "qll", 3)
	defer srv.Close()
	prov, err := New(ingest.Config{Params: map[string]string{
		"library": "qll", "baseURL": srv.URL, "rateMs": "0",
	}})
	if err != nil {
		t.Fatal(err)
	}
	recs, err := prov.Records(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("live records = %d, want 3", len(recs))
	}
}
