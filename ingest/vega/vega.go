// Package vega harvests peer libraries' subject cataloging from III Vega
// Discover catalogs (NYPL and other Innovative sites) through the anonymous
// JSON API at <region>.iiivega.com. Unlike BiblioCommons' reverse RSS
// inference, Vega states its vocabulary outright: a concept record carries
// source="homoit", so a match is a peer's explicit Homosaurus assertion.
//
// The chain per driver term: suggestions?phrase=<label> yields concept
// candidates; the concept endpoint gates source=="homoit" and an exact
// label match; the resources endpoint pages the FormatGroups cataloged
// under the concept, each carrying typed identifiers (ISBN on ~89% of
// records). Matching back to our works is by ISBN; the driver term is the
// suggestion.
//
// Concept UUIDs are shared REGION-wide (every tenant in na returns the
// same UUID for one label), so label->concept resolution runs once per
// region and every tenant there reuses it. All of a region's traffic hits
// one API host, so politeness is per REGION: requests within a region
// serialize with the delay, distinct regions run concurrently.
package vega

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/ingest"
)

// Name is the enrichment source name; suggestions carry PIPELINE provenance.
const Name = "vega"

// Suggestion confidence: every match is an explicit peer Homosaurus
// assertion joined by a shared ISBN -- the strong tier.
const confISBNMatch = 0.9

// Term is one driver term: the vocabulary URI the suggestion will carry and
// the label to resolve against the peer's concepts.
type Term struct {
	URI    string
	Labels map[string]string
	// Query is the resolution label (the English prefLabel).
	Query string
}

// Tenant is one Vega library: the catalog subdomain and its region. The
// catalog lives at https://<SiteCode>.<Region>.iiivega.com/ (some sites
// front it with a custom domain, but the Vega-domain form answers for all).
type Tenant struct {
	SiteCode string
	Region   string // "na", "na2", ...
}

// Key is the tenant's display / attribution identity ("nypl.na2").
func (t Tenant) Key() string { return t.SiteCode + "." + t.Region }

func (t Tenant) customerDomain() string { return t.SiteCode + "." + t.Region + ".iiivega.com" }

// ParseTenants reads the config form: comma-separated <siteCode>.<region>
// entries ("nypl.na2,mdpls.na"). The siteCode must be the library's real
// catalog subdomain -- DNS is wildcard, so nothing validates a guess until
// the API answers 403 "Customer with siteCode not found".
func ParseTenants(spec string) ([]Tenant, error) {
	var out []Tenant
	for _, e := range strings.Split(spec, ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		parts := strings.Split(e, ".")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("vega: tenant %q wants <siteCode>.<region> (e.g. nypl.na2)", e)
		}
		out = append(out, Tenant{SiteCode: parts[0], Region: parts[1]})
	}
	return out, nil
}

// Doer is the HTTP seam, injectable for tests.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Enricher harvests one or more Vega tenants.
type Enricher struct {
	client  Doer
	tenants []Tenant
	terms   []Term
	// pageSize is FormatGroups per resources page; maxPages caps pages per
	// (tenant, concept) -- big concepts run to hundreds.
	pageSize int
	maxPages int
	// delay is the politeness pause between requests to ONE region host.
	delay time.Duration
	log   *slog.Logger
	// anonID satisfies the API's Anonymous-User-Id header; any UUID-shaped
	// value works and one per enricher is the polite shape.
	anonID string

	statsMu sync.Mutex
	stats   ingest.EnrichStats

	cache    *harvestCache
	cacheTTL time.Duration
}

// regionConcurrency caps how many REGIONS harvest at once; within a region
// every request serializes with the politeness delay (one API host).
const regionConcurrency = 4

// harvestCache memoizes per-region concept resolutions and per-tenant work
// harvests, shared across per-run views.
type harvestCache struct {
	mu sync.Mutex
	// concepts: region+"\x00"+termURI -> resolved concept id ("" = resolved,
	// no homoit concept) with its resolution time.
	concepts map[string]conceptEntry
	// byTenant: tenant key -> completed harvest.
	byTenant map[string]*tenantHarvest
}

type conceptEntry struct {
	id string
	at time.Time
}

type tenantHarvest struct {
	mu    sync.Mutex
	items map[string][]fgItem // term URI -> format groups
	at    time.Time
}

// fgItem is one FormatGroup reduced to matching fields.
type fgItem struct {
	id    string
	title string
	isbns []string
}

// Option configures the enricher.
type Option func(*Enricher)

