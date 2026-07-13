// Package sirsidynix harvests peer libraries' subject cataloging from
// SirsiDynix Enterprise catalogs at <lib>.ent.sirsidynix.net through the
// anonymous RSS hitlist: one GET per driver term scoped to the Subject
// index, so the term surfaces every record a peer cataloged under that
// subject heading.
//
// Like the BiblioCommons and TLC harvests this is the inference model --
// the subject index is unscoped (LCSH, Homosaurus and others merge, and a
// record carries no scheme tag), so the match is the exact Homosaurus
// prefLabel string, not Vega's authoritative source tag. Nearly every
// record carries an ISBN in its content block, which is the work-match key.
//
// Enterprise returns the whole hitlist in one response (ps=1000, no
// pagination). Some tenants front the catalog with a Cloudflare challenge;
// those answer with an HTML interstitial instead of an Atom feed and are
// detected and dropped with the skip counted, not harvested as empty.
package sirsidynix

import (
	"context"
	"encoding/xml"
	"fmt"
	"html"
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
const Name = "sirsidynix"

// confISBNMatch is the single confidence tier: an exact-prefLabel subject
// hitlist joined by a shared ISBN.
const confISBNMatch = 0.9

// baseDomain is appended to a bare tenant subdomain (e.g. "winca").
const baseDomain = ".ent.sirsidynix.net"

// defaultProfile is the Enterprise profile used when a tenant omits one.
const defaultProfile = "default"

// isbnRe pulls ISBNs from an entry's content block: an "ISBN" label (the
// non-breaking space between it and the number arrives entity-encoded) or a
// bare EAN-13, whichever the record carries.
var isbnRe = regexp.MustCompile(`ISBN[^0-9]{0,8}([0-9]{9,13}[0-9Xx]?)|(97[89][0-9]{10})`)

// Term is one driver term: the vocabulary URI the suggestion will carry and
// the label used as the Subject-scoped query.
type Term struct {
	URI    string
	Labels map[string]string
	Query  string
}

// Tenant is one Enterprise library: its catalog host and RSS profile. The
// hitlist lives at https://<Host>/client/rss/hitlist/<Profile>/qu=...
type Tenant struct {
	Host    string // full hostname, e.g. winca.ent.sirsidynix.net
	Profile string // Enterprise profile, e.g. default
}

// Key is the tenant's display / attribution identity: the subdomain label,
// with the profile appended only when it is not the default.
func (t Tenant) Key() string {
	label := strings.TrimSuffix(t.Host, baseDomain)
	if t.Profile != "" && t.Profile != defaultProfile {
		return label + "/" + t.Profile
	}
	return label
}

// ParseTenants reads the config form: comma-separated <host>[/<profile>]
// entries ("winca" or "winca/mobile" or "cat.example.org/pub"). A host with
// no dot is treated as a bare Enterprise subdomain and expanded to
// <host>.ent.sirsidynix.net; a host with a dot is used verbatim. The
// profile defaults to "default".
func ParseTenants(spec string) ([]Tenant, error) {
	var out []Tenant
	for _, e := range strings.Split(spec, ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if strings.Contains(e, "://") {
			return nil, fmt.Errorf("sirsidynix: tenant %q wants a bare host, not a URL", e)
		}
		host, profile := e, defaultProfile
		if i := strings.IndexByte(e, '/'); i >= 0 {
			host, profile = strings.TrimSpace(e[:i]), strings.TrimSpace(e[i+1:])
		}
		if host == "" || profile == "" {
			return nil, fmt.Errorf("sirsidynix: tenant %q wants <host>[/<profile>] (e.g. winca or winca/mobile)", e)
		}
		if !strings.Contains(host, ".") {
			host += baseDomain
		}
		out = append(out, Tenant{Host: host, Profile: profile})
	}
	return out, nil
}

// Doer is the HTTP seam, injectable for tests.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Enricher harvests one or more Enterprise tenants.
type Enricher struct {
	client  Doer
	tenants []Tenant
	terms   []Term
	// delay is the politeness pause between requests to ONE host; distinct
	// hosts crawl concurrently, capped.
	delay           time.Duration
	hostConcurrency int
	// maxRetries is how many extra attempts a request gets after a transient
	// failure (a connection refused under harvest load, a 5xx); retryBase is
	// the first backoff pause, doubled each attempt.
	maxRetries int
	retryBase  time.Duration
	log        *slog.Logger

	statsMu sync.Mutex
	stats   ingest.EnrichStats

	cache    *harvestCache
	cacheTTL time.Duration
}

// harvestCache memoizes per-tenant completed crawls, shared across per-run
// views.
type harvestCache struct {
	mu       sync.Mutex
	byTenant map[string]*tenantHarvest
}

type tenantHarvest struct {
	mu    sync.Mutex
	items map[string][]record // term URI -> matched records
	at    time.Time
}

// record is one Enterprise hitlist entry reduced to matching fields.
type record struct {
	title string
	link  string
	isbns []string
}

// Option configures the enricher.
type Option func(*Enricher)

// WithClient injects the HTTP client (tests).
func WithClient(d Doer) Option { return func(e *Enricher) { e.client = d } }

// WithDelay overrides the politeness pause.
func WithDelay(d time.Duration) Option { return func(e *Enricher) { e.delay = d } }

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

// WithMaxRetries overrides how many extra attempts a transiently-failing
// request gets (0 disables retry).
func WithMaxRetries(n int) Option {
	return func(e *Enricher) {
		if n >= 0 {
			e.maxRetries = n
		}
	}
}

// WithRetryBase overrides the first retry backoff pause.
func WithRetryBase(d time.Duration) Option { return func(e *Enricher) { e.retryBase = d } }

// New returns the harvester for the given tenants and driver term list.
func New(tenants []Tenant, terms []Term, opts ...Option) *Enricher {
	e := &Enricher{
		client:          &http.Client{Timeout: ingest.DefaultRequestTimeout},
		tenants:         tenants,
		terms:           terms,
		delay:           1500 * time.Millisecond,
		hostConcurrency: 4,
		maxRetries:      3,
		retryBase:       time.Second,
		cacheTTL:        24 * time.Hour,
		cache:           &harvestCache{byTenant: map[string]*tenantHarvest{}},
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
// terms, Batches the (tenant, term) crawls completed (cache-warm counted
// instantly), SkippedBatches the per-tenant terms abandoned on an error or
// a challenge interstitial, Candidates the live matches so far. Safe
// mid-run.
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

// Enrich implements ingest.Enricher: per tenant (concurrent, capped) run one
// Subject-scoped hitlist per driver term (memoized within the TTL), match
// entry ISBNs back to the scoped works, and emit one suggestion per (work,
// term) endorsed by every corroborating tenant.
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
		term  Term
		hosts map[string]ingest.Attribution
	}
	var aggMu sync.Mutex
	agg := map[matchKey]*consensus{}

	sem := make(chan struct{}, e.hostConcurrency)
	var wg sync.WaitGroup
	errs := make(chan error, len(e.tenants))
	for _, tenant := range e.tenants {
		wg.Add(1)
		go func(tenant Tenant) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			}
			harvest, err := e.ensureTenantHarvest(ctx, tenant, started)
			if err != nil {
				errs <- err
				return
			}
			key := tenant.Key()
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
							attr := ingest.Attribution{Source: key, Basis: "isbn", Key: isbn, Ref: rec.link}
							aggMu.Lock()
							mk := matchKey{work: wi, uri: uri}
							c := agg[mk]
							if c == nil {
								c = &consensus{term: term, hosts: map[string]ingest.Attribution{}}
								agg[mk] = c
								e.bump(started, func(st *ingest.EnrichStats) { st.Candidates++ })
							}
							if _, ok := c.hosts[key]; !ok {
								c.hosts[key] = attr
							}
							aggMu.Unlock()
						}
					}
				}
			}
		}(tenant)
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

