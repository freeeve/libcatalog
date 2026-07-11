package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/project"
)

// subsetKeep is the SKOS surface the vocab index reads (mirrors
// backend/vocabsrc keepPredicates); everything else in a concept dump is
// dropped so the subset is minimal.
var subsetKeep = map[string]bool{
	"http://www.w3.org/2004/02/skos/core#prefLabel":  true,
	"http://www.w3.org/2004/02/skos/core#altLabel":   true,
	"http://www.w3.org/2004/02/skos/core#definition": true,
	"http://www.w3.org/2004/02/skos/core#broader":    true,
	"http://www.w3.org/2004/02/skos/core#narrower":   true,
	"http://www.w3.org/2004/02/skos/core#related":    true,
	"http://www.w3.org/2004/02/skos/core#exactMatch": true,
	"http://www.w3.org/2004/02/skos/core#closeMatch": true,
	"http://www.w3.org/2000/01/rdf-schema#label":     true,
}

const subsetPrefLabel = "http://www.w3.org/2004/02/skos/core#prefLabel"

// runVocabSubset harvests the controlled-subject URIs a projected catalog uses,
// fetches each concept's SKOS statements from its authority (id.loc.gov by
// default), and writes a small authority-tree snapshot loadable under
// data/authorities/ as scheme <scheme>. It makes a large vocabulary's used
// slice resolve in the editor (labels, "in local index") without loading the
// whole thing -- sized to the corpus, so a demo instance stays light.
func runVocabSubset(args []string) error {
	fs := flag.NewFlagSet("vocab-subset", flag.ExitOnError)
	catalogJSON := fs.String("catalog", "", "path to catalog.json (from lcat project)")
	scheme := fs.String("scheme", "lcsh", "authority scheme for the output graph (authority:<scheme>)")
	namespace := fs.String("namespace", "http://id.loc.gov/authorities/subjects/",
		"subject URI namespace to harvest")
	out := fs.String("out", "", "output .nq snapshot path")
	concurrency := fs.Int("concurrency", 6, "parallel authority fetches")
	suffix := fs.String("fetch-suffix", ".skos.nt",
		"suffix appended to each concept URI for per-term fetching (id.loc.gov convention; Homosaurus serves plain .nt)")
	dump := fs.String("dump", "", "whole-vocabulary N-Triples/N-Quads dump (file path or URL, e.g. https://homosaurus.org/v5.nt) filtered locally instead of per-term fetching")
	all := fs.Bool("all", false, "with --dump: keep the entire in-namespace vocabulary, not just the catalog's used slice")
	fromCatalog := fs.Bool("from-catalog", false,
		"emit the snapshot purely from catalog.json's subject labels and broader links -- no network; covers exactly the catalog's used slice")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("--out is required")
	}
	if *all && *dump == "" {
		return fmt.Errorf("--all requires --dump (per-term fetching has no concept list beyond the catalog's)")
	}
	if *fromCatalog && *dump != "" {
		return fmt.Errorf("--from-catalog and --dump are exclusive (the catalog IS the dump)")
	}
	if *catalogJSON == "" && !(*dump != "" && *all) {
		return fmt.Errorf("--catalog is required (optional only with --dump --all)")
	}

	if *fromCatalog {
		b, err := os.ReadFile(*catalogJSON)
		if err != nil {
			return err
		}
		var cat project.Catalog
		if err := json.Unmarshal(b, &cat); err != nil {
			return fmt.Errorf("parse catalog.json: %w", err)
		}
		data, terms := subsetFromCatalog(*scheme, *namespace, cat.Works)
		if terms == 0 {
			return fmt.Errorf("no labeled subjects under %q found in %s", *namespace, *catalogJSON)
		}
		if err := os.WriteFile(*out, data, 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote %d terms (from catalog labels, no network) to %s -- drop it under data/authorities/ and load scheme %q\n",
			terms, *out, *scheme)
		return nil
	}

	var uris []string
	if *catalogJSON != "" {
		var err error
		uris, err = catalogSubjectURIs(*catalogJSON, *namespace)
		if err != nil {
			return err
		}
	}
	if len(uris) == 0 && !(*dump != "" && *all) {
		return fmt.Errorf("no subject URIs under %q found in %s", *namespace, *catalogJSON)
	}

	if *dump != "" {
		body, err := readDump(*dump)
		if err != nil {
			return err
		}
		data, terms, err := subsetFromDump(*scheme, *namespace, uris, *all, body)
		if err != nil {
			return err
		}
		if err := os.WriteFile(*out, data, 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote %d terms (dump %s, %d catalog subjects) to %s -- drop it under data/authorities/ and load scheme %q\n",
			terms, *dump, len(uris), *out, *scheme)
		return nil
	}

	fmt.Printf("harvesting %d distinct %s subjects...\n", len(uris), *scheme)
	nts := fetchConcepts(uris, *concurrency, *suffix)
	data, terms := subsetFromNT(*scheme, *namespace, uris, nts)
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %d terms (of %d subjects) to %s -- drop it under data/authorities/ and load scheme %q\n",
		terms, len(uris), *out, *scheme)
	return nil
}