// WithClient injects the HTTP client (tests).
func WithClient(d Doer) Option { return func(e *Enricher) { e.client = d } }

// WithDelay overrides the politeness pause.
func WithDelay(d time.Duration) Option { return func(e *Enricher) { e.delay = d } }

// WithMaxPages caps resources pages fetched per (tenant, concept).
func WithMaxPages(n int) Option { return func(e *Enricher) { e.maxPages = n } }

// WithLogger wires progress logging.
func WithLogger(l *slog.Logger) Option { return func(e *Enricher) { e.log = l } }

// WithCacheTTL overrides how long resolutions and harvests are reused.
func WithCacheTTL(d time.Duration) Option { return func(e *Enricher) { e.cacheTTL = d } }

// New returns the harvester for the given tenants and driver term list.
func New(tenants []Tenant, terms []Term, opts ...Option) *Enricher {
	e := &Enricher{
		client:   &http.Client{Timeout: ingest.DefaultRequestTimeout},
		tenants:  tenants,
		terms:    terms,
		pageSize: 96,
		maxPages: 6,
		delay:    1500 * time.Millisecond,
		cacheTTL: 24 * time.Hour,
		anonID:   anonUUID(),
		cache:    &harvestCache{concepts: map[string]conceptEntry{}, byTenant: map[string]*tenantHarvest{}},
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Name implements ingest.Enricher.
func (e *Enricher) Name() string { return Name }

// Describe names the peer catalogs this enricher pulls from.
func (e *Enricher) Describe() string {
	keys := make([]string, 0, len(e.tenants))
	for _, t := range e.tenants {
		keys = append(keys, t.Key())
	}
	return strings.Join(keys, ", ")
}

// RunStats implements ingest.StatsReporter: Total is tenants x driver
// terms, Batches the (tenant, term) harvests completed (cache-warm counted
// instantly), SkippedBatches the per-tenant terms abandoned on an error,
// Candidates the live matches so far. Safe mid-run.
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

// Enrich implements ingest.Enricher: per region, resolve each driver term
// to its shared concept once; per tenant, page the concept's FormatGroups
// (memoized within the TTL); match ISBNs back to the scoped works; emit one
// suggestion per (work, term) endorsed by every corroborating tenant.
func (e *Enricher) Enrich(ctx context.Context, works []ingest.WorkSummary) ([]ingest.Enrichment, error) {
	started := time.Now()
	valid := 0
	for _, t := range e.terms {
		if t.Query != "" && t.URI != "" {
			valid++
		}
	}
	e.bump(started, func(st *ingest.EnrichStats) { st.Total = valid * len(e.tenants) })

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
		term    Term
		tenants map[string]ingest.Attribution
	}
	var aggMu sync.Mutex
	agg := map[matchKey]*consensus{}

	byRegion := map[string][]Tenant{}
	for _, t := range e.tenants {
		byRegion[t.Region] = append(byRegion[t.Region], t)
	}
	sem := make(chan struct{}, regionConcurrency)
	var wg sync.WaitGroup
	errs := make(chan error, len(byRegion))
	for region, tenants := range byRegion {
		wg.Add(1)
		go func(region string, tenants []Tenant) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			}
			for _, tenant := range tenants {
				harvest, err := e.ensureTenantHarvest(ctx, tenant, started)
				if err != nil {
					errs <- err
					return
				}
				for uri, items := range harvest {
					var term Term
					for _, t := range e.terms {
						if t.URI == uri {
							term = t
							break
						}
					}
					for _, it := range items {
						for _, isbn := range it.isbns {
							for _, wi := range byISBN[isbn] {
								if hasSubject(&works[wi], uri) {
									continue
								}
								attr := ingest.Attribution{
									Source: tenant.Key(), Basis: "isbn", Key: isbn,
									Ref: "https://" + tenant.customerDomain() + "/search/card?recordId=" + it.id + "&entityType=FormatGroup",
								}
								aggMu.Lock()
								key := matchKey{work: wi, uri: uri}
								c := agg[key]
								if c == nil {
									c = &consensus{term: term, tenants: map[string]ingest.Attribution{}}
									agg[key] = c
									e.bump(started, func(st *ingest.EnrichStats) { st.Candidates++ })
								}
								if _, ok := c.tenants[tenant.Key()]; !ok {
									c.tenants[tenant.Key()] = attr
								}
								aggMu.Unlock()
							}
						}
					}
				}
			}
		}(region, tenants)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return nil, err
		}
	}

	// One Enrichment per work (every vega match is the same tier).
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
			keys := make([]string, 0, len(c.tenants))
			for k := range c.tenants {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			attrs := make([]ingest.Attribution, 0, len(keys))
			for _, k := range keys {
				attrs = append(attrs, c.tenants[k])
			}
			enr.Endorsements = append(enr.Endorsements, ingest.Endorsement{Count: len(keys), Sources: keys, Attributions: attrs})
		}
		out = append(out, enr)
	}
	return out, nil
}

