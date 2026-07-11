package overdrive

import (
	"context"
	"fmt"

	"github.com/freeeve/libcat/ingest"
)

// ProviderName is the OverDrive provider's registry key and default provenance
// feed graph (feed:overdrive).
const ProviderName = "overdrive"

// Provider is the OverDrive reference ingest provider (ARCHITECTURE §9): it reads a
// cached Thunder scan and yields its items as resolvable records for the shared
// ingest.Run pipeline. It holds only build-time config; the live availability half
// is a separate runtime adapter.
type Provider struct {
	feed  string
	cache string
}

// New is the ingest.Factory for OverDrive. It takes the scan cache directory from
// Config.Source and the provenance feed from Config.Feed (defaulting to
// feed:overdrive). It errors when no cache is configured.
func New(cfg ingest.Config) (ingest.Provider, error) {
	if cfg.Source == "" {
		return nil, fmt.Errorf("overdrive: cache directory (Config.Source) is required")
	}
	feed := cfg.Feed
	if feed == "" {
		feed = ProviderName
	}
	return Provider{feed: feed, cache: cfg.Source}, nil
}

// Name returns the provider's provenance feed graph name.
func (p Provider) Name() string { return p.feed }

// Role reports OverDrive as a bibliographic ingest source (feed:<name>).
func (p Provider) Role() ingest.Role { return ingest.RoleIngest }

// Records reads the OverDrive scan cache and returns its items as ingest records,
// in page order. Each Item already exposes Identity/Work/Instance, so it is an
// ingest.Record with no adaptation. ctx is accepted for the Provider contract; the
// cache read is local and does not observe cancellation.
func (p Provider) Records(_ context.Context) ([]ingest.Record, error) {
	items, err := ReadCache(p.cache)
	if err != nil {
		return nil, err
	}
	recs := make([]ingest.Record, len(items))
	for i, it := range items {
		// Thunder titles carry HTML character references and markup (e.g.
		// "LEGO&#174; Creations", "Qing&#8212;Min Ning"); normalize the transcribed
		// text at the source so both the BIBFRAME titles and the identity
		// clustering key see clean values. Subjects and creators are
		// headings and stay untouched.
		it.Title = ingest.CleanText(it.Title)
		it.Subtitle = ingest.CleanText(it.Subtitle)
		it.Series = ingest.CleanText(it.Series)
		recs[i] = it
	}
	return recs, nil
}
