package bibliocommons

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/freeeve/libcat/ingest"
)

// stubDoer serves canned RSS bodies keyed by "q|page" and records every
// request URL.
type stubDoer struct {
	pages map[string]string
	urls  []string
	fail  map[string]int // "q|page" -> status code
}

func (d *stubDoer) Do(req *http.Request) (*http.Response, error) {
	d.urls = append(d.urls, req.URL.String())
	q := req.URL.Query()
	key := q.Get("q") + "|" + q.Get("page")
	if code, ok := d.fail[key]; ok {
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	body, ok := d.pages[key]
	if !ok {
		body = rssPage() // empty feed
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader([]byte(body)))}, nil
}

// rssPage renders a feed of items in the live payload's shape -- labeled
// HTML fields inside the description, a blurb full of free-text "by", and
// the Syndetics jacket img -- each item given as [title, author, isbn, oclc].
func rssPage(items ...[4]string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>search</title>`)
	for i, it := range items {
		fmt.Fprintf(&b, `<item><title>%s</title><link>https://ccslib.bibliocommons.com/item/show/%d123456_slug</link>`, it[0], i)
		fmt.Fprintf(&b, `<description><![CDATA[<b>Title:</b> %s<br/>
<b>Author:</b> <a href="https://ccslib.bibliocommons.com/search?q=x&amp;t=author" target="_parent">%s</a><br/>
<b>Format:</b> Book<br/>
<b>Description:</b> <p>&quot;A story influenced by history.&quot;-- Provided by publisher.</p><br/>
<div class='jacketCoverDiv'><img class="jacketCover medium" src="https://secure.syndetics.com/index.aspx?isbn=%s/SC.GIF&amp;client=coopcoms&amp;type=xw12&amp;oclc=%s" /></div>]]></description></item>`,
			it[0], it[1], it[2], it[3])
	}
	b.WriteString(`</channel></rss>`)
	return b.String()
}

func testTerms() []Term {
	return []Term{
		{URI: "https://homosaurus.org/v4/homoit0000101", Labels: map[string]string{"en": "Nonbinary people"}, Query: "Nonbinary people"},
		{URI: "https://homosaurus.org/v4/homoit0000202", Labels: map[string]string{"en": "Lesbians"}, Query: "Lesbians"},
	}
}

func testWorks() []ingest.WorkSummary {
	return []ingest.WorkSummary{
		{WorkID: "w1", Title: "Gender Queer", Contributors: []string{"Maia Kobabe"}, ISBNs: []string{"978-1-5493-0400-2"}},
		{WorkID: "w2", Title: "Fun Home: A Family Tragicomic", Contributors: []string{"Bechdel, Alison"}},
		{WorkID: "w3", Title: "Fun Home", Contributors: []string{"Somebody Else"}},
		{WorkID: "w4", Title: "Stone Butch Blues", Contributors: []string{"Leslie Feinberg"}, ISBNs: []string{"9781555838539"},
			Subjects: []string{"https://homosaurus.org/v4/homoit0000202"}},
	}
}

