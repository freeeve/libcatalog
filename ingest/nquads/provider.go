// Package nquads is the generic mapped N-Quads ingest provider:
// it streams a dcterms-shaped .nq export into ingest records driven entirely
// by a declarative TOML mapping -- work-IRI prefix, predicate->field map,
// identifier URN schemes, and source-attestation tiers -- so a deployment
// sideloads an RDF export the way Aspen Discovery sideloads MARC: with a
// profile, not code. Works sharing identifier keys (e.g. ISBNs) with a
// primary feed merge in the shared clustering pipeline; unshared works mint
// as their own. Generalized from the queerbooks-demo collnq provider;
// extended it to the full coll-feed contract (per-format bucket
// grouping, contributions with roles, provisions, formats, topic tags,
// classifications, extras passthrough, non-key identifiers, and standalone
// term descriptions for the vocabulary sideband).
package nquads

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/freeeve/libcat/identity"
	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcodex/rdf"
)

// ProviderName is the registry key and default provenance feed (feed:nquads).
const ProviderName = "nquads"

// dctermsIsReplacedBy is the cluster-merge predicate a coll feed emits when dedupe
// folds one work cluster into another: <retired> isReplacedBy <survivor>.
const dctermsIsReplacedBy = "http://purl.org/dc/terms/isReplacedBy"

// Provider streams a catalog .nq file into ingest records, one per work IRI.
type Provider struct {
	feed          string
	path          string
	m             *Mapping
	idScheme      string
	dropTentative bool
	// merges are the feed's dcterms:isReplacedBy cluster-merges, collected during
	// Records and returned via MergeSeeds so the pipeline seeds the resolver.
	merges []ingest.MergeSeed
}

// MergeSeeds implements ingest.MergeSeeder: the feed's cluster-merge statements as
// resolver provider keys. Populated by Records (call it first).
func (p *Provider) MergeSeeds() []ingest.MergeSeed { return p.merges }

// mergeKey renders a work id (WorkPrefix already stripped) as the resolver provider
// key its records carry: the durable id under SchemeID (matching record.providerID).
func (p *Provider) mergeKey(id string) string {
	return identity.ProviderKey(identity.SchemeID, p.idScheme+":"+id)
}

// New builds the provider from an ingest.Config: Source is the .nq path,
// Params["mapping"] the mapping TOML path, Feed overrides the provenance feed
// name, and Params["tentative"]="drop" drops works whose only attestation is
// a tentative source instead of ingesting them.
func New(cfg ingest.Config) (ingest.Provider, error) {
	if cfg.Source == "" {
		return nil, fmt.Errorf("nquads: Source (.nq path) is required")
	}
	mappingPath := cfg.Params["mapping"]
	if mappingPath == "" {
		return nil, fmt.Errorf("nquads: Params[\"mapping\"] (mapping TOML path) is required")
	}
	m, err := LoadMapping(mappingPath)
	if err != nil {
		return nil, err
	}
	feed := cfg.Feed
	if feed == "" {
		feed = ProviderName
	}
	idScheme := m.IDScheme
	if idScheme == "" {
		idScheme = feed
	}
	drop := false
	switch v := cfg.Params["tentative"]; v {
	case "", "keep":
	case "drop":
		drop = true
	default:
		return nil, fmt.Errorf("nquads: unknown tentative param %q (keep|drop)", v)
	}
	return &Provider{feed: feed, path: cfg.Source, m: m, idScheme: idScheme, dropTentative: drop}, nil
}

// Name is the provenance feed the run writes (feed:<name>).
func (p *Provider) Name() string { return p.feed }

// Role marks this an ingest-role provider.
func (p *Provider) Role() ingest.Role { return ingest.RoleIngest }

// terms is the harvested term-description side of the export: prefLabels per
// language and broader edges on non-work subjects (concept IRIs), shared by
// every record for subject labeling, classification labels, and the
// ancestor-chain standalone terms.
type terms struct {
	labels  map[string]map[string]string // concept IRI -> lang -> label
	broader map[string][]string          // concept IRI -> parent IRIs, statement order, deduped
}

