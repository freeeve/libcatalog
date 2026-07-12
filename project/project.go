// Package project turns the canonical BIBFRAME graph into the catalog's derived
// data: the JSON a static site (the Hugo module's content adapter) and the search
// index consume (ARCHITECTURE §7). It is a read-only projection -- the graph
// stays the source of truth -- and it flattens each clustered Work, with its
// Instances and the union of its feed and editorial statements, into one record.
package project

import (
	"sort"
	"strings"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcodex/rdf"
)

// BIBFRAME / RDF vocabulary the projection reads. These mirror libcodex's stable
// output IRIs; kept local so the projector depends only on the rdf toolkit.
const (
	bfNS              = "http://id.loc.gov/ontologies/bibframe/"
	bflcNS            = "http://id.loc.gov/ontologies/bflc/"
	rdfNS             = "http://www.w3.org/1999/02/22-rdf-syntax-ns#"
	rdfsNS            = "http://www.w3.org/2000/01/rdf-schema#"
	skosNS            = "http://www.w3.org/2004/02/skos/core#"
	classWork         = bfNS + "Work"
	pTitle            = bfNS + "title"
	pMainTitle        = bfNS + "mainTitle"
	pSubtitle         = bfNS + "subtitle"
	pContribution     = bfNS + "contribution"
	pAgent            = bfNS + "agent"
	pRole             = bfNS + "role"
	pSubject          = bfNS + "subject"
	pSummary          = bfNS + "summary"
	pLanguage         = bfNS + "language"
	pClassif          = bfNS + "classification"
	pClassPortion     = bfNS + "classificationPortion"
	pHasInstance      = bfNS + "hasInstance"
	pIdentifiedBy     = bfNS + "identifiedBy"
	pHasItem          = bfNS + "hasItem"
	pShelfMark        = bfNS + "shelfMark"
	pPhysicalLocation = bfNS + "physicalLocation"
	pMedia            = bfNS + "media"
	pCarrier          = bfNS + "carrier"
	pSource           = bfNS + "source"
	pSeriesStatement  = bfNS + "seriesStatement"
	pSeriesEnum       = bfNS + "seriesEnumeration"
	pHasPart          = bfNS + "hasPart"
	pPartOf           = bfNS + "partOf"
	classIsbn         = bfNS + "Isbn"
	// Publication statement as libcodex emits it (MARC 260/264): one
	// bf:provisionActivity per field on the Instance, typed bf:Publication,
	// carrying the transcribed place/agent/date as bflc:simple* literals. Only
	// the Publication provision is projected -- Distribution/Manufacture nodes
	// carry the same shape but are not what a reader means by "published".
	pProvisionActivity = bfNS + "provisionActivity"
	classPublication   = bfNS + "Publication"
	pSimplePlace       = bflcNS + "simplePlace"
	pSimpleAgent       = bflcNS + "simpleAgent"
	pSimpleDate        = bflcNS + "simpleDate"
	pDate              = bfNS + "date"
	// Series as libcodex >= v0.25.0 emits it: one bf:relation per 490
	// on the Work, discriminated by its bf:relationship IRI, carrying the
	// enumeration and pointing at a bf:Series through bf:associatedResource.
	pRelation           = bfNS + "relation"
	pRelationship       = bfNS + "relationship"
	pAssociatedResource = bfNS + "associatedResource"
	pStatus             = bfNS + "status"
	classIssn           = bfNS + "Issn"
	classHub            = bfNS + "Hub"
	seriesRelationIRI   = "http://id.loc.gov/vocabulary/relationship/series"
	// subseries is the 762 subseries linking entry's relationship; its
	// associated resource is a Hub-typed series like the 8xx entries'.
	subseriesRelationIRI = "http://id.loc.gov/vocabulary/relationship/subseries"
	// mstatus/tr is "traced": the cataloger gave the series an added entry
	// (490 ind1=1). mstatus/t ("transcribed") is on every series and says nothing.
	statusTracedIRI = "http://id.loc.gov/vocabulary/mstatus/tr"
	pLabel          = rdfsNS + "label"
	pPrefLabel      = skosNS + "prefLabel"
	pBroader        = skosNS + "broader"
	pValue          = rdfNS + "value"
	primaryContr    = bflcNS + "PrimaryContribution"
	// pTag is libcat's blank-free folksonomy-tag predicate
	// (bibframe.PredTag): editorial-class graphs cannot carry the feed's
	// labeled-blank-node tag shape, so approved community tags arrive as
	// plain literals on this predicate instead.
	pTag = "https://github.com/freeeve/libcat/ns#tag"
)

// SchemaVersion is the catalog.json / facets.json / redirects.json schema version.
// The Hugo module and search-index builder read it to detect a projector/consumer
// mismatch. v2 added the per-Instance identifier scheme (ProviderID.Source) for the
// availability adapter. v3 split controlled subjects (authority
// URIs + resolved labels) from uncontrolled feed tags. v4 added
// per-Instance format (from the Instance's RDA media type) and the Work-level
// formats facet, so a clustered mixed-format Work exposes each format.
// v5 added subject skos:broader parents (Subject.Broader / SubjectFacet.Broader) so
// consumers render vocabulary hierarchy without re-reading the graph.
// v6 added the holdings signal (Instance.Held / Work.Held): physical items or a
// live-availability identifier whose feed still lists the Work.
// v7 added Work.Summary from bf:summary -- the description/abstract as first-class
// bibliographic data rather than an adopter extra.
// v8 added Subject.Scheme / SubjectFacet.Scheme -- the short vocabulary code
// derived from the authority-URI namespace -- so a multi-vocabulary corpus can
// facet and mint term pages per scheme instead of colliding same-label terms
// from different vocabularies.
// v9 made classifications {value, label} objects (Work.Classifications /
// Facets.Classifications): value stays the scheme code (what MARC 084 $a
// carries), label is the human text riding the classification node's
// rdfs:label -- the display-only channel -- so a facet can show "Fiction /
// Romance / Contemporary" while exports keep FIC027000.
// The code stays verbatim here on purpose. A Dewey number's prime mark (082 $a
// "813/.6") is data a MARC export must round-trip, so it is not the projector's
// to strip -- but a slash cannot survive being a URL path segment, so the Hugo
// module keys the classification taxonomy by the code's slug and shows the code
// . The URL needed the slug, not the record.
// v10 added Catalog.Terms -- the vocabulary sideband: every referenced subject
// term plus its transitive skos:broader ancestors, with whatever labels and
// broader edges the graph carries -- so a consumer can label hierarchy nodes
// no Work carries directly instead of minting them label-less.
// v11 added Work.Relations -- the editorial whole/part links (hasPart/partOf
// as {id, title}, restricted to works present in this projection) -- and the
// Instance series statement/enumeration (bf:seriesStatement 490$a,
// bf:seriesEnumeration 490$v), so the site cross-links parts and shows
// series lines.
// v12 moved series from the Instance to the Work and made them objects:
// Work.Series []{title, enumeration, issn, traced} replaces Instance.Series
// []string + Instance.SeriesEnumeration. libcodex v0.25.0 hangs one bf:relation
// per 490 on the Work, so each enumeration belongs to its own series instead of
// being paired by list position -- a pairing an RDF graph, being a set, could not
// preserve. 490$x (ISSN) and ind1=1 (traced) are carried for the first time
// .
const SchemaVersion = 12

