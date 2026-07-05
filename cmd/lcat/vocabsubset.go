package main

import (
	"encoding/json"
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

	"github.com/freeeve/libcatalog/bibframe"
	"github.com/freeeve/libcatalog/project"
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
		"subject URI namespace to harvest; the concept is fetched from <uri>.skos.nt (https)")
	out := fs.String("out", "", "output .nq snapshot path")
	concurrency := fs.Int("concurrency", 6, "parallel authority fetches")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *catalogJSON == "" || *out == "" {
		return fmt.Errorf("--catalog and --out are required")
	}

	uris, err := catalogSubjectURIs(*catalogJSON, *namespace)
	if err != nil {
		return err
	}
	if len(uris) == 0 {
		return fmt.Errorf("no subject URIs under %q found in %s", *namespace, *catalogJSON)
	}
	fmt.Printf("harvesting %d distinct %s subjects...\n", len(uris), *scheme)

	nts := fetchConcepts(uris, *concurrency)
	data, terms := subsetFromNT(*scheme, uris, nts)
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

// fetchConcepts GETs each URI's SKOS N-Triples (<uri>.skos.nt over https),
// returning a uri->body map. A failed fetch is logged and omitted (the term
// simply stays unresolved, as it was before), never fatal.
func fetchConcepts(uris []string, concurrency int) map[string][]byte {
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
			body, err := fetchConcept(client, uri)
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

func fetchConcept(client *http.Client, uri string) ([]byte, error) {
	url := strings.Replace(uri, "http://", "https://", 1) + ".skos.nt"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/n-triples")
	req.Header.Set("User-Agent", "libcatalog lcat vocab-subset")
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

// subsetFromNT converts the fetched per-concept SKOS N-Triples into one
// authority-tree N-Quads snapshot under graph authority:<scheme>, keeping only
// the predicates the index reads and emitting concepts in the given URI order
// (deterministic output). Returns the bytes and the count of terms with a
// prefLabel. Pure -- unit-testable without the network.
func subsetFromNT(scheme string, order []string, nts map[string][]byte) ([]byte, int) {
	graph := bibframe.AuthorityGraph(scheme)
	var enc rdf.Encoder
	var out []byte
	terms := 0
	for _, uri := range order {
		body, ok := nts[uri]
		if !ok {
			continue
		}
		ds, err := rdf.ParseNQuads(body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip %s: parse: %v\n", uri, err)
			continue
		}
		hasLabel := false
		for _, q := range ds.Quads {
			if !q.S.IsIRI() || !subsetKeep[q.P.Value] {
				continue
			}
			if q.P.Value == subsetPrefLabel && q.S.Value == uri {
				hasLabel = true
			}
			out = enc.AppendQuad(out, rdf.Quad{S: q.S, P: q.P, O: q.O, G: graph})
		}
		if hasLabel {
			terms++
		}
	}
	return out, terms
}