// catalogSubjectURIs reads catalog.json and returns the distinct subject
// authority URIs under namespace, sorted.
func catalogSubjectURIs(path, namespace string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cat project.Catalog
	if err := json.Unmarshal(b, &cat); err != nil {
		return nil, fmt.Errorf("parse catalog.json: %w", err)
	}
	seen := map[string]bool{}
	var uris []string
	for _, w := range cat.Works {
		for _, s := range w.Subjects {
			if strings.HasPrefix(s.ID, namespace) && !seen[s.ID] {
				seen[s.ID] = true
				uris = append(uris, s.ID)
			}
		}
	}
	sort.Strings(uris)
	return uris, nil
}

// fetchConcepts GETs each URI's SKOS N-Triples (<uri><suffix> over https),
// returning a uri->body map. A failed fetch is logged and omitted (the term
// simply stays unresolved, as it was before), never fatal.
func fetchConcepts(uris []string, concurrency int, suffix string) map[string][]byte {
	if concurrency < 1 {
		concurrency = 1
	}
	client := &http.Client{Timeout: 20 * time.Second}
	out := make(map[string][]byte, len(uris))
	var mu sync.Mutex
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, uri := range uris {
		wg.Add(1)
		sem <- struct{}{}
		go func(uri string) {
			defer wg.Done()
			defer func() { <-sem }()
			body, err := fetchConcept(client, uri, suffix)
			if err != nil {
				fmt.Fprintf(os.Stderr, "skip %s: %v\n", uri, err)
				return
			}
			mu.Lock()
			out[uri] = body
			mu.Unlock()
		}(uri)
	}
	wg.Wait()
	return out
}