// Catalog is the projected corpus: one record per Work, sorted by id.
type Catalog struct {
	Version int    `json:"version"`
	Works   []Work `json:"works"`
	// Terms is the vocabulary sideband: one entry per referenced
	// subject term or transitive skos:broader ancestor that the graph
	// describes (labels and/or broader edges; bare URIs with nothing to say
	// are skipped). Sorted by ID.
	Terms []Term `json:"terms,omitempty"`
}

// Term is one controlled-vocabulary concept the catalog references -- a
// Work's subject or one of its skos:broader ancestors -- with the labels and
// broader edges resolved from the graph. The sideband exists for hierarchy
// nodes no Work carries directly: the browse-artifact builder unions subtree
// postings into ancestors (search.BuildBrowse), and without this it can only
// mint them label-less.
type Term struct {
	ID      string            `json:"id"`
	Labels  map[string]string `json:"labels,omitempty"`
	Broader []string          `json:"broader,omitempty"`
	Scheme  string            `json:"scheme,omitempty"`
}

// Work is the discovery unit as the static site sees it -- the display and facet
// fields of a bf:Work plus its Instances (the borrowable editions/formats).
type Work struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	// Summary is the Work's description/abstract (bf:summary), first label wins
	// when a record carries several.
	Summary         string           `json:"summary,omitempty"`
	Contributors    []Contributor    `json:"contributors,omitempty"`
	Subjects        []Subject        `json:"subjects,omitempty"`
	Tags            []string         `json:"tags,omitempty"`
	Languages       []string         `json:"languages,omitempty"`
	Classifications []Classification `json:"classifications,omitempty"`
	// Formats is the union of the Work's Instances' formats (e.g. ebook, audiobook),
	// so a clustered mixed-format Work is faceted under each format it offers.
	Formats   []string   `json:"formats,omitempty"`
	Instances []Instance `json:"instances,omitempty"`
	// Held is true when any Instance is held: physical items, or
	// a live-availability identifier whose feed still lists the Work. Whether
	// unheld Works are hidden or merely badged is the importing site's call.
	Held bool `json:"held,omitempty"`
	// Extra holds the Work's non-BIBFRAME adopter display fields (e.g. cover, rating,
	// dateRead) a provider carried through the feed graph under bibframe.ExtraPred
	//. The Hugo module forwards it to page params. Omitted (nil)
	// when the corpus carries none, so a catalog without extras is unchanged.
	Extra map[string]string `json:"extra,omitempty"`
	// Relations are the Work's editorial whole/part links,
	// restricted to works present in this projection -- a link to a
	// suppressed, tombstoned, or foreign work is omitted rather than
	// rendered as a dead cross-link.
	Relations *Relations `json:"relations,omitempty"`
	// Series are the Work's series memberships, one per 490.
	// Work-level, because that is where libcodex >= v0.25.0 hangs them: a
	// bf:relation whose bf:relationship is .../relationship/series. They used
	// to be flat literals on the Instance, which paired a statement to its
	// enumeration by list position -- and an RDF graph is a set, so two 490s
	// sharing a $v collapsed to one triple and the pairing died.
	Series []Series `json:"series,omitempty"`
}

// Series is one series membership: the transcribed statement, this Work's place
// in it, and the series ISSN.
//
// Enumeration belongs to the *relation*, not to the series, which is why it can
// finally be per-series rather than one-per-Instance: "bk. 2 of Firebrand fiction"
// is a fact about this Work's membership, and the same series numbers a different
// Work differently.
type Series struct {
	// Title is the transcribed series statement (490$a, bf:mainTitle on the
	// bf:Series).
	Title string `json:"title"`
	// Enumeration is this Work's volume/part within the series (490$v).
	Enumeration string `json:"enumeration,omitempty"`
	// ISSN is the series ISSN (490$x), which the flat mapping silently dropped.
	ISSN string `json:"issn,omitempty"`
	// Traced marks a series the cataloger traced (490 ind1=1), i.e. one with a
	// corresponding added entry. Carried as bf:status mstatus/tr.
	Traced bool `json:"traced,omitempty"`
}

// Relations are a Work's whole/part cross-links (schema v11).
type Relations struct {
	HasPart []RelatedWork `json:"hasPart,omitempty"`
	PartOf  []RelatedWork `json:"partOf,omitempty"`
}

// RelatedWork is one linked work: its catalog id and display title.
type RelatedWork struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

// Contributor is an agent's display name and role.
type Contributor struct {
	Name string `json:"name"`
	Role string `json:"role,omitempty"`
}

// Classification is one classification of a Work: the scheme code (bf:
// classificationPortion -- what MARC 084 $a carries) plus the optional human
// label riding the classification node's rdfs:label. Facets and
// term pages key on Value; display prefers Label and falls back to Value, so
// a corpus without labels renders exactly as before.
type Classification struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
}

// Subject is a controlled-vocabulary subject: a stable authority URI plus the
// human labels resolved from the authority's skos:prefLabel / rdfs:label statements
// in the graph, keyed by language tag (e.g. "en", "es"; "" for an untagged label).
// Links and facets key on ID; display uses Labels, falling back to ID when the
// authority provides none. Distinct from an uncontrolled feed Tag.
//
// Broader holds the authority URIs of this term's skos:broader parents (sorted,
// deduped), so a consumer can render vocabulary hierarchy (breadcrumb trails,
// broader/narrower drill-down) without re-reading the graph. It is
// id-only: a parent's label resolves from the parent's own Subject/authority record.
type Subject struct {
	ID      string            `json:"id"`
	Labels  map[string]string `json:"labels,omitempty"`
	Broader []string          `json:"broader,omitempty"`
	// Scheme is the short vocabulary code derived from the URI's namespace
	// (SchemeForURI) -- "homosaurus", "fast", "lcsh", "local";
	// empty for an unrecognized authority.
	Scheme string `json:"scheme,omitempty"`
}

// SchemePrefix maps an authority-URI namespace prefix to its short
// vocabulary code.
type SchemePrefix struct{ Prefix, Scheme string }

// SubjectSchemePrefixes is the namespace -> scheme table SchemeForURI
// consults, first match wins. `lcat project --subject-scheme`
// prepends deployment-specific entries, so a custom authority (or a
// different code for a known one) overrides these defaults.
var SubjectSchemePrefixes = []SchemePrefix{
	{"https://homosaurus.org/", "homosaurus"},
	{"http://homosaurus.org/", "homosaurus"},
	{"http://id.worldcat.org/fast/", "fast"},
	{"https://id.worldcat.org/fast/", "fast"},
	{"http://id.loc.gov/authorities/subjects/", "lcsh"},
	{"https://id.loc.gov/authorities/subjects/", "lcsh"},
	{"http://id.loc.gov/authorities/childrensSubjects/", "lcshac"},
	{"https://id.loc.gov/authorities/childrensSubjects/", "lcshac"},
	{bibframe.LocalAuthorityNS, "local"},
}

// SchemeForURI returns the short vocabulary code for an authority URI, or ""
// when the namespace is not in SubjectSchemePrefixes.
func SchemeForURI(uri string) string {
	for _, s := range SubjectSchemePrefixes {
		if strings.HasPrefix(uri, s.Prefix) {
			return s.Scheme
		}
	}
	return ""
}