// TestEnrichMatchTiers exercises the whole contract on one harvest: ISBN
// matches suggest at 0.9, title+author fallback at 0.75, a title match with
// a disagreeing author is dropped, and a work already carrying the driver
// term gets nothing (task 434).
func TestEnrichMatchTiers(t *testing.T) {
	doer := &stubDoer{pages: map[string]string{
		"Nonbinary people|1": rssPage(
			[4]string{"Gender Queer: A Memoir", "Maia Kobabe", "9781549304002", "1090707801"},
		),
		"Lesbians|1": rssPage(
			[4]string{"Fun Home", "Alison Bechdel", "", "62290938"},
			[4]string{"Stone Butch Blues", "Leslie Feinberg", "9781555838539", "27897047"},
		),
	}}
	e := New("ccslib", testTerms(), WithClient(doer), WithDelay(0))
	got, err := e.Enrich(context.Background(), testWorks())
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}

	byWork := map[string]ingest.Enrichment{}
	for _, enr := range got {
		if _, dup := byWork[enr.WorkID]; dup {
			t.Fatalf("work %s appears in two enrichments (same tier should merge)", enr.WorkID)
		}
		byWork[enr.WorkID] = enr
	}
	w1, ok := byWork["w1"]
	if !ok || w1.Confidence != 0.9 {
		t.Fatalf("w1: want ISBN-tier (0.9) enrichment, got %+v", byWork["w1"])
	}
	if len(w1.Subjects) != 1 || w1.Subjects[0].URI != "https://homosaurus.org/v4/homoit0000101" {
		t.Fatalf("w1 subjects: %+v", w1.Subjects)
	}
	if w1.Subjects[0].Labels["en"] != "Nonbinary people" {
		t.Fatalf("w1 suggestion lost the driver term labels: %+v", w1.Subjects[0])
	}
	w2, ok := byWork["w2"]
	if !ok || w2.Confidence != 0.75 {
		t.Fatalf("w2: want title+author-tier (0.75) enrichment, got %+v", byWork["w2"])
	}
	if len(w2.Subjects) != 1 || w2.Subjects[0].URI != "https://homosaurus.org/v4/homoit0000202" {
		t.Fatalf("w2 subjects: %+v", w2.Subjects)
	}
	if _, hit := byWork["w3"]; hit {
		t.Fatalf("w3 matched on title despite a different author: %+v", byWork["w3"])
	}
	if _, hit := byWork["w4"]; hit {
		t.Fatalf("w4 already carries the term; got %+v", byWork["w4"])
	}

	for _, u := range doer.urls {
		if !strings.HasPrefix(u, "https://ccslib.bibliocommons.com/search/rss?") {
			t.Fatalf("request went to the wrong surface: %s", u)
		}
		if !strings.Contains(u, "t=subject") {
			t.Fatalf("request is not a subject search: %s", u)
		}
	}
}

// TestEnrichPaginationAndCap fetches until a short page and stops at the
// page cap on terms that never run dry (task 434).
func TestEnrichPaginationAndCap(t *testing.T) {
	full := rssPage([4]string{"A", "X Y", "", ""}, [4]string{"B", "X Y", "", ""})
	short := rssPage([4]string{"C", "X Y", "", ""})
	doer := &stubDoer{pages: map[string]string{
		"Nonbinary people|1": full, "Nonbinary people|2": short,
		"Lesbians|1": full, "Lesbians|2": full, "Lesbians|3": full, "Lesbians|4": full,
	}}
	e := New("ccslib", testTerms(), WithClient(doer), WithDelay(0), WithMaxPages(3))
	e.displayQuantity = 2
	if _, err := e.Enrich(context.Background(), nil); err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	var nb, lb int
	for _, u := range doer.urls {
		if strings.Contains(u, "Nonbinary") {
			nb++
		} else {
			lb++
		}
	}
	if nb != 2 {
		t.Fatalf("short page did not stop the term: %d requests", nb)
	}
	if lb != 3 {
		t.Fatalf("page cap not honored: %d requests", lb)
	}
	// Stats speak in terms, not pages: Total is the driver term count and
	// Batches the terms processed, so Batches/Total is a progress fraction
	// (task 439).
	if st := e.RunStats(); st.Batches != 2 || st.Total != 2 {
		t.Fatalf("stats = %d/%d, want 2/2 terms", st.Batches, st.Total)
	}
}

