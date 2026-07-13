package sirsidynix

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/freeeve/libcat/ingest"
)

// entry mirrors the live winca Atom shape (probed 2026-07-13): the ISBN sits
// in the content block after an "ISBN" label separated by a non-breaking
// space that arrives double-encoded (&amp;#160;), and the record link is the
// rel="alternate" detail URL.
func entry(recordID int, title string, isbns ...string) string {
	var isbnBlocks strings.Builder
	for _, n := range isbns {
		fmt.Fprintf(&isbnBlocks, "ISBN&amp;#160;%s&lt;br/&gt;", n)
	}
	href := fmt.Sprintf("https://winca.ent.sirsidynix.net/client/en_US/default/search/detailnonmodal?qu=Lesbians&amp;d=ent%%3A%%2F%%2FSD_ILS%%2F0%%2FSD_ILS%%3A%d", recordID)
	return fmt.Sprintf(`<entry>
    <title type="html">%s</title>
    <link rel="alternate" type="html" href="%s" title="%s" />
    <id>ent://SD_ILS/0/SD_ILS:%d</id>
    <content type="html">Author&amp;#160;Lorde, Audre.&lt;br/&gt;%sFormat:&amp;#160;Books&lt;br/&gt;</content>
  </entry>`, title, href, title, recordID, isbnBlocks.String())
}