// Instance is one edition/format: its id, format (from its RDA media type), ISBNs,
// and the scheme-tagged provider ids the runtime availability adapter keys on.
type Instance struct {
	ID          string       `json:"id"`
	Format      string       `json:"format,omitempty"`
	ISBNs       []string     `json:"isbns,omitempty"`
	ProviderIDs []ProviderID `json:"providerIds,omitempty"`
	// Items are the Instance's physical holdings: call number,
	// shelving location, barcode, note -- never circulation state, which
	// stays live-only (ARCHITECTURE §5).
	Items []Item `json:"items,omitempty"`
	// Held is true when this Instance has >=1 item (physical) or a
	// live-availability identifier whose feed still lists the Work (digital,
	// unless the reconciliation flagged the Work withdrawn).
	Held bool `json:"held,omitempty"`
	// Publication statement transcribed from MARC 260/264: the
	// agent ($b), the place of publication ($a) and the date. These are
	// per-Instance -- a Work's 2020 hardback and 2022 ebook differ -- so they
	// render on the edition line, not the Work-level details.
	Publisher string `json:"publisher,omitempty"`
	Place     string `json:"place,omitempty"`
	Published string `json:"published,omitempty"`
}

// Item is one holding of an Instance (the minimal bf:Item model).
type Item struct {
	CallNumber string `json:"callNumber,omitempty"`
	Location   string `json:"location,omitempty"`
	Barcode    string `json:"barcode,omitempty"`
	Note       string `json:"note,omitempty"`
}

// ProviderID is one non-ISBN identifier with its bf:source scheme, so a client-side
// availability adapter selects its key by scheme (e.g. OverDrive's "overdrive-reserve"
// Reserve ID vs the "overdrive" title id) rather than guessing from a flat list
// (ARCHITECTURE §9). Source is empty for an untagged identifier.
//
// Every scheme is projected; the hugo module decides which ones reach the DOM, via
// data/lcat/availabilityAttrs.toml. The schemes its bundled adapters resolve are
// "overdrive-reserve" (OverDrive Reserve ID) and "daia" (DAIA document id, e.g.
// "ppn:12345"). A scheme with no adapter is still projected and simply
// never queried.
type ProviderID struct {
	Source string `json:"source,omitempty"`
	Value  string `json:"value"`
}

// Facets is the precomputed facet index: for each facetable dimension, the
// distinct values and how many Works carry each. Emitting it saves the static
// site from aggregating the whole corpus in templates at build time.
type Facets struct {
	Version         int                   `json:"version"`
	Languages       []FacetValue          `json:"languages,omitempty"`
	Subjects        []SubjectFacet        `json:"subjects,omitempty"`
	Tags            []FacetValue          `json:"tags,omitempty"`
	Formats         []FacetValue          `json:"formats,omitempty"`
	Contributors    []FacetValue          `json:"contributors,omitempty"`
	Classifications []ClassificationFacet `json:"classifications,omitempty"`
}

// FacetValue is one facet value and the number of Works that carry it.
type FacetValue struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// ClassificationFacet is one classification facet value: the scheme code (the
// key), the optional human label (see Classification), and the number of Works
// carrying it.
type ClassificationFacet struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
	Count int    `json:"count"`
}

// SubjectFacet is one controlled-subject facet value: the authority URI (the key),
// its resolved labels, its skos:broader parents (for hierarchy-aware facet
// drill-down), and the number of Works carrying it. Facets key on ID so a
// relabel does not churn the facet; display uses Labels.
type SubjectFacet struct {
	ID      string            `json:"id"`
	Labels  map[string]string `json:"labels,omitempty"`
	Broader []string          `json:"broader,omitempty"`
	// Scheme is the vocabulary code (see Subject.Scheme) so a consumer
	// renders one facet group per vocabulary.
	Scheme string `json:"scheme,omitempty"`
	Count  int    `json:"count"`
}

// Facets aggregates the catalog into per-dimension value counts, each value
// counted once per Work. Values are ordered by descending count then value, so
// the output is deterministic.
func (c *Catalog) Facets() Facets {
	lang, tag, contrib := map[string]int{}, map[string]int{}, map[string]int{}
	fmts := map[string]int{}
	subj := map[string]*SubjectFacet{}
	cls := map[string]*ClassificationFacet{}
	for _, w := range c.Works {
		countDistinct(lang, w.Languages)
		countDistinct(tag, w.Tags)
		countDistinct(fmts, w.Formats)
		for _, cl := range w.Classifications {
			if cl.Value == "" {
				continue
			}
			cf := cls[cl.Value]
			if cf == nil {
				cf = &ClassificationFacet{Value: cl.Value}
				cls[cl.Value] = cf
			}
			if cf.Label == "" {
				cf.Label = cl.Label
			}
			cf.Count++
		}
		names := make([]string, len(w.Contributors))
		for i, con := range w.Contributors {
			names[i] = con.Name
		}
		countDistinct(contrib, names)
		seen := map[string]bool{}
		for _, s := range w.Subjects {
			if s.ID == "" || seen[s.ID] {
				continue
			}
			seen[s.ID] = true
			sf := subj[s.ID]
			if sf == nil {
				sf = &SubjectFacet{ID: s.ID, Labels: s.Labels, Broader: s.Broader, Scheme: s.Scheme}
				subj[s.ID] = sf
			}
			sf.Count++
		}
	}
	return Facets{
		Version:         SchemaVersion,
		Languages:       facetValues(lang),
		Subjects:        subjectFacets(subj),
		Tags:            facetValues(tag),
		Formats:         facetValues(fmts),
		Contributors:    facetValues(contrib),
		Classifications: classificationFacets(cls),
	}
}

// classificationFacets turns the value->ClassificationFacet map into a slice
// ordered by descending count, then value, so the output is deterministic.
func classificationFacets(m map[string]*ClassificationFacet) []ClassificationFacet {
	if len(m) == 0 {
		return nil
	}
	out := make([]ClassificationFacet, 0, len(m))
	for _, cf := range m {
		out = append(out, *cf)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Value < out[j].Value
	})
	return out
}

