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
	// Subjects are controlled-vocabulary subjects (authority URI + localized labels +
	// skos:broader) a provider derived for the Work, e.g. by promoting genre tags
	// through an authority table (tasks/026). They are emitted into the feed graph as
	// a bf:subject link to the URI plus the authority's prefLabel/broader statements,
	// so the projector resolves them as controlled subjects (tasks/012/015). Empty
	// when the provider supplies none.
	Subjects []AuthoritySubject
}

// AuthoritySubject is one controlled-vocabulary subject a provider asserts for a Work: a
// stable authority URI, its human labels by language tag, and its skos:broader parent
// URIs. It is the graph-emission shape behind ingest.SubjectEnricher (tasks/026).
type AuthoritySubject struct {
	URI     string
	Labels  map[string]string // language tag -> label (e.g. "en", "es")
	Broader []string          // parent authority URIs (skos:broader)
}

// Controlled-subject vocabulary the graph emission uses; mirrors libcodex's stable IRIs
// and the projector's read side.
const (
	predSubject   = "http://id.loc.gov/ontologies/bibframe/subject"
	predPrefLabel = "http://www.w3.org/2004/02/skos/core#prefLabel"
	predBroader   = "http://www.w3.org/2004/02/skos/core#broader"
)

// addControlledSubjects attaches a Work's controlled subjects to its feed graph: a
// bf:subject link from the Work to each authority URI, plus the URI's localized
// skos:prefLabel and skos:broader statements, so the projector resolves them as
// controlled subjects with labels and hierarchy (tasks/012/015). Statements are added in
// deterministic order (by URI, language, parent); a no-op for an empty slice.
func addControlledSubjects(g *rdf.Graph, workID string, subs []AuthoritySubject) {
	if len(subs) == 0 {
		return
	}
	w := rdf.NewIRI(WorkIRI(workID))
	ordered := append([]AuthoritySubject(nil), subs...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].URI < ordered[j].URI })
	seen := map[string]bool{}
	for _, s := range ordered {
		if s.URI == "" || seen[s.URI] {
			continue
		}
		seen[s.URI] = true
		uri := rdf.NewIRI(s.URI)
		g.Add(w, rdf.NewIRI(predSubject), uri)
		langs := make([]string, 0, len(s.Labels))
		for lang := range s.Labels {
			langs = append(langs, lang)
		}
		sort.Strings(langs)
		for _, lang := range langs {
			if label := s.Labels[lang]; label != "" {
				g.Add(uri, rdf.NewIRI(predPrefLabel), rdf.NewLiteral(label, lang, ""))
			}
		}
		parents := append([]string(nil), s.Broader...)
		sort.Strings(parents)
		for _, p := range parents {
			if p != "" {
				g.Add(uri, rdf.NewIRI(predBroader), rdf.NewIRI(p))
			}
		}
	}
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
	// Verbatim carries the record's crosswalk-lossy MARC fields serialized
	// field-exact (EncodeVerbatimField), emitted into the feed graph under
	// PredMARCVerbatim so nothing is silently dropped (tasks/049). Empty for
	// non-MARC providers, leaving the grain unchanged.
	Verbatim []string
}

// GrainFromGraph canonicalizes one BIBFRAME graph into its N-Quads grain, every
// statement tagged with the given provenance graph and RDFC-1.0 canonicalized so
// an unchanged input re-serializes to identical bytes.
func GrainFromGraph(g *rdf.Graph, graph rdf.Term) ([]byte, error) {
	return grainWithEditorial(g, graph, nil)
}

// graph assembles one WorkGroup's feed graph: the shared Work + Instances via
// libcodex's WorkInstances, plus extras, controlled subjects, and each
// Instance's verbatim MARC sidecar.
func (wg WorkGroup) graph() *rdf.Graph {
	wi := codexbf.WorkInstances{Work: wg.Work}
	bases := make([]string, len(wg.Instances))
	for i, gi := range wg.Instances {
		wi.Instances = append(wi.Instances, gi.Instance)
		bases[i] = gi.InstanceID
	}
	g := wi.Graph(wg.WorkID, bases)
	addWorkExtras(g, wg.WorkID, wg.Extras)
	addControlledSubjects(g, wg.WorkID, wg.Subjects)
	for _, gi := range wg.Instances {
		addInstanceVerbatim(g, gi.InstanceID, gi.Verbatim)
	}
	return g
}

// BuildWorkGrain serializes one WorkGroup to its canonical grain bytes -- the
// per-Work unit BuildWorks writes, exposed for store-backed ingest
// (tasks/050), where grains land through blob CAS instead of a Sink.
func BuildWorkGrain(wg WorkGroup, provider string) ([]byte, error) {
	return grainWithEditorial(wg.graph(), FeedGraph(provider), wg.Editorial)
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
		g := wg.graph()
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