// label is the concept's best single-language label: en, then untagged, then
// any (sorted for determinism).
func (t *terms) label(iri string) string {
	m := t.labels[iri]
	if len(m) == 0 {
		return ""
	}
	if l := m["en"]; l != "" {
		return l
	}
	if l := m[""]; l != "" {
		return l
	}
	langs := make([]string, 0, len(m))
	for lang := range m {
		langs = append(langs, lang)
	}
	sort.Strings(langs)
	return m[langs[0]]
}

// Records parses the export and returns one record per work subject, ordered
// per the mapping's id-order so ingest runs are deterministic. Records
// sharing a mapped "group" object (dcterms:isPartOf-style; self when absent)
// carry a shared grouping id, so the pipeline clusters them into one Work
// with one Instance each.
func (p *Provider) Records(ctx context.Context) ([]ingest.Record, error) {
	f, err := os.Open(p.path)
	if err != nil {
		return nil, fmt.Errorf("nquads: open %s: %w", p.path, err)
	}
	defer f.Close()

	fieldFor := p.m.fieldFor()
	tentative := map[string]bool{}
	for _, iri := range p.m.Sources.Tentative {
		tentative[iri] = true
	}
	works := map[string]*work{}
	var mergeSeeds []ingest.MergeSeed
	tm := &terms{labels: map[string]map[string]string{}, broader: map[string][]string{}}
	get := func(iri string) *work {
		id := strings.TrimPrefix(iri, p.m.WorkPrefix)
		w := works[id]
		if w == nil {
			w = &work{id: id}
			works[id] = w
		}
		return w
	}
	dec := rdf.NewDecoder(f, rdf.NQuads)
	defer dec.Close()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		q, err := dec.DecodeQuad()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("nquads: parse %s: %w", p.path, err)
		}
		field := fieldFor[q.P.Value]
		if q.P.Value == dctermsIsReplacedBy && strings.HasPrefix(q.S.Value, p.m.WorkPrefix) && strings.HasPrefix(q.O.Value, p.m.WorkPrefix) {
			mergeSeeds = append(mergeSeeds, ingest.MergeSeed{
				FromKey: p.mergeKey(strings.TrimPrefix(q.S.Value, p.m.WorkPrefix)),
				ToKey:   p.mergeKey(strings.TrimPrefix(q.O.Value, p.m.WorkPrefix)),
			})
			continue
		}
		if !strings.HasPrefix(q.S.Value, p.m.WorkPrefix) {
			// Term descriptions ride on the concept IRI itself, outside the
			// work prefix: prefLabels per language (untagged = English by
			// the coll-feed convention) and broader edges.
			switch field {
			case "prefLabel":
				lang := q.O.Lang
				if lang == "" {
					lang = "en"
				}
				m := tm.labels[q.S.Value]
				if m == nil {
					m = map[string]string{}
					tm.labels[q.S.Value] = m
				}
				if m[lang] == "" {
					m[lang] = q.O.Value
				}
			case "broader":
				parents := tm.broader[q.S.Value]
				if !slices.Contains(parents, q.O.Value) {
					tm.broader[q.S.Value] = append(parents, q.O.Value)
				}
			}
			continue
		}
		w := get(q.S.Value)
		switch field {
		case "title":
			if w.title == "" {
				w.title = q.O.Value
			}
		case "subtitle":
			if w.subtitle == "" {
				w.subtitle = q.O.Value
			}
		case "summary":
			if w.summary == "" {
				w.summary = q.O.Value
			}
		case "creator":
			w.creators = append(w.creators, q.O.Value)
		case "contributor":
			w.contributors = append(w.contributors, q.O.Value)
		case "publisher":
			if w.publisher == "" {
				w.publisher = q.O.Value
			}
		case "issued":
			if w.issued == "" {
				w.issued = q.O.Value
			}
		case "format":
			if w.format == "" {
				w.format = q.O.Value
			}
		case "group":
			if w.group == "" {
				w.group = strings.TrimPrefix(q.O.Value, p.m.WorkPrefix)
			}
		case "identifier":
			p.mapIdentifier(w, q.O.Value)
		case "subject":
			if !slices.Contains(w.subjectURIs, q.O.Value) {
				w.subjectURIs = append(w.subjectURIs, q.O.Value)
			}
		case "tag":
			if !slices.Contains(w.tags, q.O.Value) {
				w.tags = append(w.tags, q.O.Value)
			}
		case "keyword":
			if !slices.Contains(w.keywords, q.O.Value) {
				w.keywords = append(w.keywords, q.O.Value)
			}
		case "classification":
			if v, ok := strings.CutPrefix(q.O.Value, p.m.Classifications.Prefix); ok && !slices.Contains(w.classCodes, v) {
				w.classCodes = append(w.classCodes, v)
				w.classIRIs = append(w.classIRIs, q.O.Value)
			}
		case "language":
			if code := p.m.language(q.O.Value); !slices.Contains(w.languages, code) {
				w.languages = append(w.languages, code)
			}
		case "source":
			w.sources = append(w.sources, strings.TrimPrefix(q.O.Value, p.m.Sources.Prefix))
			if !tentative[q.O.Value] {
				w.confident = true
			}
		default:
			// Extras ride predicate PREFIXES, not exact predicates: the key
			// is the remainder, the value verbatim.
			if p.m.ExtrasPrefix != "" {
				if key, ok := strings.CutPrefix(q.P.Value, p.m.ExtrasPrefix); ok && key != "" {
					if w.extras == nil {
						w.extras = map[string]string{}
					}
					if w.extras[key] == "" {
						w.extras[key] = q.O.Value
					}
				}
			}
		}
	}

	ids := make([]string, 0, len(works))
	dropped := 0
	for id, w := range works {
		if w.title == "" {
			dropped++
			continue
		}
		if p.dropTentative && !w.confident {
			dropped++
			continue
		}
		if w.group == "" {
			w.group = id // ungrouped records group with themselves
		}
		ids = append(ids, id)
	}
	if p.m.IDOrder == "numeric" {
		sort.Sort(byNumericID(ids))
	} else {
		sort.Strings(ids)
	}
	if dropped > 0 {
		fmt.Fprintf(os.Stderr, "nquads: dropped %d works (untitled or tentative-only with tentative=drop)\n", dropped)
	}
	recs := make([]ingest.Record, 0, len(ids))
	for _, id := range ids {
		recs = append(recs, record{w: works[id], terms: tm, m: p.m, idScheme: p.idScheme})
	}
	p.merges = mergeSeeds
	return recs, nil
}

