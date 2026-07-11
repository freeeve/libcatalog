package project

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/freeeve/libcodex/rdf"
)

// authorityGraphPrefix names the graphs that carry installed vocabulary
// snapshots. `bibframe.SerializeGrains` merges them into catalog.nq alongside
// the work grains, so a deployment with LCSH installed hands `lcat project` a
// corpus that is 96% authority quads.
const authorityGraphPrefix = "authority:"

// LoadDataset streams the N-Quads catalog at path and returns a dataset holding
// every work, editorial and alias quad, plus only the authority quads the
// projection actually reads.
//
// The projection consumes an authority graph through exactly three indexes:
// buildLabelIndex (skos:prefLabel, rdfs:label), buildBroaderIndex (skos:broader)
// and termSideband's closure over the latter. Nothing reads skos:altLabel,
// skos:narrower, skos:related or the type triples -- together 45% of the quads in
// a corpus with LCSH installed -- and nothing reads a term no Work references,
// which is 99.99% of the rest. mergedView is graph-scoped to feed + editorial, so
// the authority quads never reach it at all.
//
// So: keep the work graphs whole, and from the authority graphs keep only the
// label and broader quads of terms the works reach, transitively. On the demo
// playground that is 98 terms out of ~450,000, and the file is read twice rather
// than parsed once into a dataset that is thrown away.
//
// Read twice, not slurped once. `os.ReadFile` on a 264MB catalog puts 264MB in
// the heap before a quad is parsed, and ParseNQuadsShared then keeps it resident
// for the life of the dataset because its terms alias that buffer.
// Two streaming passes cost one more read of a file the page cache already holds.
func LoadDataset(path string) (*rdf.Dataset, error) {
	wanted, broader, err := scanReferences(path)
	if err != nil {
		return nil, err
	}
	closeOverBroader(wanted, broader)
	return collect(path, wanted)
}

// scanReferences is pass one: every IRI the non-authority quads mention, and the
// authority graphs' skos:broader adjacency.
//
// The seed is deliberately coarse -- every IRI in subject or object position on a
// work, editorial or alias quad, not just the objects of bf:subject. A Work reaches
// a term through several predicates (bf:subject, genre forms, classifications), and
// a seed that enumerated them would silently drop a term the day a new one is
// projected. An over-broad seed costs a few map entries; a narrow one costs a
// subject page its heading.
func scanReferences(path string) (wanted map[string]bool, broader map[string][]string, err error) {
	wanted = map[string]bool{}
	broader = map[string][]string{}
	err = eachQuad(path, func(q rdf.Quad) {
		if isAuthorityGraph(q.G) {
			if q.P.Value == pBroader && q.S.IsIRI() && q.O.IsIRI() {
				broader[q.S.Value] = append(broader[q.S.Value], q.O.Value)
			}
			return
		}
		if q.S.IsIRI() {
			wanted[q.S.Value] = true
		}
		if q.O.IsIRI() {
			wanted[q.O.Value] = true
		}
	})
	return wanted, broader, err
}

// closeOverBroader grows the wanted set to the transitive skos:broader ancestors
// of everything in it, which is the closure termSideband walks. An ancestor an
// enricher described but no Work names still has to carry its label, or the
// browse artifact mints it label-less.
func closeOverBroader(wanted map[string]bool, broader map[string][]string) {
	queue := make([]string, 0, len(wanted))
	for iri := range wanted {
		queue = append(queue, iri)
	}
	for len(queue) > 0 {
		iri := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		for _, parent := range broader[iri] {
			if !wanted[parent] {
				wanted[parent] = true
				queue = append(queue, parent)
			}
		}
	}
}

// collect is pass two: the dataset, with authority quads admitted only when they
// label or place a wanted term.
func collect(path string, wanted map[string]bool) (*rdf.Dataset, error) {
	ds := &rdf.Dataset{}
	err := eachQuad(path, func(q rdf.Quad) {
		if isAuthorityGraph(q.G) && !keepAuthorityQuad(q, wanted) {
			return
		}
		ds.Quads = append(ds.Quads, q)
	})
	if err != nil {
		return nil, err
	}
	return ds, nil
}

// keepAuthorityQuad reports whether an authority quad carries something the
// projection reads about a term it will emit.
func keepAuthorityQuad(q rdf.Quad, wanted map[string]bool) bool {
	if !q.S.IsIRI() || !wanted[q.S.Value] {
		return false
	}
	switch q.P.Value {
	case pPrefLabel, pLabel, pBroader:
		return true
	}
	return false
}

// isAuthorityGraph reports whether a quad's graph term names an installed
// vocabulary snapshot rather than a feed or the editorial layer.
func isAuthorityGraph(g rdf.Term) bool {
	return g.IsIRI() && strings.HasPrefix(g.Value, authorityGraphPrefix)
}

// eachQuad streams the N-Quads file at path, calling fn for every statement.
//
// It reads with Decoder.DecodeQuad rather than the AllQuads iterator, which
// returns silently on *any* error, including a mid-file read error. DecodeQuad
// surfaces those.
//
// A malformed line is one of them, since libcodex v0.26.0 (its filed
// from here). Before that the decoder skipped what it could not read, so a
// truncated catalog.nq projected a smaller catalog and exited 0 -- the failure
// class exists to refuse, and a lie the build would have shipped. Do not
// reach for Decoder.SkipMalformed here: this file is written by an earlier step of
// our own build, and a short read of it is not noise to tolerate.
func eachQuad(path string, fn func(rdf.Quad)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dec := rdf.NewDecoder(f, rdf.NQuads)
	defer dec.Close()
	for {
		q, err := dec.DecodeQuad()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			var se *rdf.SyntaxError
			if errors.As(err, &se) {
				return fmt.Errorf("%s is truncated or corrupt at line %d -- reserialize it (`lcat serialize --dir <grain root>`): %w", path, se.Line, err)
			}
			return err
		}
		fn(q)
	}
}