// ensureTenantHarvest returns one tenant's term->records harvest, memoized
// within the TTL; concurrent crawlers single-flight per tenant.
func (e *Enricher) ensureTenantHarvest(ctx context.Context, tenant Tenant, started time.Time) (map[string][]record, error) {
	key := tenant.Key()
	e.cache.mu.Lock()
	th := e.cache.byTenant[key]
	if th == nil {
		th = &tenantHarvest{}
		e.cache.byTenant[key] = th
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

	items := map[string][]record{}
	for _, term := range e.terms {
		if term.Query == "" || term.URI == "" {
			continue
		}
		recs, err := e.hitlist(ctx, tenant, term.Query)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			e.bump(started, func(st *ingest.EnrichStats) { st.SkippedBatches++ })
			if e.log != nil {
				e.log.Warn("sirsidynix hitlist skipped", "tenant", key, "term", term.Query, "err", err)
			}
			e.bump(started, func(st *ingest.EnrichStats) { st.Batches++ })
			continue
		}
		items[term.URI] = recs
		e.bump(started, func(st *ingest.EnrichStats) { st.Batches++ })
	}
	th.items = items
	th.at = started
	return items, nil
}

// atomFeed is the subset of the Enterprise RSS hitlist we read.
type atomFeed struct {
	XMLName xml.Name    `xml:"feed"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	Title   string     `xml:"title"`
	Links   []atomLink `xml:"link"`
	Content string     `xml:"content"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
}