// mapIdentifier routes one identifier object through the mapping's prefix
// rules: legacy scheme strings keep their keyed behavior, table rules emit
// class/source identifiers and opt into key-ness.
func (p *Provider) mapIdentifier(w *work, obj string) {
	for prefix, rule := range p.m.Identifiers {
		v, ok := strings.CutPrefix(obj, prefix)
		if !ok {
			continue
		}
		switch {
		case rule.Scheme == "isbn" || (rule.Scheme == "" && rule.Key && rule.Class == "Isbn"):
			w.isbns = append(w.isbns, v)
		case rule.Scheme != "":
			// Legacy keyed schemed id: "<scheme>:<value>" as both the
			// resolution key and the emitted identifier value.
			w.idents = append(w.idents, mappedID{class: "Identifier", source: rule.Scheme, value: rule.Scheme + ":" + v, key: rule.Scheme + ":" + v})
		default:
			class := rule.Class
			if class == "" {
				class = "Identifier"
			}
			id := mappedID{class: class, source: rule.Source, value: v}
			if rule.Key {
				id.key = rule.Source + ":" + v
			}
			w.idents = append(w.idents, id)
		}
		return
	}
}

// byNumericID orders work ids numerically when possible (unpadded decimal
// ids), falling back to lexical order for non-numeric ids.
type byNumericID []string

func (s byNumericID) Len() int      { return len(s) }
func (s byNumericID) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s byNumericID) Less(i, j int) bool {
	if len(s[i]) != len(s[j]) && isDigits(s[i]) && isDigits(s[j]) {
		return len(s[i]) < len(s[j])
	}
	return s[i] < s[j]
}

func isDigits(v string) bool {
	for i := 0; i < len(v); i++ {
		if v[i] < '0' || v[i] > '9' {
			return false
		}
	}
	return len(v) > 0
}