func fetchConcept(client *http.Client, uri, suffix string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, conceptURL(uri, suffix), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/n-triples")
	req.Header.Set("User-Agent", "libcat lcat vocab-subset")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

// conceptURL is a concept's per-term fetch URL: the URI forced to https plus the
// authority's suffix convention -- id.loc.gov serves <uri>.skos.nt, Homosaurus
// plain <uri>.nt (tasks/130).
func conceptURL(uri, suffix string) string {
	return strings.Replace(uri, "http://", "https://", 1) + suffix
}

// readDump loads a whole-vocabulary dump from a local file or an http(s) URL.
// The Accept header covers authorities that content-negotiate rather than
// publishing a suffixed dump URL.
func readDump(src string) ([]byte, error) {
	if !strings.HasPrefix(src, "http://") && !strings.HasPrefix(src, "https://") {
		return os.ReadFile(src)
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequest(http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/n-triples")
	req.Header.Set("User-Agent", "libcat lcat vocab-subset")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dump %s: HTTP %d", src, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 256<<20))
}

// schemeless strips a leading http(s):// so URIs that differ only by scheme
// compare equal. id.loc.gov serves its SKOS keyed on the canonical http URI, but
// a catalog may carry the https form; the snapshot must use the catalog's form
// or the index (which matches URIs exactly) never resolves it.
func schemeless(u string) string {
	if rest, ok := strings.CutPrefix(u, "https://"); ok {
		return rest
	}
	if rest, ok := strings.CutPrefix(u, "http://"); ok {
		return rest
	}
	return u
}

// subsetFromNT converts the fetched per-concept SKOS N-Triples into one
// authority-tree N-Quads snapshot under graph authority:<scheme>, keeping only
// the predicates the index reads and emitting concepts in the given URI order
// (deterministic output). URIs within `namespace` are re-schemed to match the
// catalog's URI (id.loc.gov's payload is canonical-http; a catalog may be
// https), so the snapshot resolves against the exact-match index. Returns the
// bytes and the count of terms with a prefLabel. Pure -- unit-testable.
func subsetFromNT(scheme, namespace string, order []string, nts map[string][]byte) ([]byte, int) {
	graph := bibframe.AuthorityGraph(scheme)
	nsBare := schemeless(namespace)
	var enc rdf.Encoder
	var out []byte
	terms := 0
	for _, uri := range order {
		body, ok := nts[uri]
		if !ok {
			continue
		}
		// A malformed line now drops the whole concept rather than the one statement
		// (libcodex v0.26.0, tasks/317). That is the right trade for a per-concept
		// body: a concept kept without its prefLabel is a heading with no heading,
		// and this loop already announces what it skipped.
		ds, err := rdf.ParseNQuads(body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip %s: parse: %v\n", uri, err)
			continue
		}
		target := "http"
		if strings.HasPrefix(uri, "https://") {
			target = "https"
		}
		// norm rewrites an in-namespace URI to the catalog's scheme (subjects and
		// broader/narrower targets alike); URIs elsewhere (exactMatch to wikidata,
		// worldcat) are left untouched.
		norm := func(v string) string {
			if b := schemeless(v); strings.HasPrefix(b, nsBare) {
				return target + "://" + b
			}
			return v
		}
		hasLabel := false
		for _, q := range ds.Quads {
			if !q.S.IsIRI() || !subsetKeep[q.P.Value] {
				continue
			}
			if q.P.Value == subsetPrefLabel && schemeless(q.S.Value) == schemeless(uri) {
				hasLabel = true
			}
			o := q.O
			if o.IsIRI() {
				o = rdf.NewIRI(norm(o.Value))
			}
			out = enc.AppendQuad(out, rdf.Quad{S: rdf.NewIRI(norm(q.S.Value)), P: q.P, O: o, G: graph})
		}
		if hasLabel {
			terms++
		}
	}
	return out, terms
}

// subsetFromDump filters a whole-vocabulary N-Triples/N-Quads dump to the
// catalog's concepts -- or, with all, to every in-namespace concept -- emitting
// the same graph-tagged snapshot as per-term harvesting (tasks/130): one request
// for a ~4k-term vocabulary like Homosaurus instead of thousands. URI scheme
// normalization mirrors subsetFromNT: a concept the catalog carries takes the
// catalog's exact URI form (the index matches URIs exactly); any other
// in-namespace URI (a broader parent, an --all concept) takes the namespace
// flag's scheme; URIs elsewhere are untouched. Returns the snapshot and the
// count of prefLabel-bearing concepts kept. The dump is the sole input, so
// keeping nothing is fatal here, unlike a per-concept fetch skip.
//
// Since libcodex v0.26.0 a malformed line refuses the dump outright, naming it
// (tasks/317). That is a smaller safety net than it sounds: a truncated download
// used to parse as a well-formed, shorter vocabulary, and the "kept nothing" guard
// below only fires when the *wrong* dump is fetched, never when the right one
// arrives half-written.
func subsetFromDump(scheme, namespace string, uris []string, all bool, dump []byte) ([]byte, int, error) {
	ds, err := rdf.ParseNQuads(dump)
	if err != nil {
		var se *rdf.SyntaxError
		if errors.As(err, &se) {
			return nil, 0, fmt.Errorf("dump is truncated or corrupt at line %d (a partial download?): %w", se.Line, err)
		}
		return nil, 0, fmt.Errorf("parse dump: %w", err)
	}
	graph := bibframe.AuthorityGraph(scheme)
	nsBare := schemeless(namespace)
	nsScheme := "http"
	if strings.HasPrefix(namespace, "https://") {
		nsScheme = "https"
	}
	catalogForm := make(map[string]string, len(uris))
	for _, u := range uris {
		catalogForm[schemeless(u)] = u
	}
	norm := func(v string) string {
		b := schemeless(v)
		if cu, ok := catalogForm[b]; ok {
			return cu
		}
		if strings.HasPrefix(b, nsBare) {
			return nsScheme + "://" + b
		}
		return v
	}
	keep := func(s rdf.Term) bool {
		if !s.IsIRI() {
			return false
		}
		b := schemeless(s.Value)
		if _, ok := catalogForm[b]; ok {
			return true
		}
		return all && strings.HasPrefix(b, nsBare)
	}
	var enc rdf.Encoder
	var out []byte
	labeled := map[string]bool{}
	for _, q := range ds.Quads {
		if !keep(q.S) || !subsetKeep[q.P.Value] {
			continue
		}
		if q.P.Value == subsetPrefLabel {
			labeled[schemeless(q.S.Value)] = true
		}
		o := q.O
		if o.IsIRI() {
			o = rdf.NewIRI(norm(o.Value))
		}
		out = enc.AppendQuad(out, rdf.Quad{S: rdf.NewIRI(norm(q.S.Value)), P: q.P, O: o, G: graph})
	}
	if len(out) == 0 {
		return nil, 0, fmt.Errorf("dump kept no concepts under %q -- wrong --namespace, or not an N-Triples/N-Quads dump", namespace)
	}
	return out, len(labeled), nil
}

// subsetFromCatalog emits the snapshot purely from catalog.json's own
// subjects[].labels and broader links (tasks/137): the ingest emission
// already wrote every used term's prefLabel into the feed graphs, and the
// projector carried them here, so a corpus-sized index needs no network at
// all -- the route for authorities whose per-term endpoints are flaky or
// retired (FAST). Labels merge across works (first non-empty per language
// wins), broader sets union, and concepts emit in sorted-URI order with
// sorted languages/parents, so the output is deterministic. Broader targets
// keep the catalog's URI form untouched -- they resolve against the same
// exact-match index the subjects do. Returns the snapshot and the count of
// labeled concepts. Pure.
func subsetFromCatalog(scheme, namespace string, works []project.Work) ([]byte, int) {
	type concept struct {
		labels  map[string]string
		broader map[string]bool
	}
	concepts := map[string]*concept{}
	for _, w := range works {
		for _, s := range w.Subjects {
			if !strings.HasPrefix(s.ID, namespace) {
				continue
			}
			c := concepts[s.ID]
			if c == nil {
				c = &concept{labels: map[string]string{}, broader: map[string]bool{}}
				concepts[s.ID] = c
			}
			for lang, label := range s.Labels {
				if label != "" && c.labels[lang] == "" {
					c.labels[lang] = label
				}
			}
			for _, b := range s.Broader {
				if b != "" {
					c.broader[b] = true
				}
			}
		}
	}
	uris := make([]string, 0, len(concepts))
	for uri := range concepts {
		uris = append(uris, uri)
	}
	sort.Strings(uris)
	graph := bibframe.AuthorityGraph(scheme)
	var enc rdf.Encoder
	var out []byte
	terms := 0
	for _, uri := range uris {
		c := concepts[uri]
		subj := rdf.NewIRI(uri)
		langs := make([]string, 0, len(c.labels))
		for lang := range c.labels {
			langs = append(langs, lang)
		}
		sort.Strings(langs)
		for _, lang := range langs {
			out = enc.AppendQuad(out, rdf.Quad{
				S: subj, P: rdf.NewIRI(subsetPrefLabel),
				O: rdf.NewLiteral(c.labels[lang], lang, ""), G: graph,
			})
		}
		parents := make([]string, 0, len(c.broader))
		for b := range c.broader {
			parents = append(parents, b)
		}
		sort.Strings(parents)
		for _, b := range parents {
			out = enc.AppendQuad(out, rdf.Quad{
				S: subj, P: rdf.NewIRI("http://www.w3.org/2004/02/skos/core#broader"),
				O: rdf.NewIRI(b), G: graph,
			})
		}
		if len(langs) > 0 {
			terms++
		}
	}
	return out, terms
}