// hitlist fetches one Subject-scoped RSS hitlist and extracts the records
// carrying an ISBN. Enterprise returns the whole result set in one response
// (ps=1000), so there is no pagination.
func (e *Enricher) hitlist(ctx context.Context, tenant Tenant, label string) ([]record, error) {
	// The query lives in the path segment: qu=<label> with the SUBJECT
	// index selector (pipes percent-encoded), ps=1000 for the full hitlist.
	q := "qu=" + strings.ReplaceAll(url.QueryEscape(label), "+", "%20") +
		"&rt=false%7C%7C%7CSUBJECT%7C%7C%7CSubject&ps=1000"
	target := "https://" + tenant.Host + "/client/rss/hitlist/" + tenant.Profile + "/" + q
	body, err := e.fetch(ctx, tenant, target)
	if err != nil {
		return nil, err
	}
	// A Cloudflare-gated tenant answers with an HTML challenge, not an Atom
	// feed. Detect it and fail loudly so the skip is counted, rather than
	// parsing zero entries and reporting a silent empty harvest.
	if isChallenge(body) {
		return nil, fmt.Errorf("%w: challenge interstitial (host gated)", ingest.ErrEnricher)
	}
	var feed atomFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("sirsidynix: parse hitlist: %w", err)
	}
	var recs []record
	for _, ent := range feed.Entries {
		isbns := extractISBNs(ent.Content)
		if len(isbns) == 0 {
			continue
		}
		recs = append(recs, record{title: strings.TrimSpace(ent.Title), link: alternateLink(ent.Links), isbns: isbns})
	}
	return recs, nil
}

// fetch GETs one hitlist URL with per-tenant politeness spacing and retry:
// a transient failure (a connection refused under harvest load, a 5xx)
// backs off and tries again up to maxRetries, so a busy Enterprise host is
// paced through rather than losing the whole term. The politeness pause
// precedes every attempt; the backoff (retryBase, doubled) is added before
// each retry.
func (e *Enricher) fetch(ctx context.Context, tenant Tenant, target string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= e.maxRetries; attempt++ {
		wait := e.delay
		if attempt > 0 {
			wait += e.retryBase << (attempt - 1)
		}
		if wait > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/atom+xml, application/xml;q=0.9, */*;q=0.8")
		req.Header.Set("User-Agent", "libcat-subject-harvest/1.0 (https://github.com/freeeve/libcat)")
		resp, err := e.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = fmt.Errorf("%w: %w", ingest.ErrEnricher, err)
			e.logRetry(tenant, target, attempt, err)
			continue
		}
		if retryableStatus(resp.StatusCode) {
			resp.Body.Close()
			lastErr = fmt.Errorf("%w: HTTP %d", ingest.ErrEnricher, resp.StatusCode)
			e.logRetry(tenant, target, attempt, lastErr)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("%w: HTTP %d", ingest.ErrEnricher, resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = fmt.Errorf("%w: %w", ingest.ErrEnricher, err)
			e.logRetry(tenant, target, attempt, err)
			continue
		}
		return body, nil
	}
	return nil, lastErr
}

// logRetry notes a retried attempt (best effort; nil logger stays silent).
func (e *Enricher) logRetry(tenant Tenant, target string, attempt int, err error) {
	if e.log != nil && attempt < e.maxRetries {
		e.log.Warn("sirsidynix request retrying", "tenant", tenant.Key(), "attempt", attempt+1, "err", err)
	}
}

// retryableStatus reports whether an HTTP status is worth another attempt:
// rate limiting and the transient 5xx family.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

// isChallenge reports whether a response body is a Cloudflare (or similar)
// HTML interstitial rather than the Atom feed.
func isChallenge(body []byte) bool {
	head := strings.ToLower(string(body[:min(len(body), 2048)]))
	if strings.Contains(head, "<feed") {
		return false
	}
	return strings.Contains(head, "<html") ||
		strings.Contains(head, "just a moment") ||
		strings.Contains(head, "cf-browser-verification") ||
		strings.Contains(head, "challenge-platform")
}

// alternateLink returns the record's rel="alternate" href, or the first
// link if none is tagged alternate.
func alternateLink(links []atomLink) string {
	for _, l := range links {
		if strings.EqualFold(l.Rel, "alternate") && l.Href != "" {
			return l.Href
		}
	}
	for _, l := range links {
		if l.Href != "" {
			return l.Href
		}
	}
	return ""
}

// extractISBNs pulls the normalized ISBNs from an entry's content block,
// de-duplicating within the entry. Enterprise separates the "ISBN" label
// from the number with a non-breaking space that arrives as a numeric
// character reference (&#160;) whose own digits would defeat the matcher,
// so the content is HTML-unescaped first.
func extractISBNs(content string) []string {
	content = html.UnescapeString(content)
	var out []string
	seen := map[string]bool{}
	for _, m := range isbnRe.FindAllStringSubmatch(content, -1) {
		raw := m[1]
		if raw == "" {
			raw = m[2]
		}
		if n := normalizeISBN(raw); n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
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
