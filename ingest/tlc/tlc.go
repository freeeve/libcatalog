// Package tlc harvests peer libraries' subject cataloging from TLC (The
// Library Corporation) LS2 PAC catalogs through the anonymous search
// endpoint: one POST per driver term with the term as BOTH the keyword and
// a Subject facet filter, so the keyword surfaces candidates and the facet
// enforces subject-cataloging precision. A tenant is a bare
// <tenant>.tlcdelivers.com subdomain or a full vanity catalog host (e.g.
// ls2pac.lapl.org) serving the same API.
//
// The subject index is unscoped (LCSH, Homosaurus and others merge, and a
// record carries no scheme tag), so like the BiblioCommons harvest this is
// the inference model -- the match is the exact Homosaurus prefLabel string
// -- not Vega's authoritative source tag. Every probed record carried at
// least one typed ISBN, which is the work-match key.
package tlc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/ingest"
)

// Name is the enrichment source name; suggestions carry PIPELINE provenance.
const Name = "tlc"

// confISBNMatch is the single confidence tier: an exact-prefLabel subject
// facet joined by a shared ISBN.
const confISBNMatch = 0.9

// Term is one driver term: the vocabulary URI the suggestion will carry and
// the label used as keyword + Subject facet.
type Term struct {
	URI    string
	Labels map[string]string
	Query  string
}

// Doer is the HTTP seam, injectable for tests.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Enricher harvests one or more LS2 PAC tenants.
type Enricher struct {
	client Doer
	// hosts are LS2 PAC tenants: a bare subdomain (e.g. "nbpl" ->
	// nbpl.tlcdelivers.com) or a full vanity host (e.g. "ls2pac.lapl.org").
	hosts []string
	terms []Term
	// hitsPerPage is the verified request-schema page size; maxPages caps
	// pages per (host, term).
	hitsPerPage int
	maxPages    int
	// delay is the politeness pause between requests to ONE host; distinct
	// hosts crawl concurrently, capped.
	delay           time.Duration
	hostConcurrency int
	log             *slog.Logger

	statsMu sync.Mutex
	stats   ingest.EnrichStats

	cache    *harvestCache
	cacheTTL time.Duration
}

// harvestCache memoizes per-host completed crawls, shared across per-run
// views.
type harvestCache struct {
	mu     sync.Mutex
	byHost map[string]*hostHarvest
}

type hostHarvest struct {
	mu    sync.Mutex
	items map[string][]record // term URI -> matched records
	at    time.Time
}

// record is one LS2 resource reduced to matching fields.
type record struct {
	id     int
	title  string
	author string
	isbns  []string
}

// Option configures the enricher.
type Option func(*Enricher)

// WithClient injects the HTTP client (tests).
func WithClient(d Doer) Option { return func(e *Enricher) { e.client = d } }

// WithDelay overrides the politeness pause.
func WithDelay(d time.Duration) Option { return func(e *Enricher) { e.delay = d } }

// WithMaxPages caps pages fetched per (host, term).
func WithMaxPages(n int) Option { return func(e *Enricher) { e.maxPages = n } }

// WithHostConcurrency overrides how many tenants crawl at once.
func WithHostConcurrency(n int) Option {
	return func(e *Enricher) {
		if n > 0 {
			e.hostConcurrency = n
		}
	}
}

// WithLogger wires progress logging.
func WithLogger(l *slog.Logger) Option { return func(e *Enricher) { e.log = l } }

// WithCacheTTL overrides how long a completed harvest is reused.
func WithCacheTTL(d time.Duration) Option { return func(e *Enricher) { e.cacheTTL = d } }