// ensureTenantHarvest returns one tenant's term->FormatGroups harvest,
// memoized within the TTL. Concept resolution is shared region-wide through
// the concepts cache, so N tenants in one region resolve each label once.
func (e *Enricher) ensureTenantHarvest(ctx context.Context, tenant Tenant, started time.Time) (map[string][]fgItem, error) {
	e.cache.mu.Lock()
	th := e.cache.byTenant[tenant.Key()]
	if th == nil {
		th = &tenantHarvest{}
		e.cache.byTenant[tenant.Key()] = th
	}
	e.cache.mu.Unlock()

	th.mu.Lock()
	defer th.mu.Unlock()
	valid := 0
	for _, t := range e.terms {
		if t.Query != "" && t.URI != "" {
			valid++
		}
	}
	if th.items != nil && started.Sub(th.at) < e.cacheTTL {
		e.bump(started, func(st *ingest.EnrichStats) { st.Batches += valid })
		return th.items, nil
	}

	items := map[string][]fgItem{}
	unreachable := 0
	for _, term := range e.terms {
		if term.Query == "" || term.URI == "" {
			continue
		}
		conceptID, err := e.resolveConcept(ctx, tenant, term, started)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if ingest.IsUnreachable(err) {
				if unreachable++; unreachable >= ingest.UnreachableAbortAfter {
					return nil, fmt.Errorf("%w: %s", ingest.ErrPeerUnreachable, tenant.Key())
				}
			} else {
				unreachable = 0
			}
			e.bump(started, func(st *ingest.EnrichStats) { st.Batches++; st.SkippedBatches++ })
			if e.log != nil {
				e.log.Warn("vega concept resolution skipped", "tenant", tenant.Key(), "term", term.Query, "err", err)
			}
			continue
		}
		unreachable = 0
		if conceptID == "" {
			// The region has no homoit concept for this label; done.
			e.bump(started, func(st *ingest.EnrichStats) { st.Batches++ })
			continue
		}
		var fgs []fgItem
		failed := false
		for page := 0; page < e.maxPages; page++ {
			res, err := e.fetchResources(ctx, tenant, conceptID, page)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				e.bump(started, func(st *ingest.EnrichStats) { st.SkippedBatches++ })
				if e.log != nil {
					e.log.Warn("vega resources page skipped", "tenant", tenant.Key(), "term", term.Query, "page", page, "err", err)
				}
				failed = true
				break
			}
			fgs = append(fgs, res.items...)
			if page+1 >= res.totalPages {
				break
			}
			if page+1 == e.maxPages && e.log != nil {
				e.log.Info("vega concept truncated at page cap", "tenant", tenant.Key(), "term", term.Query, "pages", e.maxPages)
			}
		}
		if !failed {
			items[term.URI] = fgs
		}
		e.bump(started, func(st *ingest.EnrichStats) { st.Batches++ })
	}
	th.items = items
	th.at = started
	return items, nil
}

