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

// Enricher harvests one BiblioCommons OPAC.
type Enricher struct {
	client Doer
	// host is the BiblioCommons subdomain (e.g. "ccslib").
	host  string
	terms []Term
	// displayQuantity is items per RSS page; maxPages caps pages per term
	// (big terms run to hundreds of pages; a capped harvest says so).
	displayQuantity int
	maxPages        int
	// delay is the politeness pause between requests (a shared public
	// OPAC; the prototype used 1.5s).
	delay time.Duration
	log   *slog.Logger

	statsMu sync.Mutex
	stats   ingest.EnrichStats

	// The harvest is memoized: its cost is per TERM, independent of which
	// works are in scope, so a re-run (or a re-scope) within the TTL
	// re-matches without touching the peer OPAC again.
	cacheMu  sync.Mutex
	cache    []harvested
	cachedAt time.Time
	cacheTTL time.Duration
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

// New returns the harvester for one host and driver term list.
func New(host string, terms []Term, opts ...Option) *Enricher {
	e := &Enricher{
		client:          http.DefaultClient,
		host:            host,
		terms:           terms,
		displayQuantity: 100,
		maxPages:        6,
		delay:           1500 * time.Millisecond,
		cacheTTL:        24 * time.Hour,
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Name implements ingest.Enricher.
func (e *Enricher) Name() string { return Name }

// RunStats implements ingest.StatsReporter: Total is the driver term count
// (known at construction), Batches the terms processed so far -- so
// Batches/Total is a true progress fraction -- and SkippedBatches the terms
// abandoned on a fetch error. Safe mid-run.
func (e *Enricher) RunStats() ingest.EnrichStats {
	e.statsMu.Lock()
	defer e.statsMu.Unlock()
	return e.stats
}

func (e *Enricher) setStats(st ingest.EnrichStats) {
	e.statsMu.Lock()
	e.stats = st
	e.statsMu.Unlock()
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
}

// parseFeed extracts the match-relevant fields from one RSS page.
func parseFeed(body []byte) ([]harvestItem, error) {
	var feed rssFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("bibliocommons: parse rss: %w", err)
	}
	out := make([]harvestItem, 0, len(feed.Items))
	for _, it := range feed.Items {
		h := harvestItem{title: strings.TrimSpace(it.Title)}
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
// (memoized within the cache TTL), match items back to the scoped works, and
// suggest the DRIVER term on every matched work that does not already carry
// it.
func (e *Enricher) Enrich(ctx context.Context, works []ingest.WorkSummary) ([]ingest.Enrichment, error) {
	started := time.Now()
	harvest, err := e.ensureHarvest(ctx, started)
	if err != nil {
		return nil, err
	}

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

	type tierKey struct {
		work int
		conf float64
	}
	suggested := map[tierKey][]Term{}
	seen := map[string]bool{} // workIdx|uri
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
				key := fmt.Sprintf("%d|%s", wi, h.term.URI)
				if seen[key] {
					continue
				}
				seen[key] = true
				tk := tierKey{work: wi, conf: conf}
				suggested[tk] = append(suggested[tk], h.term)
			}
		}
	}

	var out []ingest.Enrichment
	for i := range works {
		for _, conf := range []float64{confISBNMatch, confTitleMatch} {
			terms := suggested[tierKey{work: i, conf: conf}]
			if len(terms) == 0 {
				continue
			}
			enr := ingest.Enrichment{WorkID: works[i].WorkID, Confidence: conf}
			for _, t := range terms {
				enr.Subjects = append(enr.Subjects, bibframe.AuthoritySubject{URI: t.URI, Labels: t.Labels})
			}
			out = append(out, enr)
		}
	}
	return out, nil
}

// ensureHarvest returns the term-by-term feed crawl, reusing a completed one
// within the TTL so a re-run or re-scope is match-only.
func (e *Enricher) ensureHarvest(ctx context.Context, started time.Time) ([]harvested, error) {
	e.cacheMu.Lock()
	defer e.cacheMu.Unlock()
	if e.cache != nil && started.Sub(e.cachedAt) < e.cacheTTL {
		return e.cache, nil
	}

	var st ingest.EnrichStats
	publish := func() {
		st.ElapsedMS = time.Since(started).Milliseconds()
		e.setStats(st)
	}
	// The run is sized up front -- one search per driver term -- so
	// progress is a true fraction: Batches terms done out of Total.
	for _, term := range e.terms {
		if term.Query != "" && term.URI != "" {
			st.Total++
		}
	}
	publish()
	var harvest []harvested
	first := true
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
			items, err := e.fetchPage(ctx, term.Query, page)
			if err != nil {
				st.SkippedBatches++
				publish()
				if e.log != nil {
					e.log.Warn("bibliocommons page skipped", "term", term.Query, "page", page, "err", err)
				}
				break // abandon this term; the next run backfills
			}
			publish()
			h.items = append(h.items, items...)
			if len(items) < e.displayQuantity {
				break // short page = the term's last page
			}
			if page == e.maxPages && e.log != nil {
				e.log.Info("bibliocommons term truncated at page cap", "term", term.Query, "pages", e.maxPages)
			}
		}
		st.Batches++
		publish()
		harvest = append(harvest, h)
	}
	publish()
	e.cache = harvest
	e.cachedAt = started
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

// fetchPage GETs one RSS page of a subject search.
func (e *Enricher) fetchPage(ctx context.Context, query string, page int) ([]harvestItem, error) {
	u := fmt.Sprintf("https://%s.bibliocommons.com/search/rss?q=%s&t=subject&display_quantity=%d&page=%d",
		e.host, url.QueryEscape(query), e.displayQuantity, page)
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