// New returns the harvester for the given tenant subdomains and terms.
func New(hosts []string, terms []Term, opts ...Option) *Enricher {
	e := &Enricher{
		client:          &http.Client{Timeout: ingest.DefaultRequestTimeout},
		hosts:           hosts,
		terms:           terms,
		hitsPerPage:     24,
		maxPages:        6,
		delay:           1500 * time.Millisecond,
		hostConcurrency: 4,
		cacheTTL:        24 * time.Hour,
		cache:           &harvestCache{byHost: map[string]*hostHarvest{}},
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// ForHosts returns a per-run view over a different tenant list (the
// per-job host override); the crawl cache is shared, the stats its own.
func (e *Enricher) ForHosts(hosts []string) ingest.Enricher {
	return &Enricher{
		client:          e.client,
		hosts:           append([]string(nil), hosts...),
		terms:           e.terms,
		hitsPerPage:     e.hitsPerPage,
		maxPages:        e.maxPages,
		delay:           e.delay,
		hostConcurrency: e.hostConcurrency,
		log:             e.log,
		cacheTTL:        e.cacheTTL,
		cache:           e.cache,
	}
}

// Name implements ingest.Enricher.
func (e *Enricher) Name() string { return Name }

// Describe names the peer catalogs this view pulls from.
func (e *Enricher) Describe() string { return strings.Join(e.hosts, ", ") }

// RunStats implements ingest.StatsReporter: Total is hosts x driver terms,
// Batches the (host, term) crawls completed (cache-warm counted instantly),
// SkippedBatches the per-host terms abandoned on an error, Candidates the
// live matches so far. Safe mid-run.
func (e *Enricher) RunStats() ingest.EnrichStats {
	e.statsMu.Lock()
	defer e.statsMu.Unlock()
	return e.stats
}

func (e *Enricher) bump(started time.Time, f func(*ingest.EnrichStats)) {
	e.statsMu.Lock()
	f(&e.stats)
	e.stats.ElapsedMS = time.Since(started).Milliseconds()
	e.statsMu.Unlock()
}

// Enrich implements ingest.Enricher: per tenant (concurrent, capped) run
// one faceted search per driver term (memoized within the TTL), match
// record ISBNs back to the scoped works, and emit one suggestion per
// (work, term) endorsed by every corroborating tenant.
func (e *Enricher) Enrich(ctx context.Context, works []ingest.WorkSummary) ([]ingest.Enrichment, error) {
	started := time.Now()
	valid := 0
	for _, t := range e.terms {
		if t.Query != "" && t.URI != "" {
			valid++
		}
	}
	e.bump(started, func(st *ingest.EnrichStats) { st.Total = valid * len(e.hosts) })

	byISBN := map[string][]int{}
	for i := range works {
		for _, isbn := range works[i].ISBNs {
			if n := normalizeISBN(isbn); n != "" {
				byISBN[n] = append(byISBN[n], i)
			}
		}
	}

	type matchKey struct {
		work int
		uri  string
	}
	type consensus struct {
		term  Term
		hosts map[string]ingest.Attribution
	}
	var aggMu sync.Mutex
	agg := map[matchKey]*consensus{}

	sem := make(chan struct{}, e.hostConcurrency)
	var wg sync.WaitGroup
	errs := make(chan error, len(e.hosts))
	for _, host := range e.hosts {
		wg.Add(1)
		go func(host string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			}
			harvest, err := e.ensureHostHarvest(ctx, host, started)
			if err != nil {
				errs <- err
				return
			}
			for uri, recs := range harvest {
				var term Term
				for _, t := range e.terms {
					if t.URI == uri {
						term = t
						break
					}
				}
				for _, rec := range recs {
					for _, isbn := range rec.isbns {
						for _, wi := range byISBN[isbn] {
							if hasSubject(&works[wi], uri) {
								continue
							}
							// No verified record-URL shape in the API (the
							// resource's link field is empty), so the
							// attribution carries the evidence without one.
							attr := ingest.Attribution{Source: host, Basis: "isbn", Key: isbn}
							aggMu.Lock()
							key := matchKey{work: wi, uri: uri}
							c := agg[key]
							if c == nil {
								c = &consensus{term: term, hosts: map[string]ingest.Attribution{}}
								agg[key] = c
								e.bump(started, func(st *ingest.EnrichStats) { st.Candidates++ })
							}
							if _, ok := c.hosts[host]; !ok {
								c.hosts[host] = attr
							}
							aggMu.Unlock()
						}
					}
				}
			}
		}(host)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return nil, err
		}
	}

	grouped := map[int][]*consensus{}
	for key, c := range agg {
		grouped[key.work] = append(grouped[key.work], c)
	}
	var out []ingest.Enrichment
	for i := range works {
		group := grouped[i]
		if len(group) == 0 {
			continue
		}
		sort.Slice(group, func(a, b int) bool { return group[a].term.URI < group[b].term.URI })
		enr := ingest.Enrichment{WorkID: works[i].WorkID, Confidence: confISBNMatch}
		for _, c := range group {
			enr.Subjects = append(enr.Subjects, bibframe.AuthoritySubject{URI: c.term.URI, Labels: c.term.Labels})
			keys := make([]string, 0, len(c.hosts))
			for k := range c.hosts {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			attrs := make([]ingest.Attribution, 0, len(keys))
			for _, k := range keys {
				attrs = append(attrs, c.hosts[k])
			}
			enr.Endorsements = append(enr.Endorsements, ingest.Endorsement{Count: len(keys), Sources: keys, Attributions: attrs})
		}
		out = append(out, enr)
	}
	return out, nil
}

// ensureHostHarvest returns one tenant's term->records harvest, memoized
// within the TTL; concurrent crawlers single-flight per host.
func (e *Enricher) ensureHostHarvest(ctx context.Context, host string, started time.Time) (map[string][]record, error) {
	e.cache.mu.Lock()
	hh := e.cache.byHost[host]
	if hh == nil {
		hh = &hostHarvest{}
		e.cache.byHost[host] = hh
	}
	e.cache.mu.Unlock()

	hh.mu.Lock()
	defer hh.mu.Unlock()
	valid := 0
	for _, t := range e.terms {
		if t.Query != "" && t.URI != "" {
			valid++
		}
	}
	if hh.items != nil && started.Sub(hh.at) < e.cacheTTL {
		e.bump(started, func(st *ingest.EnrichStats) { st.Batches += valid })
		return hh.items, nil
	}

	items := map[string][]record{}
	unreachable := 0
	for _, term := range e.terms {
		if term.Query == "" || term.URI == "" {
			continue
		}
		var recs []record
		failed := false
		for page := 0; page < e.maxPages; page++ {
			res, err := e.search(ctx, host, term.Query, page)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if ingest.IsUnreachable(err) {
					if unreachable++; unreachable >= ingest.UnreachableAbortAfter {
						return nil, fmt.Errorf("%w: %s", ingest.ErrPeerUnreachable, host)
					}
				} else {
					unreachable = 0
				}
				e.bump(started, func(st *ingest.EnrichStats) { st.SkippedBatches++ })
				if e.log != nil {
					e.log.Warn("tlc search page skipped", "host", host, "term", term.Query, "page", page, "err", err)
				}
				failed = true
				break
			}
			recs = append(recs, res.records...)
			if (page+1)*e.hitsPerPage >= res.totalHits {
				break
			}
			if page+1 == e.maxPages && e.log != nil {
				e.log.Info("tlc term truncated at page cap", "host", host, "term", term.Query, "pages", e.maxPages)
			}
		}
		if !failed {
			unreachable = 0
			// Union, not overwrite: several driver terms can share one URI
			// (the same concept searched in more than one language).
			items[term.URI] = append(items[term.URI], recs...)
		}
		e.bump(started, func(st *ingest.EnrichStats) { st.Batches++ })
	}
	hh.items = items
	hh.at = started
	return items, nil
}