// subjectFacets turns the URI->SubjectFacet map into a slice ordered by descending
// count, then id, so the output is deterministic.
func subjectFacets(m map[string]*SubjectFacet) []SubjectFacet {
	if len(m) == 0 {
		return nil
	}
	out := make([]SubjectFacet, 0, len(m))
	for _, sf := range m {
		out = append(out, *sf)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// countDistinct increments m once for each distinct non-empty value in vals.
func countDistinct(m map[string]int, vals []string) {
	seen := map[string]bool{}
	for _, v := range vals {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		m[v]++
	}
}

// facetValues turns a value->count map into a slice ordered by descending count,
// then ascending value.
func facetValues(m map[string]int) []FacetValue {
	if len(m) == 0 {
		return nil
	}
	out := make([]FacetValue, 0, len(m))
	for v, n := range m {
		out = append(out, FacetValue{Value: v, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Value < out[j].Value
	})
	return out
}

// Redirect is one retired-Work -> surviving-Work URL redirect (ARCHITECTURE §4):
// after an editorial merge the retired id must still resolve, so shared links and
// SEO survive.
type Redirect struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// RedirectMap is the redirect artifact emitted alongside catalog.json: every
// retired Work id and the surviving id it now resolves to, chains collapsed to the
// final survivor and sorted by retired id. An empty To is a tombstone -- retired
// with no successor.
//
// Per the decision the projector emits the map and the host serves it.
// The host is the Hugo module, which publishes this file to /redirects.json and
// mints a meta-refresh stub for each merged id, and `lcat serve`, which reads the
// published map and answers 301 for a merge and 410 for a tombstone.
// Unlike catalog.json and facets.json this artifact is read at request time, so it
// must reach the served tree. Until nothing published it, nothing read
// it, and every retired permalink answered a bare 404.
type RedirectMap struct {
	Version   int        `json:"version"`
	Redirects []Redirect `json:"redirects"`
}

// Redirects builds the redirect map from a catalog.nq dataset by reading the
// editorial graph's lcat:mergedInto statements and collapsing merge chains
// (A->B->C yields A->C and B->C) to the final survivor. A merge cycle terminates
// at the last id reached rather than looping.
//
// catalogNQ must not be modified while the returned map is in use: the parse
// is zero-copy (ParseNQuadsShared), so ids alias the buffer.
func Redirects(catalogNQ []byte) (RedirectMap, error) {
	ds, err := rdf.ParseNQuadsShared(catalogNQ)
	if err != nil {
		return RedirectMap{}, err
	}
	return RedirectsDataset(ds), nil
}

// RedirectsDataset is Redirects over an already-parsed dataset, so a caller that
// projects several feeds parses the catalog once rather than once per feed
// .
func RedirectsDataset(ds *rdf.Dataset) RedirectMap {
	ed := bibframe.EditorialGraph()
	raw := map[string]string{}
	gone := map[string]bool{}
	for _, q := range ds.Quads {
		if q.G != ed || !q.S.IsIRI() {
			continue
		}
		switch q.P.Value {
		case bibframe.PredMergedInto:
			if q.O.IsIRI() {
				raw[fragID(q.S.Value, "Work")] = fragID(q.O.Value, "Work")
			}
		case bibframe.PredTombstoned:
			// A tombstone with a successor redirects like a merge; one
			// without leaves an empty-target entry the host serves as gone.
			if q.O.IsIRI() {
				raw[fragID(q.S.Value, "Work")] = fragID(q.O.Value, "Work")
			} else {
				gone[fragID(q.S.Value, "Work")] = true
			}
		}
	}
	rm := RedirectMap{Version: SchemaVersion, Redirects: []Redirect{}}
	froms := make([]string, 0, len(raw)+len(gone))
	for from := range raw {
		froms = append(froms, from)
	}
	for from := range gone {
		if _, mapped := raw[from]; !mapped {
			froms = append(froms, from)
		}
	}
	sort.Strings(froms)
	for _, from := range froms {
		if gone[from] {
			if _, mapped := raw[from]; !mapped {
				rm.Redirects = append(rm.Redirects, Redirect{From: from, To: ""})
				continue
			}
		}
		if to := follow(raw, from); to != from {
			rm.Redirects = append(rm.Redirects, Redirect{From: from, To: to})
		}
	}
	return rm
}

// follow chases the merge chain from start to the final survivor -- the last id
// with no onward mapping. It stops on a missing link or a cycle (returning the id
// reached), so a malformed overlay cannot loop.
func follow(raw map[string]string, start string) string {
	seen := map[string]bool{}
	cur := start
	for {
		to, ok := raw[cur]
		if !ok || to == "" || seen[cur] {
			return cur
		}
		seen[cur] = true
		cur = to
	}
}

// Project reads a catalog.nq dataset and projects each Work into a Catalog record.
// Display/facet fields are drawn from the union of the provider's feed graph and
// the editorial graph, so curated subjects appear alongside feed data. Editorial
// lcat:overrides markers shadow the feed first: a property a
// cataloger claimed shows only its editorial values, and deleting the marker
// resurfaces the feed untouched.
//
// catalogNQ must not be modified while the returned Catalog is in use: the
// parse is zero-copy (ParseNQuadsShared) -- one input-sized
// allocation saved at corpus scale -- so projected strings alias the buffer.
func Project(catalogNQ []byte, provider string) (*Catalog, error) {
	ds, err := rdf.ParseNQuadsShared(catalogNQ)
	if err != nil {
		return nil, err
	}
	return ProjectDataset(ds, provider), nil
}

// ProjectDataset is Project over an already-parsed dataset. A multi-feed
// projection views one provenance graph at a time, so it used to reparse the
// whole catalog once per feed -- five full parses of a 1.76M-quad corpus for
// three feeds plus Feeds and Redirects. Pair it with LoadDataset.
func ProjectDataset(ds *rdf.Dataset, provider string) *Catalog {
	overrides := bibframe.ScanOverrides(ds)
	view := mergedView(ds, bibframe.FeedGraph(provider), bibframe.EditorialGraph(), overrides)
	p := &projector{
		view:    view,
		labels:  buildLabelIndex(ds),
		broader: buildBroaderIndex(ds),
		aliases: buildTagAliasIndex(ds),
		extras:  buildExtraIndex(ds, bibframe.FeedGraph(provider), bibframe.EditorialGraph()),
	}
	cat := &Catalog{Version: SchemaVersion}
	if p.view == nil {
		return cat
	}
	for _, w := range p.view.SubjectsOfType(classWork) {
		// Only the catalog's own minted Works project as records. Since
		// libcodex v0.11.0 the crosswalk types 76X-78X relation targets as
		// bf:Work too -- related-resource stubs that belong inside their
		// carrying record, not as top-level catalog entries. Minted Works
		// are the "#<id>Work" fragment IRIs (the identity.ScanGrain
		// convention); relation stubs are blank or external nodes.
		if !w.IsIRI() || !strings.HasPrefix(w.Value, "#") || !strings.HasSuffix(w.Value, "Work") {
			continue
		}
		// The delete stance: a tombstoned Work leaves the
		// projection (its redirect entry comes from Redirects); a suppressed
		// one merely hides. Both statements are editorial, so they ride the
		// merged view.
		if len(p.view.Objects(w, bibframe.PredTombstoned)) > 0 {
			continue
		}
		if lit, ok := p.view.Literal(w, bibframe.PredSuppressed); ok && lit == "true" {
			continue
		}
		cat.Works = append(cat.Works, p.work(w))
	}
	sort.Slice(cat.Works, func(i, j int) bool { return cat.Works[i].ID < cat.Works[j].ID })
	resolveRelations(cat.Works)
	cat.Terms = p.termSideband(cat.Works)
	return cat
}

// termSideband collects the vocabulary sideband: every subject
// URI the works reference plus its transitive skos:broader closure, emitted
// as Terms when the graph carries any metadata for them. The closure walks
// the corpus-wide broader index, so an ancestor chain an enricher described
// (labels + broader quads, no work link) surfaces here even though no
// Work.Subjects entry names it.
func (p *projector) termSideband(works []Work) []Term {
	seen := map[string]bool{}
	var frontier []string
	for _, w := range works {
		for _, s := range w.Subjects {
			if !seen[s.ID] {
				seen[s.ID] = true
				frontier = append(frontier, s.ID)
			}
		}
	}
	// BFS over broader edges; the seen set terminates cycles.
	for len(frontier) > 0 {
		var next []string
		for _, uri := range frontier {
			for _, parent := range p.broader[uri] {
				if parent == "" || seen[parent] {
					continue
				}
				seen[parent] = true
				next = append(next, parent)
			}
		}
		frontier = next
	}
	out := make([]Term, 0, len(seen))
	for uri := range seen {
		labels, broader := p.labels[uri], p.broader[uri]
		if len(labels) == 0 && len(broader) == 0 {
			continue
		}
		out = append(out, Term{ID: uri, Labels: labels, Broader: broader, Scheme: SchemeForURI(uri)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) == 0 {
		return nil
	}
	return out
}

type projector struct {
	// view is the projection graph: the feed graph with editorially-owned
	// (lcat:overrides) properties filtered out, merged with the editorial
	// graph -- so cataloger edits project as first-class values for every
	// field. Editorial statements are blank-free, so the merge
	// cannot collide labels.
	view    *rdf.Graph
	labels  map[string]map[string]string // authority URI -> language tag -> label
	broader map[string][]string          // authority URI -> sorted parent (skos:broader) URIs
	aliases map[string][]string          // authority URI -> tags it subsumes (lcat:tagAlias)
	extras  map[string]map[string]string // Work node IRI -> extra key -> value
}

// mergedView builds the projector's feed+editorial view: feed triples with
// overridden statements shadowed, then editorial triples, in one exactly-
// sized allocation. GraphViews rather than direct quad
// iteration: libcodex v0.20.0's cached per-graph counts make Len free after
// one shared pass and Empty skips the editorial walk entirely, so the view
// version now beats the fused hand-written merge it replaced.
// Triple order matches the old pipeline: feed first, editorial appended.
func mergedView(ds *rdf.Dataset, feed, editorial rdf.Term, overrides bibframe.Overrides) *rdf.Graph {
	fv, ev := ds.GraphView(feed), ds.GraphView(editorial)
	if fv.Empty() && ev.Empty() {
		return nil
	}
	merged := &rdf.Graph{Triples: make([]rdf.Triple, 0, fv.Len()+ev.Len())}
	for tr := range fv.Triples() {
		if tr.S.IsIRI() && overrides.Shadows(tr.S.Value, tr.P.Value) {
			continue
		}
		merged.Triples = append(merged.Triples, tr)
	}
	if !ev.Empty() {
		for tr := range ev.Triples() {
			merged.Triples = append(merged.Triples, tr)
		}
	}
	return merged
}

func (p *projector) work(w rdf.Term) Work {
	wk := Work{ID: fragID(w.Value, "Work")}
	if t, ok := p.view.Object(w, pTitle); ok {
		wk.Title, _ = p.view.Literal(t, pMainTitle)
		wk.Subtitle, _ = p.view.Literal(t, pSubtitle)
	}
	wk.Summary = p.summary(w)
	wk.Contributors = p.contributors(w)
	wk.Subjects, wk.Tags = p.subjectsAndTags(w)
	wk.Languages = p.languages(w)
	wk.Classifications = p.classifications(w)
	wk.Instances = p.instances(w)
	wk.Formats = formatUnion(wk.Instances)
	wk.Extra = p.extras[w.Value]
	wk.Relations = p.relations(w)
	wk.Series = p.series(w)
	for _, inst := range wk.Instances {
		if inst.Held {
			wk.Held = true
			break
		}
	}
	return wk
}

// summary returns the Work's description/abstract: the label of its first
// bf:summary node carrying one. Records rarely have several (a MARC record can
// repeat 520); first-wins matches how the title projects.
func (p *projector) summary(w rdf.Term) string {
	for _, s := range p.view.Objects(w, pSummary) {
		if label, _ := p.view.Literal(s, pLabel); label != "" {
			return label
		}
	}
	return ""
}

// formatUnion is the deduped, sorted set of the Work's Instances' formats -- the
// Work-level formats facet. A clustered ebook+audiobook yields both.
func formatUnion(insts []Instance) []string {
	set := map[string]bool{}
	for _, i := range insts {
		if i.Format != "" {
			set[i.Format] = true
		}
	}
	return sortedKeys(set)
}

// contributors returns the Work's agents, primary contributions first (as a MARC
// 1XX would lead), then the rest by name.
func (p *projector) contributors(w rdf.Term) []Contributor {
	type entry struct {
		c       Contributor
		primary bool
	}
	var es []entry
	// Dedupe on (name, role) like the other dimensions: a feed and
	// an editorial contribution node for the same agent -- or a provider
	// repeating a creator -- must not list the contributor twice. A duplicate
	// that is primary anywhere stays primary.
	seen := map[Contributor]int{}
	for _, node := range p.view.Objects(w, pContribution) {
		agent, ok := p.view.Object(node, pAgent)
		if !ok {
			continue
		}
		name, _ := p.view.Literal(agent, pLabel)
		if name == "" {
			continue
		}
		var role string
		if r, ok := p.view.Object(node, pRole); ok {
			role, _ = p.view.Literal(r, pLabel)
		}
		c := Contributor{Name: name, Role: role}
		primary := p.view.HasType(node, primaryContr)
		if i, dup := seen[c]; dup {
			es[i].primary = es[i].primary || primary
			continue
		}
		seen[c] = len(es)
		es = append(es, entry{c, primary})
	}
	// Sort by (primary desc, name, role) -- a total order over the distinguishing
	// fields, so the projection is independent of contribution statement order: two
	// equivalent serializations of the same graph must yield identical catalog.json.
	sort.Slice(es, func(i, j int) bool {
		if es[i].primary != es[j].primary {
			return es[i].primary
		}
		if es[i].c.Name != es[j].c.Name {
			return es[i].c.Name < es[j].c.Name
		}
		return es[i].c.Role < es[j].c.Role
	})
	out := make([]Contributor, len(es))
	for i, e := range es {
		out[i] = e.c
	}
	return out
}

// series collects the Work's series memberships.
//
// libcodex >= v0.25.0 emits one bf:relation per 490, on the Work, following LC's
// ConvSpec-Process6-Series.xsl. The relation carries the enumeration and points at
// a bf:Series through bf:associatedResource:
//
//	<Work> bf:relation _:rel .
//	_:rel   bf:relationship <.../relationship/series> ;
//	        bf:seriesEnumeration "bk. 2" ;
//	        bf:associatedResource _:series .
//	_:series a bf:Series ; bf:title [ bf:mainTitle "Firebrand fiction" ] ;
//	        bf:status <.../mstatus/tr> ; bf:identifiedBy [ a bf:Issn ; rdf:value "..." ] .
//
// The relationship IRI is what discriminates a series from a 76x-78x linking
// entry, which uses the same bf:relation predicate. Reading bf:relation without
// checking it would project every "translation of" and "sequel to" as a series.
//
// libcodex >= v0.33.0 additionally emits the controlled series added entries
// (800/810/811/830) and the 760/762 linking entries: same relationship IRI
// (762 uses .../subseries), with the associated resource typed bf:Hub as well
// as bf:Series. A controlled entry IS the series trace, so it projects
// Traced. And because a cataloged record typically carries the pair -- the
// 490 transcription AND its 8xx controlled form -- two relations describing
// one membership merge by title: one Series entry, traced, with the
// enumeration and ISSN from whichever side carries them.
//
// Sorted by title then enumeration, so a projection is stable across runs: blank
// node labels are canonicalized per graph, not per corpus, and Objects() does not
// promise an order.
func (p *projector) series(w rdf.Term) []Series {
	var out []Series
	byTitle := map[string]int{}
	for _, rel := range p.view.Objects(w, pRelation) {
		r, ok := p.view.Object(rel, pRelationship)
		if !ok || (r.Value != seriesRelationIRI && r.Value != subseriesRelationIRI) {
			continue
		}
		res, ok := p.view.Object(rel, pAssociatedResource)
		if !ok {
			continue
		}
		s := Series{Title: p.seriesTitle(res)}
		if s.Title == "" {
			// A series with no transcribed statement is a relation we cannot
			// render. Its enumeration alone ("bk. 2" of what?) says nothing.
			continue
		}
		// The enumeration hangs off the RELATION, not the series -- which is the
		// whole point of the reshape. "bk. 2" is a fact about this Work's
		// membership; the same series numbers another Work differently.
		if enum, ok := p.view.Literal(rel, pSeriesEnum); ok {
			s.Enumeration = enum
		}
		// mstatus/tr marks a traced 490; a Hub-typed resource is itself the
		// controlled added entry, i.e. the trace.
		if p.view.HasType(res, classHub) {
			s.Traced = true
		}
		for _, st := range p.view.Objects(res, pStatus) {
			if st.Value == statusTracedIRI {
				s.Traced = true
			}
		}
		// A bf:Series may carry several identifiers; only the bf:Issn is its ISSN.
		// First one wins and stops the walk, so the result does not depend on the
		// order Objects() happens to return.
		for _, id := range p.view.Objects(res, pIdentifiedBy) {
			if !p.view.HasType(id, classIssn) {
				continue
			}
			if v, ok := p.view.Literal(id, pValue); ok && v != "" {
				s.ISSN = v
				break
			}
		}
		if at, dup := byTitle[seriesTitleKey(s.Title)]; dup {
			out[at] = mergeSeries(out[at], s)
			continue
		}
		byTitle[seriesTitleKey(s.Title)] = len(out)
		out = append(out, s)
	}
	if len(out) == 0 {
		out = p.legacySeries(w)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Title != out[b].Title {
			return out[a].Title < out[b].Title
		}
		return out[a].Enumeration < out[b].Enumeration
	})
	return out
}

// seriesTitleKey folds a series title for pairing a 490 transcription with
// its controlled 8xx form: case and the transcription's trailing ISBD
// punctuation must not keep one membership as two entries.
func seriesTitleKey(t string) string {
	return strings.ToLower(strings.TrimRight(t, " ;:,."))
}

// mergeSeries folds two relations describing one membership (the 490 and its
// 8xx pair) into one entry: the controlled side usually carries the ISSN,
// the transcription the enumeration, and the trace holds if either says so.
// When the titles differ by ISBD residue, the form without trailing
// punctuation wins -- relation order is not promised, the output must not
// depend on it.
func mergeSeries(a, b Series) Series {
	if a.Title != b.Title && strings.TrimRight(b.Title, " ;:,.") == b.Title {
		a.Title = b.Title
	}
	if a.Enumeration == "" {
		a.Enumeration = b.Enumeration
	}
	if a.ISSN == "" {
		a.ISSN = b.ISSN
	}
	a.Traced = a.Traced || b.Traced
	return a
}

// seriesTitle reads bf:title -> bf:mainTitle off a bf:Series node.
func (p *projector) seriesTitle(res rdf.Term) string {
	for _, t := range p.view.Objects(res, pTitle) {
		if v, ok := p.view.Literal(t, pMainTitle); ok && v != "" {
			return v
		}
	}
	return ""
}

// legacySeries reads the pre-v0.25.0 flat literals off the Work's Instances, so a
// grain tree written by an older libcodex keeps projecting its series rather than
// silently losing them the day the dependency is bumped. Only consulted when the
// Work carries no series relation at all.
//
// It inherits the defect it cannot fix. The flat shape paired statement to
// enumeration by list position, and an RDF graph is a set: two 490s sharing a $v
// emitted one triple, so the pairing was already gone before this code saw it.
// Every legacy series therefore gets the Instance's first non-empty enumeration,
// which is what the old projector did to all of them. Re-ingest to get it right.
func (p *projector) legacySeries(w rdf.Term) []Series {
	var titles []string
	enum := ""
	for _, inst := range p.view.Objects(w, pHasInstance) {
		for _, s := range p.view.Objects(inst, pSeriesStatement) {
			if s.IsLiteral() && s.Value != "" {
				titles = append(titles, s.Value)
			}
		}
		if enum == "" {
			for _, s := range p.view.Objects(inst, pSeriesEnum) {
				if s.IsLiteral() && s.Value != "" {
					enum = s.Value
					break
				}
			}
		}
	}
	var out []Series
	for _, t := range sortedUniqueStrings(titles) {
		out = append(out, Series{Title: t, Enumeration: enum})
	}
	return out
}

// sortedUniqueStrings sorts and de-duplicates: several printings of one Work
// transcribe the same 490 on each Instance.
func sortedUniqueStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	sort.Strings(in)
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}

// relations collects the Work's editorial whole/part link targets as raw
// ids; Project() resolves display titles and drops targets
// absent from the projection in a post-pass over the built catalog, so a
// link to a suppressed or tombstoned work never renders as a dead
// cross-link.
func (p *projector) relations(w rdf.Term) *Relations {
	collect := func(pred string) []RelatedWork {
		var out []RelatedWork
		for _, t := range p.view.Objects(w, pred) {
			if !t.IsIRI() || !strings.HasPrefix(t.Value, "#") || !strings.HasSuffix(t.Value, "Work") {
				continue
			}
			if id := fragID(t.Value, "Work"); id != "" {
				out = append(out, RelatedWork{ID: id})
			}
		}
		sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
		return out
	}
	hasPart, partOf := collect(pHasPart), collect(pPartOf)
	if len(hasPart)+len(partOf) == 0 {
		return nil
	}
	return &Relations{HasPart: hasPart, PartOf: partOf}
}

// resolveRelations is the post-pass: titles from the projection itself, and
// links to works outside it dropped.
func resolveRelations(works []Work) {
	titles := make(map[string]string, len(works))
	for _, w := range works {
		titles[w.ID] = w.Title
	}
	keep := func(links []RelatedWork) []RelatedWork {
		var out []RelatedWork
		for _, l := range links {
			if title, ok := titles[l.ID]; ok {
				l.Title = title
				out = append(out, l)
			}
		}
		return out
	}
	for i := range works {
		r := works[i].Relations
		if r == nil {
			continue
		}
		r.HasPart, r.PartOf = keep(r.HasPart), keep(r.PartOf)
		if len(r.HasPart)+len(r.PartOf) == 0 {
			works[i].Relations = nil
		}
	}
}

// subjectsAndTags splits a Work's bf:subject objects (across the feed and editorial
// graphs) into two dimensions. An external IRI object is a controlled-
// vocabulary subject: its authority URI plus labels resolved from the graph
// (buildLabelIndex). A labeled blank node -- or a labeled grain-local fragment
// node like an editor skolem -- is an uncontrolled tag: its label
// string. Subjects are deduped by URI and sorted by URI; tags are deduped and
// sorted.
//
// Consequence for authority-less feeds: a Work whose bf:subject objects are all
// labeled blank nodes contributes zero controlled subjects, so a corpus built
// purely from such a feed projects an empty subject facet -- its topical terms
// live in the tag dimension instead. Both OverDrive routes are authority-less
// this way: Thunder JSON subjects are uncontrolled label strings, and OverDrive
// MARC-Express 6XX carry no $0 authority URI (only $2 source vocabularies, chiefly
// bisacsh, which the crosswalk models as classification, not a subject). This is
// vendor data, not a crosswalk or projection defect; controlled subject facets
// require authority-linked source records (e.g. LCSH 650 $0).
func (p *projector) subjectsAndTags(w rdf.Term) ([]Subject, []string) {
	subj := map[string]Subject{}
	tags := map[string]bool{}
	collect := func(g *rdf.Graph) {
		if g == nil {
			return
		}
		for _, s := range g.Objects(w, pSubject) {
			if s.IsIRI() && !bibframe.GrainLocalIRI(s.Value) {
				if _, ok := subj[s.Value]; !ok {
					subj[s.Value] = Subject{ID: s.Value, Labels: p.labels[s.Value], Broader: p.broader[s.Value], Scheme: SchemeForURI(s.Value)}
				}
			} else if label, ok := g.Literal(s, pLabel); ok && label != "" {
				// Blank or grain-local heading node: an uncontrolled
				// heading's label is a tag.
				tags[label] = true
			}
		}
		for _, tag := range g.Objects(w, pTag) {
			if tag.IsLiteral() && tag.Value != "" {
				tags[tag.Value] = true
			}
		}
	}
	collect(p.view)

	// A promoted tag disappears where its controlled term is present: the
	// tag "became" the subject (lcat:tagAlias). Works carrying
	// the tag but not the term keep showing it.
	for id := range subj {
		for _, aliased := range p.aliases[id] {
			delete(tags, aliased)
		}
	}

	ids := make([]string, 0, len(subj))
	for id := range subj {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	subjects := make([]Subject, len(ids))
	for i, id := range ids {
		subjects[i] = subj[id]
	}
	if len(subjects) == 0 {
		subjects = nil
	}
	return subjects, sortedKeys(tags)
}

// buildLabelIndex indexes the human labels of controlled-vocabulary terms across
// every graph in the dataset: for each IRI subject of skos:prefLabel or
// rdfs:label, it maps the term URI -> language tag -> label. prefLabel wins over
// rdfs:label for the same (URI, language). These labels come from authority
// statements (e.g. an authority:<vocab> graph merged into catalog.nq); the index is
// empty when no authority data is present, so subjects fall back to their URI.
func buildLabelIndex(ds *rdf.Dataset) map[string]map[string]string {
	idx := map[string]map[string]string{}
	put := func(uri, lang, label string, pref bool) {
		if label == "" {
			return
		}
		byLang := idx[uri]
		if byLang == nil {
			byLang = map[string]string{}
			idx[uri] = byLang
		}
		if _, ok := byLang[lang]; ok && !pref {
			return // keep prefLabel over rdfs:label
		}
		byLang[lang] = label
	}
	// Two passes so prefLabel always wins regardless of statement order.
	for _, q := range ds.Quads {
		if q.P.Value == pPrefLabel && q.S.IsIRI() && q.O.IsLiteral() {
			put(q.S.Value, q.O.Lang, q.O.Value, true)
		}
	}
	for _, q := range ds.Quads {
		if q.P.Value == pLabel && q.S.IsIRI() && q.O.IsLiteral() {
			put(q.S.Value, q.O.Lang, q.O.Value, false)
		}
	}
	if len(idx) == 0 {
		return nil
	}
	return idx
}

// buildTagAliasIndex indexes lcat:tagAlias statements across every graph:
// controlled-term URI -> the uncontrolled tag strings it subsumes
// . Nil when the corpus carries no aliases.
func buildTagAliasIndex(ds *rdf.Dataset) map[string][]string {
	var idx map[string][]string
	for _, q := range ds.Quads {
		if q.P.Value != bibframe.PredTagAlias || !q.S.IsIRI() || !q.O.IsLiteral() || q.O.Value == "" {
			continue
		}
		if idx == nil {
			idx = map[string][]string{}
		}
		idx[q.S.Value] = append(idx[q.S.Value], q.O.Value)
	}
	return idx
}

// buildBroaderIndex indexes the skos:broader hierarchy links of controlled-vocabulary
// terms across every graph: for each IRI subject with an IRI skos:broader
// object it maps the term URI -> sorted, deduped parent term URIs. These come from
// authority statements (e.g. an authority:<vocab> graph). A consumer joins a parent
// URI back to its own Subject/authority record to render breadcrumb trails. The index
// is provider/vocabulary-agnostic and nil when the corpus carries no skos:broader.
func buildBroaderIndex(ds *rdf.Dataset) map[string][]string {
	set := map[string]map[string]bool{}
	for _, q := range ds.Quads {
		if q.P.Value == pBroader && q.S.IsIRI() && q.O.IsIRI() {
			parents := set[q.S.Value]
			if parents == nil {
				parents = map[string]bool{}
				set[q.S.Value] = parents
			}
			parents[q.O.Value] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	idx := make(map[string][]string, len(set))
	for uri, parents := range set {
		idx[uri] = sortedKeys(parents)
	}
	return idx
}

// buildExtraIndex indexes the non-BIBFRAME adopter "extras" a provider carried through
// the feed provenance graph: for each Work-node subject with a
// bibframe.ExtraPred+<key> literal in feed, it maps the Work node IRI -> key -> value.
// Restricting to the feed graph keeps the read provenance-scoped, mirroring how extras
// were emitted. The result is nil when the corpus carries none, so existing catalogs are
// unchanged (Work.Extra stays omitted).
func buildExtraIndex(ds *rdf.Dataset, feed, editorial rdf.Term) map[string]map[string]string {
	idx := map[string]map[string]string{}
	// Feed pass first, editorial pass second: an editorial extra (an
	// uploaded cover) overlays the feed's value for the same
	// key, matching the override model everywhere else. Empty views (no
	// editorial extras is the common case) cost no walk.
	for _, gv := range []*rdf.GraphView{ds.GraphView(feed), ds.GraphView(editorial)} {
		if gv.Empty() {
			continue
		}
		for tr := range gv.Triples() {
			if !tr.S.IsIRI() || !tr.O.IsLiteral() {
				continue
			}
			if !strings.HasPrefix(tr.P.Value, bibframe.ExtraPred) {
				continue
			}
			key := tr.P.Value[len(bibframe.ExtraPred):]
			if key == "" {
				continue
			}
			m := idx[tr.S.Value]
			if m == nil {
				m = map[string]string{}
				idx[tr.S.Value] = m
			}
			m[key] = tr.O.Value
		}
	}
	if len(idx) == 0 {
		return nil
	}
	return idx
}

func (p *projector) languages(w rdf.Term) []string {
	set := map[string]bool{}
	for _, l := range p.view.Objects(w, pLanguage) {
		if code := rdf.LocalName(l.Value); code != "" {
			set[code] = true
		}
	}
	return sortedKeys(set)
}

// classifications collects the Work's classifications, deduped by code. The
// code is the node's bf:classificationPortion; the optional display label is
// its rdfs:label -- the display-only channel a scheme-aware crosswalk (or an
// editorial graph) hangs the human text on. When the same code
// appears both labeled and bare, the label wins.
func (p *projector) classifications(w rdf.Term) []Classification {
	labels := map[string]string{}
	for _, c := range p.view.Objects(w, pClassif) {
		v, ok := p.view.Literal(c, pClassPortion)
		if !ok || v == "" {
			continue
		}
		label, _ := p.view.Literal(c, pLabel)
		if labels[v] == "" {
			labels[v] = label
		}
	}
	if len(labels) == 0 {
		return nil
	}
	values := make([]string, 0, len(labels))
	for v := range labels {
		values = append(values, v)
	}
	sort.Strings(values)
	out := make([]Classification, len(values))
	for i, v := range values {
		out[i] = Classification{Value: v, Label: labels[v]}
	}
	return out
}

// availabilitySources are the bf:source schemes whose identifiers a runtime
// availability adapter can resolve -- the digital-holding signal for Held
// . Bibliographic control numbers (LCCN, local ids) are not
// holdings.
var availabilitySources = map[string]bool{"overdrive-reserve": true}

func (p *projector) instances(w rdf.Term) []Instance {
	// A withdrawal flag means the availability feed stopped
	// listing this Work: its identifiers no longer count as digital holdings.
	withdrawn := len(p.view.Objects(w, bibframe.PredWithdrawn)) > 0
	var out []Instance
	for _, inst := range p.view.Objects(w, pHasInstance) {
		i := Instance{ID: fragID(inst.Value, "Instance"), Format: p.instanceFormat(inst)}
		var isbns []string
		var pids []ProviderID
		for _, id := range p.view.Objects(inst, pIdentifiedBy) {
			v, ok := p.view.Literal(id, pValue)
			if !ok || v == "" {
				continue
			}
			if p.view.HasType(id, classIsbn) {
				isbns = append(isbns, v)
				continue
			}
			pids = append(pids, ProviderID{Source: p.identifierSource(id), Value: v})
		}
		sort.Strings(isbns)
		sort.Slice(pids, func(a, b int) bool {
			if pids[a].Source != pids[b].Source {
				return pids[a].Source < pids[b].Source
			}
			return pids[a].Value < pids[b].Value
		})
		i.ISBNs, i.ProviderIDs = isbns, pids
		i.Publisher, i.Place, i.Published = p.publication(inst)
		i.Items = p.items(inst)
		i.Held = len(i.Items) > 0
		if !i.Held && !withdrawn {
			for _, pid := range pids {
				if availabilitySources[pid.Source] {
					i.Held = true
					break
				}
			}
		}
		out = append(out, i)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}

// publication projects the Instance's publication statement from the
// first bf:provisionActivity typed bf:Publication: the transcribed publisher
// (bflc:simpleAgent), place (bflc:simplePlace) and date (bf:date, or
// bflc:simpleDate when the controlled date is absent). Distribution/Manufacture
// provisions carry the same shape but are not a "published" statement, so they
// are skipped. Provision nodes are ordered by node value so the choice is
// deterministic when a record transcribes more than one Publication field.
func (p *projector) publication(inst rdf.Term) (publisher, place, date string) {
	nodes := p.view.Objects(inst, pProvisionActivity)
	sort.Slice(nodes, func(a, b int) bool { return nodes[a].Value < nodes[b].Value })
	for _, node := range nodes {
		if !p.view.HasType(node, classPublication) {
			continue
		}
		publisher, _ = p.view.Literal(node, pSimpleAgent)
		place, _ = p.view.Literal(node, pSimplePlace)
		if date, _ = p.view.Literal(node, pDate); date == "" {
			date, _ = p.view.Literal(node, pSimpleDate)
		}
		return publisher, place, date
	}
	return "", "", ""
}

// items projects an Instance's holdings, ordered by item node.
func (p *projector) items(inst rdf.Term) []Item {
	nodes := p.view.Objects(inst, pHasItem)
	if len(nodes) == 0 {
		return nil
	}
	sort.Slice(nodes, func(a, b int) bool { return nodes[a].Value < nodes[b].Value })
	out := make([]Item, 0, len(nodes))
	for _, node := range nodes {
		var item Item
		item.CallNumber, _ = p.view.Literal(node, pShelfMark)
		item.Location, _ = p.view.Literal(node, pPhysicalLocation)
		item.Barcode, _ = p.view.Literal(node, bibframe.PredBarcode)
		item.Note, _ = p.view.Literal(node, bibframe.PredItemNote)
		out = append(out, item)
	}
	return out
}

// identifierSource returns the rdfs:label of an identifier node's bf:source scheme,
// or "" when the identifier carries no scheme.
func (p *projector) identifierSource(id rdf.Term) string {
	if src, ok := p.view.Object(id, pSource); ok {
		if label, ok := p.view.Literal(src, pLabel); ok {
			return label
		}
	}
	return ""
}

// instanceFormat reads the Instance's RDA media type (bf:media -> a bf:Media with an
// rdfs:label) and maps it to a discovery format. It falls back to the carrier label
// when no media is present, and to "" when neither is (format omitted).
func (p *projector) instanceFormat(inst rdf.Term) string {
	if m, ok := p.view.Object(inst, pMedia); ok {
		if label, ok := p.view.Literal(m, pLabel); ok && label != "" {
			return formatFromRDA(label)
		}
	}
	if c, ok := p.view.Object(inst, pCarrier); ok {
		if label, ok := p.view.Literal(c, pLabel); ok {
			return formatFromCarrier(label)
		}
	}
	return ""
}

// formatFromRDA maps an RDA media type (bf:media) to a discovery format token. The
// mapping is general RDA, not provider-specific: any provider emitting bf:media
// benefits. Digital ebooks and audiobooks share the "online resource" carrier, so
// the media type is what distinguishes them. An unrecognized media type passes
// through so nothing is silently dropped.
func formatFromRDA(media string) string {
	switch media {
	case "audio":
		return "audiobook"
	case "computer":
		return "ebook"
	case "video":
		return "video"
	case "unmediated":
		return "print"
	default:
		return media
	}
}

// formatFromCarrier is the fallback when an Instance carries no media type: a coarse
// carrier -> format guess. "online resource" alone cannot tell ebook from audiobook,
// so it yields "" rather than mislabel; "volume" is print.
func formatFromCarrier(carrier string) string {
	if carrier == "volume" {
		return "print"
	}
	return ""
}

// fragID extracts an id from a node IRI of the form "#<id><suffix>".
func fragID(iri, suffix string) string {
	if len(iri) > 0 && iri[0] == '#' {
		iri = iri[1:]
	}
	if n := len(iri) - len(suffix); n >= 0 && iri[n:] == suffix {
		return iri[:n]
	}
	return iri
}

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
