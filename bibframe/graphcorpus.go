package bibframe

import (
	"fmt"
	"sort"

	"github.com/freeeve/libcatalog/storage"
	codexbf "github.com/freeeve/libcodex/bibframe"
	"github.com/freeeve/libcodex/rdf"
)

// WorkGroup is one clustered Work ready to serialize: its minted id, its shared
// Work-level BIBFRAME, and the Instances (each with its own minted id) that
// realize it. It is the direct-BIBFRAME, two-tier-identity unit a native
// provider produces after resolution (ARCHITECTURE §4).
type WorkGroup struct {
	WorkID    string
	Work      codexbf.Work
	Instances []GroupInstance
	// Editorial is the raw N-Quads of the Work's human/authority-owned
	// statements, preserved verbatim from the prior grain so a feed re-ingest
	// never clobbers them (ARCHITECTURE §5). Empty when there are none.
	Editorial []byte
	// Extras are the Work's non-BIBFRAME adopter display fields (e.g. cover,
	// rating, dateRead) that a provider carries through to catalog.json's `extra`
	// object (tasks/026). They are emitted into the feed provenance graph under
	// ExtraPred, so their origin is tracked like every other feed statement. Empty
	// when the provider supplies none, leaving the grain byte-for-byte unchanged.
	Extras map[string]string
}

// ExtraPred is the reserved predicate namespace for adopter "extras": per-Work fields
// that are not BIBFRAME (e.g. cover, rating, dateRead) but a provider wants carried
// through to catalog.json's `extra` object (tasks/026). A key K is emitted as the
// predicate ExtraPred+K on the Work node, in the feed provenance graph; the projector
// harvests the same namespace back into Work.Extra, and the Hugo module forwards it to
// page params (tasks/022).
const ExtraPred = LcatNS + "extra/"

// addWorkExtras attaches a Work's non-BIBFRAME display extras to its graph as
// ExtraPred+<key> literal statements on the Work node, in deterministic key order. It
// is a no-op for an empty map, so a provider that carries no extras yields an unchanged
// graph. Empty keys or values are skipped.
func addWorkExtras(g *rdf.Graph, workID string, extras map[string]string) {
	if len(extras) == 0 {
		return
	}
	w := rdf.NewIRI(WorkIRI(workID))
	keys := make([]string, 0, len(extras))
	for k := range extras {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k == "" || extras[k] == "" {
			continue
		}
		g.Add(w, rdf.NewIRI(ExtraPred+k), rdf.NewLiteral(extras[k], "", ""))
	}
}

// GroupInstance is one Instance of a WorkGroup: its minted id and Instance-level
// BIBFRAME.
type GroupInstance struct {
	InstanceID string
	Instance   codexbf.Instance
}

// GrainFromGraph canonicalizes one BIBFRAME graph into its N-Quads grain, every
// statement tagged with the given provenance graph and RDFC-1.0 canonicalized so
// an unchanged input re-serializes to identical bytes.
func GrainFromGraph(g *rdf.Graph, graph rdf.Term) ([]byte, error) {
	return grainWithEditorial(g, graph, nil)
}

// grainWithEditorial canonicalizes a feed graph together with preserved editorial
// N-Quads into one grain. The feed statements land in graph; the editorial lines
// carry their own 4th column and are merged in as-is, then the whole dataset is
// canonicalized jointly so feed and editorial re-serialize deterministically
// (ARCHITECTURE §5). Editorial statements are IRI-based, so they introduce no
// blank labels that could collide with the feed graph's.
func grainWithEditorial(g *rdf.Graph, graph rdf.Term, editorial []byte) ([]byte, error) {
	nq := g.NQuads(graph)
	if len(editorial) > 0 {
		nq = append(nq, editorial...)
	}
	ds, err := rdf.ParseNQuads(nq)
	if err != nil {
		return nil, fmt.Errorf("parse n-quads: %w", err)
	}
	return ds.Canonical()
}

// BuildWorks writes one canonical N-Quads grain per Work into sink (at
// GrainPath(WorkID)) in the provider's feed graph, plus a bulk catalog.nq. Each
// grain carries the shared Work and its Instances via libcodex's WorkInstances,
// so a clustered Work (multiple editions/formats) is one per-Work file with
// minted, provider-independent ids at both tiers. A WorkGroup's preserved
// Editorial statements are merged back in, so a feed re-ingest is clobber-safe
// (§5). It reports the number of Works (grains) and Instances written.
func BuildWorks(sink storage.Sink, works []WorkGroup, provider string) (BuildStats, error) {
	feed := FeedGraph(provider)
	stats := BuildStats{}

	type built struct {
		id        string
		g         *rdf.Graph
		editorial []byte
	}
	graphs := make([]built, 0, len(works))
	for _, wg := range works {
		wi := codexbf.WorkInstances{Work: wg.Work}
		bases := make([]string, len(wg.Instances))
		for i, gi := range wg.Instances {
			wi.Instances = append(wi.Instances, gi.Instance)
			bases[i] = gi.InstanceID
		}
		g := wi.Graph(wg.WorkID, bases)
		addWorkExtras(g, wg.WorkID, wg.Extras)
		grain, err := grainWithEditorial(g, feed, wg.Editorial)
		if err != nil {
			return stats, fmt.Errorf("grain %s: %w", wg.WorkID, err)
		}
		if err := writeSink(sink, GrainPath(wg.WorkID), grain); err != nil {
			return stats, err
		}
		stats.Grains++
		stats.Records += len(wg.Instances)
		graphs = append(graphs, built{wg.WorkID, g, wg.Editorial})
	}

	sort.Slice(graphs, func(i, j int) bool { return graphs[i].id < graphs[j].id })
	w, err := sink.Create("catalog.nq")
	if err != nil {
		return stats, fmt.Errorf("create catalog.nq: %w", err)
	}
	// One shared encoder across the corpus keeps feed blank-node labels unique, so
	// the bulk file is a valid merge of the grains rather than a collision-prone
	// concatenation (ARCHITECTURE §3). Editorial lines are IRI-based, so they are
	// appended verbatim after each Work's feed lines.
	var enc rdf.Encoder
	for _, b := range graphs {
		if _, err := w.Write(enc.AppendNQuads(nil, b.g, feed)); err != nil {
			w.Close()
			return stats, fmt.Errorf("write catalog.nq: %w", err)
		}
		if len(b.editorial) > 0 {
			if _, err := w.Write(b.editorial); err != nil {
				w.Close()
				return stats, fmt.Errorf("write catalog.nq: %w", err)
			}
		}
	}
	if err := w.Close(); err != nil {
		return stats, fmt.Errorf("close catalog.nq: %w", err)
	}
	return stats, nil
}