// searchPage is one parsed search response.
type searchPage struct {
	totalHits int
	records   []record
}

// search POSTs one faceted subject search page. The body schema is STRICT:
// unknown fields make totalHits come back null, so this stays exactly the
// verified shape.
func (e *Enricher) search(ctx context.Context, host, label string, page int) (searchPage, error) {
	if e.delay > 0 {
		select {
		case <-ctx.Done():
			return searchPage{}, ctx.Err()
		case <-time.After(e.delay):
		}
	}
	reqBody := map[string]any{
		"searchTerm":   label,
		"hitsPerPage":  e.hitsPerPage,
		"startIndex":   page * e.hitsPerPage,
		"sortCriteria": "Relevancy",
		"facetFilters": []map[string]string{{
			"facetDisplay": label, "facetName": "Subject", "facetValue": label,
		}},
		"branchFilters":                  []string{},
		"audienceCharacteristicsFilters": []string{},
		"addToHistory":                   false,
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return searchPage{}, err
	}
	origin := hostOrigin(host)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/search", bytes.NewReader(payload))
	if err != nil {
		return searchPage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", origin+"/")
	req.Header.Set("Ls2pac-config-name", "pac")
	req.Header.Set("Ls2pac-config-type", "pac")
	req.Header.Set("User-Agent", "libcat-subject-harvest/1.0 (https://github.com/freeeve/libcat)")
	resp, err := e.client.Do(req)
	if err != nil {
		return searchPage{}, fmt.Errorf("%w: %w", ingest.ErrEnricher, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return searchPage{}, fmt.Errorf("%w: HTTP %d", ingest.ErrEnricher, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return searchPage{}, fmt.Errorf("%w: %w", ingest.ErrEnricher, err)
	}
	var res struct {
		TotalHits *int `json:"totalHits"`
		Resources []struct {
			ID              int    `json:"id"`
			ShortTitle      string `json:"shortTitle"`
			ShortAuthor     string `json:"shortAuthor"`
			StandardNumbers []struct {
				Type string `json:"type"`
				Data string `json:"data"`
			} `json:"standardNumbers"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return searchPage{}, fmt.Errorf("tlc: parse search: %w", err)
	}
	// A null totalHits means the strict request schema was violated -- a
	// contract drift worth failing loudly on, not an empty result.
	if res.TotalHits == nil {
		return searchPage{}, fmt.Errorf("%w: totalHits null (request schema rejected)", ingest.ErrEnricher)
	}
	out := searchPage{totalHits: *res.TotalHits}
	for _, r := range res.Resources {
		rec := record{id: r.ID, title: r.ShortTitle, author: r.ShortAuthor}
		for _, sn := range r.StandardNumbers {
			if strings.EqualFold(sn.Type, "Isbn") {
				if n := normalizeISBN(sn.Data); n != "" {
					rec.isbns = append(rec.isbns, n)
				}
			}
		}
		out.records = append(out.records, rec)
	}
	return out, nil
}

// hostOrigin resolves a config host token to its https origin. A bare
// subdomain (no dot) expands to <host>.tlcdelivers.com; a token with a dot
// is a vanity catalog host (e.g. ls2pac.lapl.org) serving the same LS2 PAC
// API and is used verbatim.
func hostOrigin(host string) string {
	if strings.Contains(host, ".") {
		return "https://" + host
	}
	return "https://" + host + ".tlcdelivers.com"
}

// hasSubject reports whether the work already carries the term URI.
func hasSubject(w *ingest.WorkSummary, uri string) bool {
	for _, s := range w.Subjects {
		if s == uri {
			return true
		}
	}
	return false
}

// normalizeISBN strips spaces and hyphens.
func normalizeISBN(v string) string {
	return strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(v), "-", ""), " ", "")
}