// TestEnrichHarvestCachedAcrossRuns re-enriches without touching the network
// inside the TTL (task 434).
func TestEnrichHarvestCachedAcrossRuns(t *testing.T) {
	doer := &stubDoer{pages: map[string]string{
		"Nonbinary people|1": rssPage([4]string{"Gender Queer: A Memoir", "Maia Kobabe", "9781549304002", ""}),
	}}
	e := New("ccslib", testTerms(), WithClient(doer), WithDelay(0))
	if _, err := e.Enrich(context.Background(), testWorks()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	fetched := len(doer.urls)
	got, err := e.Enrich(context.Background(), testWorks())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(doer.urls) != fetched {
		t.Fatalf("second run refetched: %d -> %d requests", fetched, len(doer.urls))
	}
	if len(got) == 0 {
		t.Fatal("cached harvest produced no matches")
	}
}

// TestEnrichTermFailureIsSkippedNotFatal keeps harvesting the other terms
// when one term's fetch fails, counting the skip (task 434).
func TestEnrichTermFailureIsSkippedNotFatal(t *testing.T) {
	doer := &stubDoer{
		pages: map[string]string{
			"Lesbians|1": rssPage([4]string{"Stone Butch Blues", "Leslie Feinberg", "9781555838539", ""}),
		},
		fail: map[string]int{"Nonbinary people|1": http.StatusInternalServerError},
	}
	e := New("ccslib", testTerms(), WithClient(doer), WithDelay(0))
	works := []ingest.WorkSummary{
		{WorkID: "w9", Title: "Stone Butch Blues", Contributors: []string{"Leslie Feinberg"}, ISBNs: []string{"9781555838539"}},
	}
	got, err := e.Enrich(context.Background(), works)
	if err != nil {
		t.Fatalf("one bad term must not fail the run: %v", err)
	}
	if len(got) != 1 || got[0].WorkID != "w9" {
		t.Fatalf("surviving term's matches lost: %+v", got)
	}
	if st := e.RunStats(); st.SkippedBatches != 1 {
		t.Fatalf("skipped batches = %d, want 1", st.SkippedBatches)
	}
	// A failed term still counts as processed, so the fraction reaches
	// Total even on a partial harvest (task 439).
	if st := e.RunStats(); st.Batches != 2 || st.Total != 2 {
		t.Fatalf("stats = %d/%d, want 2/2 terms", st.Batches, st.Total)
	}
}

// TestParseFeedFieldExtraction covers the description parsing against the
// live payload's shape (probed on ccslib 2026-07-12): the labeled Author
// field is read -- never the blurb's free-text "by" phrases -- the Syndetics
// URL yields identifiers in both hostname forms and its ISBN normalizes,
// entity-encoded descriptions decode like CDATA ones, and an item with no
// cover URL still parses (task 434).
func TestParseFeedFieldExtraction(t *testing.T) {
	body := `<?xml version="1.0"?><rss version="2.0"><channel>
<item><title> Field Guide for the Formerly Villainous </title><link>x</link>
<description>&lt;b&gt;Title:&lt;/b&gt; Field Guide for the Formerly Villainous&lt;br/&gt;
&lt;b&gt;Author:&lt;/b&gt; &lt;a href=&quot;https://ccslib.bibliocommons.com/search?q=x&amp;amp;t=author&quot; target=&quot;_parent&quot;&gt;England, Autumn K.&lt;/a&gt;&lt;br/&gt;
&lt;b&gt;Description:&lt;/b&gt; &lt;p&gt;&amp;quot;A tale inspired by legend.&amp;quot;-- Provided by publisher.&lt;/p&gt;&lt;br/&gt;
&lt;div class='jacketCoverDiv'&gt;&lt;img src=&quot;https://s.syndetics.com/index.aspx?isbn=978-1-4642-8063-4/SC.GIF&amp;amp;client=coopcoms&amp;amp;oclc=on1601367632&quot; /&gt;&lt;/div&gt;</description></item>
<item><title>No Cover</title><link>x</link><description><![CDATA[<b>Author:</b> Anon<br/>]]></description></item>
</channel></rss>`
	items, err := parseFeed([]byte(body))
	if err != nil {
		t.Fatalf("parseFeed: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items: %d", len(items))
	}
	if items[0].title != "Field Guide for the Formerly Villainous" || items[0].author != "England, Autumn K" {
		t.Fatalf("item 0 (trailing punctuation trims off the author): %+v", items[0])
	}
	if items[0].isbn != "9781464280634" || items[0].oclc != "on1601367632" {
		t.Fatalf("item 0 identifiers: %+v", items[0])
	}
	if items[1].isbn != "" || items[1].author != "Anon" {
		t.Fatalf("item 1: %+v", items[1])
	}
}

// TestAuthorsAgree pins the order-blind token check both ways, plus the
// cataloging apparatus a peer string carries: parenthetical qualifiers and
// life dates must not block a plain-name agreement.
func TestAuthorsAgree(t *testing.T) {
	if !authorsAgree("Bechdel, Alison", []string{"Alison Bechdel"}) {
		t.Fatal("inverted form must agree")
	}
	if !authorsAgree("Allen, Samantha (Journalist)", []string{"Samantha Allen"}) {
		t.Fatal("parenthetical qualifier must not block agreement")
	}
	if !authorsAgree("Woolf, Virginia, 1882-1941", []string{"Virginia Woolf"}) {
		t.Fatal("life dates must not block agreement")
	}
	if authorsAgree("Alison Bechdel", []string{"Somebody Else"}) {
		t.Fatal("different names must not agree")
	}
	if authorsAgree("", []string{"Anyone"}) {
		t.Fatal("empty peer author must not agree")
	}
}