// resolveConcept maps a driver term to the region's shared concept id:
// suggestions by label, filtered to subject-role Concepts whose (em-tag
// stripped) term equals the label, gated on the concept record's
// source=="homoit". "" means resolved-with-no-match, cached like a hit.
func (e *Enricher) resolveConcept(ctx context.Context, tenant Tenant, term Term, started time.Time) (string, error) {
	key := tenant.Region + "\x00" + term.URI
	e.cache.mu.Lock()
	if c, ok := e.cache.concepts[key]; ok && started.Sub(c.at) < e.cacheTTL {
		e.cache.mu.Unlock()
		return c.id, nil
	}
	e.cache.mu.Unlock()

	body, err := e.get(ctx, tenant, "1", "/api/search/suggestions?phrase="+url.QueryEscape(term.Query))
	if err != nil {
		return "", err
	}
	var suggs []struct {
		Term  string   `json:"term"`
		ID    string   `json:"id"`
		Type  string   `json:"type"`
		Roles []string `json:"roles"`
	}
	if err := json.Unmarshal(body, &suggs); err != nil {
		return "", fmt.Errorf("vega: parse suggestions: %w", err)
	}
	resolved := ""
	for _, s := range suggs {
		if s.Type != "Concept" || !containsStr(s.Roles, "subject") {
			continue
		}
		if !strings.EqualFold(stripEm(s.Term), term.Query) {
			continue
		}
		body, err := e.get(ctx, tenant, "2", "/api/search-result/search/concepts/"+url.PathEscape(s.ID))
		if err != nil {
			return "", err
		}
		var concept struct {
			ID     string `json:"id"`
			Source string `json:"source"`
			Label  string `json:"label"`
		}
		if err := json.Unmarshal(body, &concept); err != nil {
			return "", fmt.Errorf("vega: parse concept: %w", err)
		}
		// Only an explicit Homosaurus concept counts -- the suggestion
		// list mixes in same-label LCSH concepts.
		if concept.Source == "homoit" {
			resolved = concept.ID
			break
		}
	}
	e.cache.mu.Lock()
	e.cache.concepts[key] = conceptEntry{id: resolved, at: started}
	e.cache.mu.Unlock()
	return resolved, nil
}

// resourcesPage is one parsed resources response.
type resourcesPage struct {
	totalPages int
	items      []fgItem
}

// fetchResources GETs one page of a concept's FormatGroups.
func (e *Enricher) fetchResources(ctx context.Context, tenant Tenant, conceptID string, page int) (resourcesPage, error) {
	path := fmt.Sprintf("/api/search-result/showcases/resources/resources/%s?id=%s&entityType=Concept&pageNum=%d&pageSize=%d&targetType=FormatGroup",
		url.PathEscape(conceptID), url.QueryEscape(conceptID), page, e.pageSize)
	body, err := e.get(ctx, tenant, "1", path)
	if err != nil {
		return resourcesPage{}, err
	}
	var res struct {
		TotalPages int `json:"totalPages"`
		Data       []struct {
			ID           string   `json:"id"`
			Title        []string `json:"$title"`
			IdentifiedBy []struct {
				Type  string `json:"type"`
				Value string `json:"value"`
			} `json:"identifiedBy"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return resourcesPage{}, fmt.Errorf("vega: parse resources: %w", err)
	}
	out := resourcesPage{totalPages: res.TotalPages}
	for _, fg := range res.Data {
		it := fgItem{id: fg.ID}
		if len(fg.Title) > 0 {
			it.title = fg.Title[0]
		}
		for _, ident := range fg.IdentifiedBy {
			if strings.EqualFold(ident.Type, "isbn") {
				if n := normalizeISBN(ident.Value); n != "" {
					it.isbns = append(it.isbns, n)
				}
			}
		}
		out.items = append(out.items, it)
	}
	return out, nil
}

// get performs one anonymous Vega API request with the tenant handshake
// headers, pausing the politeness delay first (all of a region's traffic
// hits one host; callers within a region are serialized already).
func (e *Enricher) get(ctx context.Context, tenant Tenant, apiVersion, path string) ([]byte, error) {
	if e.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(e.delay):
		}
	}
	u := "https://" + tenant.Region + ".iiivega.com" + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	host := tenant.customerDomain()
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", "https://"+host)
	req.Header.Set("Referer", "https://"+host+"/")
	req.Header.Set("iii-host-domain", host)
	req.Header.Set("iii-customer-domain", host)
	req.Header.Set("Anonymous-User-Id", e.anonID)
	req.Header.Set("api-version", apiVersion)
	req.Header.Set("User-Agent", "libcat-subject-harvest/1.0 (https://github.com/freeeve/libcat)")
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ingest.ErrEnricher, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: HTTP %d", ingest.ErrEnricher, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ingest.ErrEnricher, err)
	}
	return body, nil
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

var emRe = regexp.MustCompile(`</?em>`)

// stripEm removes the suggestion highlight tags ("Genderqueer <em>people</em>").
func stripEm(s string) string { return emRe.ReplaceAllString(s, "") }

// containsStr reports whether list holds v.
func containsStr(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// anonUUID mints the Anonymous-User-Id: any UUID-shaped value satisfies the
// API; derived from the clock so the package needs no crypto imports.
func anonUUID() string {
	n := time.Now().UnixNano()
	return fmt.Sprintf("%08x-%04x-4%03x-8%03x-%012x",
		uint32(n), uint16(n>>32), uint16(n>>48)&0xfff, uint16(n>>20)&0xfff, uint64(n)&0xffffffffffff)
}