func feed(entries ...string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title type="html">Search Results</title>
  ` + strings.Join(entries, "\n  ") + `
</feed>`
}

// tenantDoer serves canned feeds keyed by "host|profile|label" and records
// the requested URLs + headers, goroutine-safe.
type tenantDoer struct {
	mu    sync.Mutex
	pages map[string]string
	urls  []string
	hdrs  []http.Header
}

func (d *tenantDoer) Do(req *http.Request) (*http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.urls = append(d.urls, req.URL.String())
	d.hdrs = append(d.hdrs, req.Header.Clone())
	host := req.URL.Hostname()
	// path: /client/rss/hitlist/<profile>/qu=<label>&...
	parts := strings.SplitN(req.URL.EscapedPath(), "/", 6)
	profile, qseg := "", ""
	if len(parts) >= 6 {
		profile, qseg = parts[4], parts[5]
	}
	label := ""
	if i := strings.Index(qseg, "qu="); i >= 0 {
		rest := qseg[i+3:]
		if a := strings.Index(rest, "&"); a >= 0 {
			rest = rest[:a]
		}
		label = strings.ReplaceAll(rest, "%20", " ")
	}
	key := fmt.Sprintf("%s|%s|%s", host, profile, label)
	if page, ok := d.pages[key]; ok {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(page))}, nil
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(feed()))}, nil
}

func testTerms() []Term {
	return []Term{{
		URI:    "https://homosaurus.org/v5/homoit0000699",
		Labels: map[string]string{"en": "Lesbians"},
		Query:  "Lesbians",
	}}
}

// TestSirsiDynixChain pins the harvest on the live payload shape: the
// Subject-scoped hitlist URL, ISBN extraction from the double-encoded
// content, the ISBN work-match, and the endorsement carrying the record
// link (task 460).
func TestSirsiDynixChain(t *testing.T) {
	doer := &tenantDoer{pages: map[string]string{
		"winca.ent.sirsidynix.net|default|Lesbians": feed(
			entry(1215347, "Zami : a new spelling of my name", "9780895941220"),
			entry(2, "Unrelated", "9789999999999"),
		),
	}}
	works := []ingest.WorkSummary{
		{WorkID: "w1", Title: "Zami", ISBNs: []string{"978-0-89594-122-0"}},
		{WorkID: "w2", Title: "Other", ISBNs: []string{"9780000000000"}},
	}
	e := New([]Tenant{{Host: "winca.ent.sirsidynix.net", Profile: "default"}}, testTerms(), WithClient(doer), WithDelay(0))
	got, err := e.Enrich(context.Background(), works)
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if len(got) != 1 || got[0].WorkID != "w1" || got[0].Confidence != 0.9 {
		t.Fatalf("enrichments = %+v, want one ISBN match on w1", got)
	}
	if got[0].Subjects[0].URI != "https://homosaurus.org/v5/homoit0000699" {
		t.Fatalf("subject = %+v", got[0].Subjects[0])
	}
	end := got[0].Endorsements[0]
	if end.Count != 1 || end.Sources[0] != "winca" {
		t.Fatalf("endorsement = %+v", end)
	}
	a := end.Attributions[0]
	if a.Basis != "isbn" || a.Key != "9780895941220" || !strings.Contains(a.Ref, "detailnonmodal?qu=Lesbians") {
		t.Fatalf("attribution = %+v, want the isbn evidence and the record link", a)
	}
	// The Subject index selector rides the query (pipes percent-encoded).
	if !strings.Contains(doer.urls[0], "rt=false%7C%7C%7CSUBJECT%7C%7C%7CSubject") {
		t.Fatalf("request URL missing the Subject selector: %s", doer.urls[0])
	}
}

// TestSirsiDynixExtractsISBN10AndEAN: an ISBN-10 preceded by the &#160;
// entity (whose own digits would defeat a naive matcher) is extracted, and
// so is a bare EAN-13. The input is the post-XML-decode form extractISBNs
// receives -- one entity layer already peeled by the XML parser.
func TestSirsiDynixExtractsISBN10AndEAN(t *testing.T) {
	isbns := extractISBNs("ISBN&#160;1643756877<br/>ISBN&#160;9781643756905<br/>")
	if strings.Join(isbns, ",") != "1643756877,9781643756905" {
		t.Fatalf("isbns = %v, want both the ISBN-10 and the EAN-13", isbns)
	}
}

// challengeDoer answers with a Cloudflare interstitial instead of a feed.
type challengeDoer struct{}

func (challengeDoer) Do(*http.Request) (*http.Response, error) {
	body := `<!DOCTYPE html><html><head><title>Just a moment...</title></head><body>
<div class="cf-browser-verification"></div></body></html>`
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
}

// TestSirsiDynixChallengeFailsLoudly: a gated tenant's HTML challenge is a
// skipped term with the skip counted, not a silent empty harvest.
func TestSirsiDynixChallengeFailsLoudly(t *testing.T) {
	e := New([]Tenant{{Host: "gated.ent.sirsidynix.net", Profile: "default"}}, testTerms(), WithClient(challengeDoer{}), WithDelay(0))
	got, err := e.Enrich(context.Background(), []ingest.WorkSummary{{WorkID: "w1", ISBNs: []string{"9780895941220"}}})
	if err != nil || len(got) != 0 {
		t.Fatalf("got = %+v, %v; want nothing (challenge detected)", got, err)
	}
	if st := e.RunStats(); st.SkippedBatches != 1 {
		t.Fatalf("skipped = %d, want the challenge counted", st.SkippedBatches)
	}
}

// TestSirsiDynixMultiTenantConsensus: two tenants matching one pair endorse
// a single suggestion; Total = terms x tenants.
func TestSirsiDynixMultiTenantConsensus(t *testing.T) {
	page := feed(entry(7, "Zami", "9780895941220"))
	doer := &tenantDoer{pages: map[string]string{
		"winca.ent.sirsidynix.net|default|Lesbians": page,
		"zzpl.ent.sirsidynix.net|default|Lesbians":  page,
	}}
	works := []ingest.WorkSummary{{WorkID: "w1", ISBNs: []string{"9780895941220"}}}
	e := New([]Tenant{
		{Host: "winca.ent.sirsidynix.net", Profile: "default"},
		{Host: "zzpl.ent.sirsidynix.net", Profile: "default"},
	}, testTerms(), WithClient(doer), WithDelay(0))
	got, err := e.Enrich(context.Background(), works)
	if err != nil || len(got) != 1 {
		t.Fatalf("got = %+v, %v", got, err)
	}
	end := got[0].Endorsements[0]
	if end.Count != 2 || strings.Join(end.Sources, ",") != "winca,zzpl" {
		t.Fatalf("endorsement = %+v, want both tenants", end)
	}
	if st := e.RunStats(); st.Total != 2 || st.Batches != 2 || st.Candidates != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

// TestScrubXML: invalid UTF-8 bytes and XML-illegal control characters are
// dropped; clean text (incl. valid multi-byte UTF-8) is preserved (task 465).
func TestScrubXML(t *testing.T) {
	if got := string(scrubXML([]byte("clean text"))); got != "clean text" {
		t.Fatalf("clean fast-path = %q", got)
	}
	if got := string(scrubXML([]byte("caf\xc3\xa9"))); got != "café" {
		t.Fatalf("valid multibyte = %q, want preserved", got)
	}
	// 0xff is an invalid UTF-8 byte; 0x03 is an XML-illegal C0 control; the
	// tab must survive.
	if got := string(scrubXML([]byte("a\xffb\x03c\tok"))); got != "abc\tok" {
		t.Fatalf("scrubbed = %q, want the bad bytes dropped", got)
	}
}

// TestSirsiDynixParsesDespiteBadBytes: a hitlist whose entry carries an
// invalid UTF-8 byte and a control character still parses and matches,
// rather than losing the whole term to xml.Unmarshal (task 465).
func TestSirsiDynixParsesDespiteBadBytes(t *testing.T) {
	bad := entry(1215347, "Za\xffmi\x03", "9780895941220")
	doer := &tenantDoer{pages: map[string]string{
		"winca.ent.sirsidynix.net|default|Lesbians": feed(bad),
	}}
	works := []ingest.WorkSummary{{WorkID: "w1", ISBNs: []string{"9780895941220"}}}
	e := New([]Tenant{{Host: "winca.ent.sirsidynix.net", Profile: "default"}}, testTerms(),
		WithClient(doer), WithDelay(0))
	got, err := e.Enrich(context.Background(), works)
	if err != nil || len(got) != 1 || got[0].WorkID != "w1" {
		t.Fatalf("got = %+v, %v; want the ISBN matched despite invalid bytes", got, err)
	}
}

// flakyDoer emits a set number of transient failures (a network error, or
// a 503 when failWith is nil) before serving its body, counting attempts.
type flakyDoer struct {
	mu       sync.Mutex
	fails    int
	failWith error
	body     string
	calls    int
}

func (d *flakyDoer) Do(req *http.Request) (*http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	if d.fails > 0 {
		d.fails--
		if d.failWith != nil {
			return nil, d.failWith
		}
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("busy"))}, nil
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(d.body))}, nil
}

// TestSirsiDynixRetriesTransientFailure: a connection refused under load
// backs off and retries rather than losing the whole term (task 463).
func TestSirsiDynixRetriesTransientFailure(t *testing.T) {
	doer := &flakyDoer{fails: 2, failWith: errors.New("dial tcp :443: connect: connection refused"),
		body: feed(entry(1215347, "Zami", "9780895941220"))}
	works := []ingest.WorkSummary{{WorkID: "w1", ISBNs: []string{"9780895941220"}}}
	e := New([]Tenant{{Host: "winca.ent.sirsidynix.net", Profile: "default"}}, testTerms(),
		WithClient(doer), WithDelay(0), WithRetryBase(0))
	got, err := e.Enrich(context.Background(), works)
	if err != nil || len(got) != 1 || got[0].WorkID != "w1" {
		t.Fatalf("got = %+v, %v; want a match after retries", got, err)
	}
	if doer.calls != 3 {
		t.Fatalf("calls = %d, want 3 (2 refused + 1 ok)", doer.calls)
	}
}

// TestSirsiDynixRetries503: a transient 5xx is retried; a whole run of them
// exhausts the budget and counts one skip, not a silent empty harvest.
func TestSirsiDynixRetries503(t *testing.T) {
	doer := &flakyDoer{fails: 99} // always 503
	e := New([]Tenant{{Host: "winca.ent.sirsidynix.net", Profile: "default"}}, testTerms(),
		WithClient(doer), WithDelay(0), WithRetryBase(0), WithMaxRetries(2))
	got, err := e.Enrich(context.Background(), []ingest.WorkSummary{{WorkID: "w1", ISBNs: []string{"9780895941220"}}})
	if err != nil || len(got) != 0 {
		t.Fatalf("got = %+v, %v; want nothing (all attempts 503)", got, err)
	}
	if doer.calls != 3 {
		t.Fatalf("calls = %d, want 3 (1 + 2 retries)", doer.calls)
	}
	if st := e.RunStats(); st.SkippedBatches != 1 {
		t.Fatalf("skipped = %d, want the exhausted term counted", st.SkippedBatches)
	}
}

// TestParseTenants pins the config form: bare subdomain expansion, a custom
// profile, a full host used verbatim, and the key form.
func TestParseTenants(t *testing.T) {
	ts, err := ParseTenants(" winca, otherlib/mobile , cat.example.org/pub ,")
	if err != nil {
		t.Fatalf("ParseTenants: %v", err)
	}
	if len(ts) != 3 {
		t.Fatalf("tenants = %+v", ts)
	}
	if ts[0].Host != "winca.ent.sirsidynix.net" || ts[0].Profile != "default" || ts[0].Key() != "winca" {
		t.Fatalf("tenant0 = %+v", ts[0])
	}
	if ts[1].Host != "otherlib.ent.sirsidynix.net" || ts[1].Profile != "mobile" || ts[1].Key() != "otherlib/mobile" {
		t.Fatalf("tenant1 = %+v", ts[1])
	}
	if ts[2].Host != "cat.example.org" || ts[2].Profile != "pub" {
		t.Fatalf("tenant2 = %+v", ts[2])
	}
	if _, err := ParseTenants("https://winca.ent.sirsidynix.net"); err == nil {
		t.Fatal("a URL-form tenant must refuse")
	}
}
