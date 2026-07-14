// Package bibliocommons harvests a peer library's subject cataloging through
// the public BiblioCommons RSS search -- the reverse direction, because the
// forward one does not exist: a record page exposes no subjects to an
// unauthenticated reader, but a SUBJECT search feeds every title it is
// assigned to. Driving the queries from a loaded vocabulary's terms turns
// that into "which of OUR works does the peer catalog under this term",
// matched by ISBN first and normalized title+author second, and emitted as
// moderated suggestions of the DRIVER term.
//
// Inherent bound, by design and documented: the harvest only surfaces terms
// it queries. It can confirm a peer's use of a term we asked about; it can
// never reveal one we did not. Coverage equals the driver term list.
package bibliocommons

import (
	"context"
	"encoding/xml"
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
const Name = "bibliocommons"

// Suggestion confidence by match kind, on the shared scale: an identifier
// match is near-certain, a normalized title+author match needs review.
const (
	confISBNMatch  = 0.9
	confTitleMatch = 0.75
)

// Term is one driver term: the vocabulary URI the suggestion will carry and
// the label forms to query the peer OPAC with.
type Term struct {
	URI    string
	Labels map[string]string
	// Query is the search string (typically the English prefLabel).
	Query string
}

// Doer is the HTTP seam, injectable for tests.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Enricher harvests one or more BiblioCommons OPACs. Multiple hosts turn
// the harvest into a consensus vote: the same term matched to the same
// work from several peers emits one suggestion endorsed by all of them.
type Enricher struct {
	client Doer
	// hosts are BiblioCommons subdomains (e.g. "ccslib", "seattle").
	hosts []string
	terms []Term
	// displayQuantity is items per RSS page; maxPages caps pages per term
	// (big terms run to hundreds of pages; a capped harvest says so).
	displayQuantity int
	maxPages        int
	// delay is the politeness pause between requests to ONE host (a shared
	// public OPAC; the prototype used 1.5s). Distinct hosts crawl
	// concurrently (capped) -- politeness is per host.
	delay time.Duration
	log   *slog.Logger

	// hostConcurrency caps how many peer OPACs crawl at once; each host's
	// own crawl stays sequential with the politeness delay. Default 4; a
	// wide consensus run (20 known Homosaurus peers) may raise it.
	hostConcurrency int

	statsMu sync.Mutex
	stats   ingest.EnrichStats

	// The harvest is memoized per host: its cost is per (host, term),
	// independent of which works are in scope, so a re-run (or a re-scope,
	// or an overlapping host list) within the TTL re-matches without
	// touching that peer again. Shared across ForHosts views.
	cache    *harvestCache
	cacheTTL time.Duration
}

// harvestCache holds per-host completed crawls, shared across per-run
// enricher views.
type harvestCache struct {
	mu     sync.Mutex
	byHost map[string]*hostHarvest
}

// hostHarvest is one host's crawl; its mutex single-flights concurrent
// crawlers of the same host.
type hostHarvest struct {
	mu    sync.Mutex
	items []harvested
	at    time.Time
}

// harvested is one driver term's collected feed items.
type harvested struct {
	term  Term
	items []harvestItem
}

// Option configures the enricher.
type Option func(*Enricher)

// WithClient injects the HTTP client (tests).
func WithClient(d Doer) Option { return func(e *Enricher) { e.client = d } }

// WithDelay overrides the politeness pause.
func WithDelay(d time.Duration) Option { return func(e *Enricher) { e.delay = d } }

// WithMaxPages caps RSS pages fetched per term.
func WithMaxPages(n int) Option { return func(e *Enricher) { e.maxPages = n } }

// WithLogger wires progress logging.
func WithLogger(l *slog.Logger) Option { return func(e *Enricher) { e.log = l } }

// WithCacheTTL overrides how long a completed harvest is reused before the
// peer OPAC is crawled again.
func WithCacheTTL(d time.Duration) Option { return func(e *Enricher) { e.cacheTTL = d } }

// WithHostConcurrency overrides how many peer OPACs crawl at once
// (politeness stays per host; non-positive values keep the default).
func WithHostConcurrency(n int) Option {
	return func(e *Enricher) {
		if n > 0 {
			e.hostConcurrency = n
		}
	}
}

// New returns the harvester for the given peer hosts and driver term list.
func New(hosts []string, terms []Term, opts ...Option) *Enricher {
	e := &Enricher{
		client:          &http.Client{Timeout: ingest.DefaultRequestTimeout},
		hosts:           hosts,
		terms:           terms,
		displayQuantity: 100,
		maxPages:        6,
		delay:           1500 * time.Millisecond,
		cacheTTL:        24 * time.Hour,
		hostConcurrency: 4,
		cache:           &harvestCache{byHost: map[string]*hostHarvest{}},
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// ForHosts returns a per-run view over a different peer list -- the per-job
// host override. The crawl cache is shared (a host crawled by any view is
// warm for every view within the TTL); the run stats are the view's own.
func (e *Enricher) ForHosts(hosts []string) ingest.Enricher {
	return &Enricher{
		client:          e.client,
		hosts:           append([]string(nil), hosts...),
		terms:           e.terms,
		displayQuantity: e.displayQuantity,
		maxPages:        e.maxPages,
		delay:           e.delay,
		log:             e.log,
		cacheTTL:        e.cacheTTL,
		hostConcurrency: e.hostConcurrency,
		cache:           e.cache,
	}
}

// Name implements ingest.Enricher.
func (e *Enricher) Name() string { return Name }

// Describe names the peer libraries this view pulls from.
func (e *Enricher) Describe() string { return strings.Join(e.hosts, ", ") }

// RunStats implements ingest.StatsReporter: Total is hosts x driver terms
// (known at construction), Batches the (host, term) crawls completed so far
// -- so Batches/Total is a true progress fraction, cache-warm hosts counted
// instantly -- and SkippedBatches the per-host terms abandoned on a fetch
// error. Safe mid-run.
func (e *Enricher) RunStats() ingest.EnrichStats {
	e.statsMu.Lock()
	defer e.statsMu.Unlock()
	return e.stats
}

// rssItem is one <item> of the search feed.
type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
}

type rssFeed struct {
	Items []rssItem `xml:"channel>item"`
}

// The description is entity-encoded HTML of labeled fields
// ("<b>Author:</b> <a ...>England, Autumn K.</a><br/>..."): authorRe reads
// the labeled Author field -- NOT any free-text "by ...", which the blurb
// paragraph is full of -- and syndeticsRe the identifier-bearing Syndetics
// cover URL.
var (
	syndeticsRe = regexp.MustCompile(`(?i)s(?:ecure)?\.syndetics\.com/index\.aspx\?([^"'\s]+)`)
	authorRe    = regexp.MustCompile(`(?is)Author:\s*</b>\s*(?:<a[^>]*>)?\s*([^<]+)`)
)

// harvestItem is one parsed feed row.
type harvestItem struct {
	title, author, isbn, oclc string
	// link is the peer OPAC's record page (/item/show/<bibid>_slug) -- the
	// moderator's one-click door to the record actually matched.
	link string
}

// parseFeed extracts the match-relevant fields from one RSS page. The body
// is scrubbed of XML-illegal control characters first: peer feeds carry
// stray C0 bytes (U+0003, U+0019...) inside description fields, and one
// such byte makes xml.Unmarshal reject the whole page -- which cost the
// whole subject term, persistently, since the bad byte lives in the source
// record (a Seattle harvest lost 18 terms this way).
func parseFeed(body []byte) ([]harvestItem, error) {
	var feed rssFeed
	if err := xml.Unmarshal(scrubXML(body), &feed); err != nil {
		return nil, fmt.Errorf("bibliocommons: parse rss: %w", err)
	}
	out := make([]harvestItem, 0, len(feed.Items))
	for _, it := range feed.Items {
		h := harvestItem{title: strings.TrimSpace(it.Title), link: strings.TrimSpace(it.Link)}
		if m := authorRe.FindStringSubmatch(it.Description); m != nil {
			h.author = strings.TrimSpace(strings.TrimRight(m[1], " .,"))
		}
		if m := syndeticsRe.FindStringSubmatch(it.Description); m != nil {
			// The description is HTML, so the URL's separators arrive as
			// literal "&amp;" even after XML decoding; ParseQuery rejects
			// the ';' they contain.
			raw := strings.ReplaceAll(m[1], "&amp;", "&")
			if q, err := url.ParseQuery(raw); err == nil {
				h.isbn = normalizeISBN(q.Get("isbn"))
				h.oclc = strings.TrimSpace(q.Get("oclc"))
			}
		}
		out = append(out, h)
	}
	return out, nil
}

// scrubXML drops the characters XML 1.0 forbids outright -- C0 controls
// except tab/LF/CR, plus DEL and the C1 range -- and leaves everything else
// (including multi-byte UTF-8) untouched. Allocation-free when the body is
// already clean, which is nearly every page.
func scrubXML(body []byte) []byte {
	clean := true
	for _, b := range body {
		if (b < 0x20 && b != '\t' && b != '\n' && b != '\r') || b == 0x7f {
			clean = false
			break
		}
	}
	if clean {
		return body
	}
	out := make([]byte, 0, len(body))
	for _, b := range body {
		if (b < 0x20 && b != '\t' && b != '\n' && b != '\r') || b == 0x7f {
			continue
		}
		out = append(out, b)
	}
	return out
}

// normalizeISBN strips hyphens and a Syndetics "/SC.GIF"-style suffix.
func normalizeISBN(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.IndexByte(v, '/'); i >= 0 {
		v = v[:i]
	}
	return strings.ReplaceAll(v, "-", "")
}

// normTitle folds a title for fallback matching: lowercase alphanumeric
// tokens, subtitle (after ':') dropped.
func normTitle(s string) string {
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	var b strings.Builder
	space := true
	for _, r := range strings.ToLower(s) {
		alnum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		switch {
		case alnum:
			b.WriteRune(r)
			space = false
		case !space:
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

// parentheticalRe strips cataloging qualifiers off a peer author string:
// "Allen, Samantha (Journalist)" must token-match a plain "Samantha Allen".
var parentheticalRe = regexp.MustCompile(`\([^)]*\)`)

// authorTokens folds an author string into its name tokens, order-blind
// ("Bechdel, Alison" and "Alison Bechdel" share the same set). Qualifiers
// and life dates are dropped -- they are cataloging apparatus, not name.
func authorTokens(s string) map[string]bool {
	s = parentheticalRe.ReplaceAllString(s, " ")
	out := map[string]bool{}
	for _, tok := range strings.Fields(normTitle(s)) {
		if strings.IndexFunc(tok, func(r rune) bool { return r < '0' || r > '9' }) == -1 {
			continue // an all-digit token is a life date
		}
		out[tok] = true
	}
	return out
}

// authorsAgree requires every peer author token to appear among the work's
// contributor tokens -- the title-only false-positive fix the prototype
// measured (~1,400 fallback matches with no author check).
func authorsAgree(peer string, contributors []string) bool {
	want := authorTokens(peer)
	if len(want) == 0 {
		return false
	}
	have := map[string]bool{}
	for _, c := range contributors {
		for tok := range authorTokens(c) {
			have[tok] = true
		}
	}
	for tok := range want {
		if !have[tok] {
			return false
		}
	}
	return true
}

// Enrich implements ingest.Enricher: harvest each driver term's RSS feed
// from every configured host (memoized per host within the cache TTL; hosts
// crawl concurrently, capped), match items back to the scoped works, and
// suggest the DRIVER term on every matched work that does not already carry
// it. The same (work, term) pair matched from several hosts emits ONE
// suggestion endorsed by all of them, at the strongest tier any host earned.
func (e *Enricher) Enrich(ctx context.Context, works []ingest.WorkSummary) ([]ingest.Enrichment, error) {
	started := time.Now()
	valid := 0
	for _, term := range e.terms {
		if term.Query != "" && term.URI != "" {
			valid++
		}
	}
	e.bump(started, func(st *ingest.EnrichStats) { st.Total = valid * len(e.hosts) })

	// Corpus match indexes.
	byISBN := map[string][]int{}
	byTitle := map[string][]int{}
	for i := range works {
		for _, isbn := range works[i].ISBNs {
			if n := normalizeISBN(isbn); n != "" {
				byISBN[n] = append(byISBN[n], i)
			}
		}
		if t := normTitle(works[i].Title); t != "" {
			byTitle[t] = append(byTitle[t], i)
		}
	}

	// Consensus accumulator: per (work, term URI), the strongest tier any
	// host earned and every host that corroborated.
	type matchKey struct {
		work int
		uri  string
	}
	type consensus struct {
		term  Term
		conf  float64
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
			for _, h := range harvest {
				for _, it := range h.items {
					var idx []int
					conf := confISBNMatch
					if it.isbn != "" && len(byISBN[it.isbn]) > 0 {
						idx = byISBN[it.isbn]
					} else if t := normTitle(it.title); t != "" && len(byTitle[t]) > 0 && it.author != "" {
						// Fallback demands author agreement -- a bare title
						// match on generic titles is the measured noise.
						for _, wi := range byTitle[t] {
							if authorsAgree(it.author, works[wi].Contributors) {
								idx = append(idx, wi)
							}
						}
						conf = confTitleMatch
					}
					for _, wi := range idx {
						if hasSubject(&works[wi], h.term.URI) {
							continue
						}
						attr := ingest.Attribution{Source: host, Basis: "isbn", Key: it.isbn, Ref: it.link}
						if conf == confTitleMatch {
							attr.Basis = "title+author"
							attr.Key = it.title + " / " + it.author
						}
						aggMu.Lock()
						key := matchKey{work: wi, uri: h.term.URI}
						c := agg[key]
						if c == nil {
							c = &consensus{term: h.term, conf: conf, hosts: map[string]ingest.Attribution{}}
							agg[key] = c
							// A NEW (work, term) pair is one live candidate;
							// further hosts corroborate, they don't add.
							e.bump(started, func(st *ingest.EnrichStats) { st.Candidates++ })
						}
						if conf > c.conf {
							c.conf = conf
						}
						// The strongest evidence per host wins (an ISBN match
						// outranks a later title match from the same host).
						if prev, ok := c.hosts[host]; !ok || (prev.Basis != "isbn" && attr.Basis == "isbn") {
							c.hosts[host] = attr
						}
						aggMu.Unlock()
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

	// One Enrichment per (work, tier), subjects beside their endorsements.
	type tierKey struct {
		work int
		conf float64
	}
	grouped := map[tierKey][]*consensus{}
	for key, c := range agg {
		tk := tierKey{work: key.work, conf: c.conf}
		grouped[tk] = append(grouped[tk], c)
	}
	var out []ingest.Enrichment
	for i := range works {
		for _, conf := range []float64{confISBNMatch, confTitleMatch} {
			group := grouped[tierKey{work: i, conf: conf}]
			if len(group) == 0 {
				continue
			}
			sort.Slice(group, func(a, b int) bool { return group[a].term.URI < group[b].term.URI })
			enr := ingest.Enrichment{WorkID: works[i].WorkID, Confidence: conf}
			for _, c := range group {
				enr.Subjects = append(enr.Subjects, bibframe.AuthoritySubject{URI: c.term.URI, Labels: c.term.Labels})
				hosts := make([]string, 0, len(c.hosts))
				for h := range c.hosts {
					hosts = append(hosts, h)
				}
				sort.Strings(hosts)
				attrs := make([]ingest.Attribution, 0, len(hosts))
				for _, h := range hosts {
					attrs = append(attrs, c.hosts[h])
				}
				enr.Endorsements = append(enr.Endorsements, ingest.Endorsement{Count: len(hosts), Sources: hosts, Attributions: attrs})
			}
			out = append(out, enr)
		}
	}
	return out, nil
}

// bump mutates the run stats under the lock and refreshes the elapsed time.
func (e *Enricher) bump(started time.Time, f func(*ingest.EnrichStats)) {
	e.statsMu.Lock()
	f(&e.stats)
	e.stats.ElapsedMS = time.Since(started).Milliseconds()
	e.statsMu.Unlock()
}

// ensureHostHarvest returns one host's term-by-term crawl, reusing a
// completed one within the TTL (shared across ForHosts views; concurrent
// crawlers of the same host single-flight on its entry). A cache hit still
// advances Batches by the term count, so the progress fraction is honest
// whichever hosts were warm.
func (e *Enricher) ensureHostHarvest(ctx context.Context, host string, started time.Time) ([]harvested, error) {
	e.cache.mu.Lock()
	hh := e.cache.byHost[host]
	if hh == nil {
		hh = &hostHarvest{}
		e.cache.byHost[host] = hh
	}
	e.cache.mu.Unlock()

	hh.mu.Lock()
	defer hh.mu.Unlock()
	if hh.items != nil && started.Sub(hh.at) < e.cacheTTL {
		done := 0
		for _, term := range e.terms {
			if term.Query != "" && term.URI != "" {
				done++
			}
		}
		e.bump(started, func(st *ingest.EnrichStats) { st.Batches += done })
		return hh.items, nil
	}

	var harvest []harvested
	first := true
	unreachable := 0
	for _, term := range e.terms {
		if term.Query == "" || term.URI == "" {
			continue
		}
		h := harvested{term: term}
		for page := 1; page <= e.maxPages; page++ {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if !first && e.delay > 0 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(e.delay):
				}
			}
			first = false
			items, err := e.fetchPage(ctx, host, term.Query, page)
			if err != nil {
				if ingest.IsUnreachable(err) {
					if unreachable++; unreachable >= ingest.UnreachableAbortAfter {
						return nil, fmt.Errorf("%w: %s", ingest.ErrPeerUnreachable, host)
					}
				} else {
					unreachable = 0
				}
				e.bump(started, func(st *ingest.EnrichStats) { st.SkippedBatches++ })
				if e.log != nil {
					e.log.Warn("bibliocommons page skipped", "host", host, "term", term.Query, "page", page, "err", err)
				}
				break // abandon this term on this host; the next run backfills
			}
			unreachable = 0
			h.items = append(h.items, items...)
			if len(items) < e.displayQuantity {
				break // short page = the term's last page
			}
			if page == e.maxPages && e.log != nil {
				e.log.Info("bibliocommons term truncated at page cap", "host", host, "term", term.Query, "pages", e.maxPages)
			}
		}
		e.bump(started, func(st *ingest.EnrichStats) { st.Batches++ })
		harvest = append(harvest, h)
	}
	hh.items = harvest
	hh.at = started
	return harvest, nil
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

// fetchPage GETs one RSS page of one host's subject search.
func (e *Enricher) fetchPage(ctx context.Context, host, query string, page int) ([]harvestItem, error) {
	u := fmt.Sprintf("https://%s.bibliocommons.com/search/rss?q=%s&t=subject&display_quantity=%d&page=%d",
		host, url.QueryEscape(query), e.displayQuantity, page)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "libcat-subject-harvest/1.0 (https://github.com/freeeve/libcat)")
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ingest.ErrEnricher, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: HTTP %d", ingest.ErrEnricher, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ingest.ErrEnricher, err)
	}
	return parseFeed(body)
}
