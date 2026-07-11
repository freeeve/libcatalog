package hardcover

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"

	"github.com/freeeve/libcat/ingest"
)

// ProviderName is the Hardcover provider's registry key and default provenance feed
// graph (feed:hardcover).
const ProviderName = "hardcover"

// Provider is the Hardcover ingest provider. It either fetches a user's Read shelf
// live over the GraphQL API (with a bearer token) or replays a captured shelf JSON
// from Source -- the latter powers offline rebuilds and the golden test.
type Provider struct {
	feed   string
	source string // optional path to a captured user_books JSON array; empty = live fetch
	token  string
	limit  int
	client *http.Client
}

// New is the ingest.Factory for Hardcover. Config.Feed overrides the provenance feed
// (default feed:hardcover); Config.Source, when set, is a captured user_books JSON file
// replayed instead of calling the API; Config.Params["limit"] sets the page size. For a
// live fetch the API token comes from Params["token"] or $HARDCOVER_API_TOKEN /
// $HARDCOVER_TOKEN and is required -- it is never written to disk.
func New(cfg ingest.Config) (ingest.Provider, error) {
	feed := cfg.Feed
	if feed == "" {
		feed = ProviderName
	}
	limit := 100
	if v := cfg.Params["limit"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	p := Provider{feed: feed, source: cfg.Source, limit: limit, client: http.DefaultClient}
	if cfg.Source == "" {
		token := normalizeToken(firstNonEmpty(cfg.Params["token"], os.Getenv("HARDCOVER_API_TOKEN"), os.Getenv("HARDCOVER_TOKEN")))
		if token == "" {
			return nil, fmt.Errorf("hardcover: an API token (Config.Params[\"token\"] or $HARDCOVER_API_TOKEN) or a captured Config.Source file is required")
		}
		p.token = token
	}
	return p, nil
}

// Name returns the provider's provenance feed graph name.
func (p Provider) Name() string { return p.feed }

// Role reports Hardcover as a bibliographic ingest source (feed:<name>).
func (p Provider) Role() ingest.Role { return ingest.RoleIngest }

// Records returns the shelf as ingest records: each read book is exploded into one
// record per collapsed edition format, so the shared pipeline clusters a book's formats
// into a single Work (they share the author/title/language cluster key) with one
// Instance each (distinct per-format instance keys). A book with no derivable format
// yields a single formatless record. ctx cancels a live fetch.
func (p Provider) Records(ctx context.Context) ([]ingest.Record, error) {
	books, err := p.load(ctx)
	if err != nil {
		return nil, err
	}
	var recs []ingest.Record
	for _, ub := range books {
		if ub.Book.Title == "" {
			continue
		}
		formats := collapseFormats(ub.Book.Editions)
		if len(formats) == 0 {
			recs = append(recs, record{ub: ub})
			continue
		}
		for _, fi := range formats {
			recs = append(recs, record{ub: ub, fi: fi})
		}
	}
	return recs, nil
}

// load returns the shelf rows, from the captured Source file when set (offline/test)
// or a live GraphQL fetch otherwise.
func (p Provider) load(ctx context.Context) ([]userBook, error) {
	if p.source == "" {
		return p.fetchShelf(ctx)
	}
	data, err := os.ReadFile(p.source)
	if err != nil {
		return nil, fmt.Errorf("hardcover: read captured shelf %s: %w", p.source, err)
	}
	var books []userBook
	if err := json.Unmarshal(data, &books); err != nil {
		return nil, fmt.Errorf("hardcover: decode captured shelf %s: %w", p.source, err)
	}
	return books, nil
}

// firstNonEmpty returns the first non-empty string, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
