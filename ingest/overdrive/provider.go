package overdrive

import (
	"context"
	"fmt"
	"strconv"
	"time"

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
	feed      string
	cache     string
	live      *liveFetcher
	ownedOnly bool
}

// New is the ingest.Factory for OverDrive. The source is either a page cache
// (Config.Source, read offline) or the live thunder API (no Source, with
// Params["library"] the OverDrive library key -- optionally Params["baseURL"],
// Params["perPage"], Params["rateMs"], and Params["writeCache"] to mirror the
// fetched pages into a reusable cache). Params["ownedOnly"]="true" ingests only
// titles the library holds (see Records). Config.Feed sets the provenance feed
// (default feed:overdrive). It errors when neither a cache nor a library is set.
func New(cfg ingest.Config) (ingest.Provider, error) {
	feed := cfg.Feed
	if feed == "" {
		feed = ProviderName
	}
	p := Provider{feed: feed, ownedOnly: cfg.Params["ownedOnly"] == "true"}
	switch {
	case cfg.Source != "":
		p.cache = cfg.Source
	case cfg.Params["library"] != "":
		p.live = newLiveFetcher(cfg.Params)
	default:
		return nil, fmt.Errorf("overdrive: a page cache (Config.Source) or a live library key (Params[\"library\"]) is required")
	}
	return p, nil
}

// newLiveFetcher builds the live pager from the string params, applying
// defaults for the base URL, page size, and request rate.
func newLiveFetcher(params map[string]string) *liveFetcher {
	lf := &liveFetcher{
		baseURL:  DefaultBaseURL,
		library:  params["library"],
		perPage:  defaultPerPage,
		writeDir: params["writeCache"],
		rate:     defaultRate,
	}
	if v := params["baseURL"]; v != "" {
		lf.baseURL = v
	}
	if n, err := strconv.Atoi(params["perPage"]); err == nil && n > 0 {
		lf.perPage = n
	}
	if ms, err := strconv.Atoi(params["rateMs"]); err == nil && ms >= 0 {
		lf.rate = time.Duration(ms) * time.Millisecond
	}
	return lf
}

// Name returns the provider's provenance feed graph name.
func (p Provider) Name() string { return p.feed }

// Role reports OverDrive as a bibliographic ingest source (feed:<name>).
func (p Provider) Role() ingest.Role { return ingest.RoleIngest }

// Records yields the OverDrive items as ingest records, in page order, from the
// live API (observing ctx) or the local page cache. Each Item already exposes
// Identity/Work/Instance, so it is an ingest.Record with no adaptation.
func (p Provider) Records(ctx context.Context) ([]ingest.Record, error) {
	var items []Item
	var err error
	if p.live != nil {
		items, err = p.live.items(ctx)
	} else {
		items, err = ReadCache(p.cache)
	}
	if err != nil {
		return nil, err
	}
	recs := make([]ingest.Record, 0, len(items))
	for _, it := range items {
		// With ownedOnly, drop titles the library does not hold so the ingested
		// collection is exactly what the feed says it owns -- ownership derives
		// from the feed, not an external membership list.
		if p.ownedOnly && !it.owned() {
			continue
		}
		// Thunder titles carry HTML character references and markup (e.g.
		// "LEGO&#174; Creations", "Qing&#8212;Min Ning"); normalize the transcribed
		// text at the source so both the BIBFRAME titles and the identity
		// clustering key see clean values. Subjects and creators are
		// headings and stay untouched.
		it.Title = ingest.CleanText(it.Title)
		it.Subtitle = ingest.CleanText(it.Subtitle)
		it.Series = ingest.CleanText(it.Series)
		recs = append(recs, it)
	}
	return recs, nil
}
