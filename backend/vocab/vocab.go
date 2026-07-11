// Package vocab loads controlled vocabularies from SKOS authority grains and
// serves the in-memory term index behind term validation, the picker's
// autocomplete, and neighborhood browsing. A vocabulary's quads live in its
// authority:<vocab> named graph (ARCHITECTURE §5), so the loader routes terms
// to schemes by graph name -- one authorities tree can carry homosaurus, lcsh,
// and local terms side by side. This replaces qllpoc's embedded
// homosaurus-min.json with a vocabulary-agnostic store-backed load.
package vocab

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"
	"github.com/freeeve/libcat/storage/vocabsidecar"
)

// SKOS predicate IRIs.
const (
	skosPrefLabel  = "http://www.w3.org/2004/02/skos/core#prefLabel"
	skosAltLabel   = "http://www.w3.org/2004/02/skos/core#altLabel"
	skosDefinition = "http://www.w3.org/2004/02/skos/core#definition"
	skosBroader    = "http://www.w3.org/2004/02/skos/core#broader"
	skosNarrower   = "http://www.w3.org/2004/02/skos/core#narrower"
	skosRelated    = "http://www.w3.org/2004/02/skos/core#related"
	skosExactMatch = "http://www.w3.org/2004/02/skos/core#exactMatch"
	skosCloseMatch = "http://www.w3.org/2004/02/skos/core#closeMatch"
	rdfsLabel      = "http://www.w3.org/2000/01/rdf-schema#label"
	// authorityGraphPrefix matches bibframe.AuthorityGraph's naming.
	authorityGraphPrefix = "authority:"
)

// Term is one controlled-vocabulary concept.
type Term struct {
	Scheme     string              `json:"scheme"`
	ID         string              `json:"id"`                   // the authority URI
	Labels     map[string]string   `json:"labels"`               // lang -> prefLabel ("" key = untagged)
	AltLabels  map[string][]string `json:"altLabels,omitempty"`  // lang -> used-for labels
	Definition map[string]string   `json:"definition,omitempty"` // lang -> scope note
	Broader    []string            `json:"broader,omitempty"`
	Narrower   []string            `json:"narrower,omitempty"`
	Related    []string            `json:"related,omitempty"`
	ExactMatch []string            `json:"exactMatch,omitempty"`
	CloseMatch []string            `json:"closeMatch,omitempty"`
	// MergedInto marks a retired term: it was merged into the referenced
	// URI (lcat:mergedInto). Retired terms resolve via Lookup
	// (so old references still label) but leave the search index.
	MergedInto string `json:"mergedInto,omitempty"`
}

// Label returns the term's best label for lang: exact match, then English,
// then untagged, then any; the term URI when no label exists.
func (t *Term) Label(lang string) string {
	if l := PickLabel(t.Labels, lang); l != "" {
		return l
	}
	return t.ID
}

// PickLabel returns the best display label from a language-tagged label map
// (the convention every label-bearing shape shares): each
// preferred tag in order, then English, then untagged, then the
// lexicographically first remaining tag -- deterministic where map order is
// not. Empty-string labels never win; "" means no usable label, and the
// caller supplies its own fallback (term URI, raw ID).
func PickLabel(labels map[string]string, prefer ...string) string {
	for _, k := range append(prefer, "en", "") {
		if l := labels[k]; l != "" {
			return l
		}
	}
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		if l := labels[k]; l != "" {
			return l
		}
	}
	return ""
}

// Index is the loaded term index. Reads are lock-free over an immutable
// snapshot; Reload builds a fresh snapshot and swaps it atomically, so every
// holder of the *Index (terms handler, suggestion gate, publisher) sees
// authority edits without rewiring.
type Index struct {
	snap atomic.Pointer[snapshot]
}

// snapshot is one immutable build of the index. A scheme is served either
// from maps (schemes/search) or from sidecar artifacts -- never
// both: any loose quads or a stale manifest bypass the sidecar for that
// build, so the map path stays the correctness backstop.
type snapshot struct {
	schemes map[string]map[string]*Term
	// search holds, per scheme, entries sorted by normalized label for
	// prefix search across pref and alt labels in every language.
	search map[string][]searchEntry
	// matchTiers maps canonicalized identifier URIs to live terms, one map
	// per precedence tier (own ID, skos:exactMatch, skos:closeMatch), for
	// identifier-based reconciliation across schemes.
	matchTiers [identifierTiers]map[string]*Term
	// sidecar holds the artifact-backed schemes.
	sidecar map[string]*sidecarScheme
}

type searchEntry struct {
	norm string
	uri  string
	alt  bool
}

var emptySnapshot = &snapshot{}

// load returns the current snapshot, never nil.
func (ix *Index) load() *snapshot {
	if s := ix.snap.Load(); s != nil {
		return s
	}
	return emptySnapshot
}

// Load reads every authority grain under prefix from the store and indexes
// the terms of the requested schemes (nil/empty schemes = all authority
// graphs found).
func Load(ctx context.Context, st blob.Store, prefix string, schemes []string) (*Index, error) {
	ix := &Index{}
	if err := ix.Reload(ctx, st, prefix, schemes); err != nil {
		return nil, err
	}
	return ix, nil
}

// Reload rebuilds the index from the store and atomically swaps it in --
// the post-authority-edit refresh path. A failed reload leaves the previous
// snapshot serving.
func (ix *Index) Reload(ctx context.Context, st blob.Store, prefix string, schemes []string) error {
	s, err := buildSnapshot(ctx, st, prefix, schemes)
	if err != nil {
		return err
	}
	ix.snap.Store(s)
	return nil
}

func buildSnapshot(ctx context.Context, st blob.Store, prefix string, schemes []string) (*snapshot, error) {
	want := map[string]bool{}
	for _, s := range schemes {
		want[s] = true
	}
	snap := &snapshot{schemes: map[string]map[string]*Term{}, search: map[string][]searchEntry{}, sidecar: map[string]*sidecarScheme{}}

	// Pass 1: collect the .nq inventory and any sidecar manifests.
	type nqFile struct {
		path string
		etag string
	}
	var nqs []nqFile
	manifests := map[string]*vocabsidecar.SidecarManifest{}
	for entry, err := range st.List(ctx, prefix) {
		if err != nil {
			return nil, fmt.Errorf("vocab: list authorities: %w", err)
		}
		switch {
		case strings.HasSuffix(entry.Path, ".nq"):
			nqs = append(nqs, nqFile{path: entry.Path, etag: entry.ETag})
		case strings.HasPrefix(entry.Path, prefix+vocabsidecar.DirPart) && strings.HasSuffix(entry.Path, vocabsidecar.ManifestSuffix):
			data, _, err := st.Get(ctx, entry.Path)
			if err != nil {
				return nil, fmt.Errorf("vocab: read %s: %w", entry.Path, err)
			}
			m := &vocabsidecar.SidecarManifest{}
			if err := json.Unmarshal(data, m); err != nil || m.Version != vocabsidecar.Version || m.Scheme == "" {
				slog.Warn("vocab: ignoring unreadable sidecar manifest", "path", entry.Path, "err", err)
				continue
			}
			if len(want) > 0 && !want[m.Scheme] {
				continue
			}
			manifests[m.Scheme] = m
		}
	}

	// Pass 2: parse every .nq except sources an ETag-matched manifest
	// covers; the deferred set replays if its scheme turns out dirty.
	deferred := map[string]nqFile{}
	sourceETags := map[string]string{}
	for _, f := range nqs {
		sourceETags[f.path] = f.etag
	}
	// parse indexes one file at most once: pass 3 replays a dirty scheme's
	// whole source, so two schemes sharing it must not each replay it --
	// addDataset appends alt labels, and a double parse would duplicate them.
	parsed := map[string]bool{}
	parse := func(f nqFile) error {
		if parsed[f.path] {
			return nil
		}
		parsed[f.path] = true
		data, _, err := st.Get(ctx, f.path)
		if err != nil {
			return fmt.Errorf("vocab: read %s: %w", f.path, err)
		}
		ds, err := rdf.ParseNQuads(data)
		if err != nil {
			return fmt.Errorf("vocab: parse %s: %w", f.path, err)
		}
		snap.addDataset(ds, want)
		return nil
	}
	armedFor := func(f nqFile) []*vocabsidecar.SidecarManifest {
		var out []*vocabsidecar.SidecarManifest
		for _, m := range manifests {
			if m.Source == f.path && m.SourceETag == f.etag {
				out = append(out, m)
			}
		}
		return out
	}
	for _, f := range nqs {
		armed := armedFor(f)
		// Skippable only when every scheme the file carries is armed on this
		// exact file version -- a shared source never drops a scheme. A
		// wanted-schemes filter narrows what "carried" must cover.
		skippable := len(armed) > 0
		for _, m := range armed {
			for _, s := range m.SourceSchemes {
				if len(want) > 0 && !want[s] {
					continue
				}
				ok := false
				for _, am := range armed {
					if am.Scheme == s {
						ok = true
						break
					}
				}
				if !ok {
					skippable = false
				}
			}
		}
		if skippable {
			for _, m := range armed {
				deferred[m.Scheme] = f
			}
			continue
		}
		if err := parse(f); err != nil {
			return nil, err
		}
	}

	// Pass 3: arm each manifest scheme whose sidecar is current.
	//
	// Loose quads for the scheme no longer disqualify it. A single live-picked
	// term cached under cache/<scheme>/ used to force the whole snapshot back
	// into resident maps -- 513k LCSH headings, +698MB, permanently, because
	// three accessors read the sidecar alone and the loader's only way to keep
	// them correct was to abandon the sidecar. Those accessors now merge the
	// sidecar with the map overlay, so a scheme carries both.
	//
	// A scheme that still falls back is a capacity event -- its whole snapshot
	// becomes resident -- so each fallback logs at WARN with the term count it
	// is about to cost. That turns an unexplained OOM at the next deploy into
	// a grep.
	for scheme, m := range manifests {
		if _, ok := deferred[scheme]; !ok || m.SourceETag != sourceETags[m.Source] {
			slog.Warn("vocab: sidecar stale; serving scheme from resident maps",
				"scheme", scheme, "source", m.Source, "terms", m.Terms)
			continue
		}
		sc, err := openSidecar(ctx, st, prefix, m)
		if err != nil {
			slog.Warn("vocab: sidecar open failed; serving scheme from resident maps",
				"scheme", scheme, "terms", m.Terms, "err", err)
			continue
		}
		snap.sidecar[scheme] = sc
	}
	// A deferred source whose scheme never armed must still be parsed: its
	// snapshot terms live nowhere else. parse is idempotent per path, so a
	// source shared by an armed and an unarmed scheme is read exactly once.
	for scheme, src := range deferred {
		if snap.sidecar[scheme] == nil {
			if err := parse(src); err != nil {
				return nil, err
			}
		}
	}
	// Parsing an unarmed scheme's source populates the maps of every scheme
	// that shares the file, including one that armed above. Such a scheme is
	// already fully resident, so its sidecar is pure overhead -- drop it. The
	// test is "was this scheme's own source parsed", not "are there any quads
	// for it": a cached live pick leaves quads and must not demote anything.
	for scheme := range snap.sidecar {
		if m := manifests[scheme]; parsed[m.Source] {
			slog.Warn("vocab: shared source replayed; serving scheme from resident maps",
				"scheme", scheme, "source", m.Source, "terms", m.Terms)
			delete(snap.sidecar, scheme)
		}
	}
	// Non-heading debris guard: a subject in an authority
	// graph carrying no labels at all is bookkeeping (a merge marker on an
	// absent node, a legacy authority:aliases tagAlias statement), not a
	// heading -- indexed, it shadows the term's real scheme on Resolve and
	// mints bogus schemes. Drop such terms, then schemes left empty.
	for scheme, terms := range snap.schemes {
		for id, t := range terms {
			if len(t.Labels) == 0 && len(t.AltLabels) == 0 {
				delete(terms, id)
			}
		}
		if len(terms) == 0 && snap.sidecar[scheme] == nil {
			delete(snap.schemes, scheme)
		}
	}
	snap.finish()
	return snap, nil
}

func (s *snapshot) addDataset(ds *rdf.Dataset, want map[string]bool) {
	for _, q := range ds.Quads {
		scheme, ok := strings.CutPrefix(q.G.Value, authorityGraphPrefix)
		if !ok || !q.S.IsIRI() {
			continue
		}
		if len(want) > 0 && !want[scheme] {
			continue
		}
		t := s.term(scheme, q.S.Value)
		switch q.P.Value {
		case skosPrefLabel:
			if q.O.IsLiteral() {
				t.Labels[q.O.Lang] = q.O.Value
			}
		case rdfsLabel:
			if q.O.IsLiteral() {
				if _, ok := t.Labels[q.O.Lang]; !ok {
					t.Labels[q.O.Lang] = q.O.Value
				}
			}
		case skosAltLabel:
			if q.O.IsLiteral() {
				if t.AltLabels == nil {
					t.AltLabels = map[string][]string{}
				}
				t.AltLabels[q.O.Lang] = append(t.AltLabels[q.O.Lang], q.O.Value)
			}
		case skosDefinition:
			if q.O.IsLiteral() {
				if _, ok := t.Definition[q.O.Lang]; !ok {
					if t.Definition == nil {
						t.Definition = map[string]string{}
					}
					t.Definition[q.O.Lang] = q.O.Value
				}
			}
		case skosBroader:
			if q.O.IsIRI() {
				t.Broader = appendUnique(t.Broader, q.O.Value)
			}
		case skosNarrower:
			if q.O.IsIRI() {
				t.Narrower = appendUnique(t.Narrower, q.O.Value)
			}
		case skosRelated:
			if q.O.IsIRI() {
				t.Related = appendUnique(t.Related, q.O.Value)
			}
		case skosExactMatch:
			if q.O.IsIRI() {
				t.ExactMatch = appendUnique(t.ExactMatch, q.O.Value)
			}
		case skosCloseMatch:
			if q.O.IsIRI() {
				t.CloseMatch = appendUnique(t.CloseMatch, q.O.Value)
			}
		case bibframe.PredMergedInto:
			if q.O.IsIRI() {
				t.MergedInto = q.O.Value
			}
		}
	}
}

func (s *snapshot) term(scheme, uri string) *Term {
	byURI := s.schemes[scheme]
	if byURI == nil {
		byURI = map[string]*Term{}
		s.schemes[scheme] = byURI
	}
	t := byURI[uri]
	if t == nil {
		t = &Term{Scheme: scheme, ID: uri, Labels: map[string]string{}}
		byURI[uri] = t
	}
	return t
}

// finish sorts relation lists and builds the per-scheme search slices and
// the identifier match tiers. Retired (merged) terms stay resolvable but get
// no search or match entries.
func (s *snapshot) finish() {
	s.buildMatch(0, func(t *Term) []string { return []string{t.ID} })
	s.buildMatch(1, func(t *Term) []string { return t.ExactMatch })
	s.buildMatch(2, func(t *Term) []string { return t.CloseMatch })
	for scheme, byURI := range s.schemes {
		var entries []searchEntry
		for uri, t := range byURI {
			sort.Strings(t.Broader)
			sort.Strings(t.Narrower)
			sort.Strings(t.Related)
			sort.Strings(t.ExactMatch)
			sort.Strings(t.CloseMatch)
			if t.MergedInto != "" {
				continue
			}
			seen := map[string]bool{}
			for _, l := range t.Labels {
				if n := normLabel(l); n != "" && !seen[n] {
					seen[n] = true
					entries = append(entries, searchEntry{norm: n, uri: uri})
				}
			}
			for _, alts := range t.AltLabels {
				for _, l := range alts {
					if n := normLabel(l); n != "" && !seen[n] {
						seen[n] = true
						entries = append(entries, searchEntry{norm: n, uri: uri, alt: true})
					}
				}
			}
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].norm != entries[j].norm {
				return entries[i].norm < entries[j].norm
			}
			return entries[i].uri < entries[j].uri
		})
		s.search[scheme] = entries
	}
}

// buildMatch registers one identifier tier of every live map-backed term.
// Tiers run strongest first (own ID, then exactMatch, then closeMatch) and
// MatchIdentifier checks them in order; within a tier, collisions resolve to
// the lexicographically smallest scheme then URI for determinism (sidecar
// schemes join the comparison at query time).
func (s *snapshot) buildMatch(k int, ids func(*Term) []string) {
	tier := map[string]*Term{}
	for _, byURI := range s.schemes {
		for _, t := range byURI {
			if t.MergedInto != "" {
				continue
			}
			for _, id := range ids(t) {
				key := canonIdentifier(id)
				if key == "" {
					continue
				}
				if prev, ok := tier[key]; !ok || t.Scheme < prev.Scheme || (t.Scheme == prev.Scheme && t.ID < prev.ID) {
					tier[key] = t
				}
			}
		}
	}
	s.matchTiers[k] = tier
}

// canonIdentifier folds an identifier URI to its match key: scheme prefix
// and trailing slash dropped, so http://d-nb.info/gnd/X and
// https://d-nb.info/gnd/X/ reconcile to the same term.
func canonIdentifier(uri string) string {
	uri = strings.TrimSpace(uri)
	uri = strings.TrimPrefix(uri, "https://")
	uri = strings.TrimPrefix(uri, "http://")
	return strings.TrimSuffix(uri, "/")
}

// MatchIdentifier returns the live term a canonicalized identifier URI
// resolves to: the term's own URI first, then its skos:exactMatch and
// closeMatch siblings -- the identifier-based reconciliation gate for
// external headings that carry $0. Within a tier, map and
// sidecar candidates resolve to the smallest scheme then URI.
func (ix *Index) MatchIdentifier(uri string) (*Term, bool) {
	key := canonIdentifier(uri)
	if key == "" {
		return nil, false
	}
	snap := ix.load()
	for k := range identifierTiers {
		best := snap.matchTiers[k][key]
		for _, sc := range snap.sidecarSorted() {
			t, ok := sc.tierMatch(k, key)
			if !ok {
				continue
			}
			if best == nil || t.Scheme < best.Scheme || (t.Scheme == best.Scheme && t.ID < best.ID) {
				best = t
			}
		}
		if best != nil {
			return best, true
		}
	}
	return nil, false
}

// sidecarSorted returns the sidecar schemes in scheme order, for
// deterministic cross-scheme resolution.
func (s *snapshot) sidecarSorted() []*sidecarScheme {
	if len(s.sidecar) == 0 {
		return nil
	}
	keys := make([]string, 0, len(s.sidecar))
	for k := range s.sidecar {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*sidecarScheme, len(keys))
	for i, k := range keys {
		out[i] = s.sidecar[k]
	}
	return out
}

// Schemes lists the loaded vocabulary keys, sorted.
func (ix *Index) Schemes() []string {
	return ix.load().schemeNames()
}

// SchemeStats reports how a scheme is served: sidecar is true when it is
// artifact-backed (its terms stay on disk), and residentTerms is the count held
// in resident maps -- the whole scheme for a map-backed one, or just the
// live-pick overlay for a sidecar-backed one. Together they turn an unexplained
// process RSS into a one-line answer per scheme. Nil-safe: a nil
// index reports (false, 0).
func (ix *Index) SchemeStats(scheme string) (sidecar bool, residentTerms int) {
	if ix == nil {
		return false, 0
	}
	s := ix.load()
	return s.sidecar[scheme] != nil, len(s.schemes[scheme])
}

// schemeNames is the sorted union of the two backends' scheme names.
//
// A scheme appears in both maps once it carries a sidecar and an overlay of
// live picks, so the union must dedupe. It did not have to before
// when a scheme served from exactly one backend; the admin SPA keys its vocab
// tabs by scheme name and a repeat crashed the picker.
func (s *snapshot) schemeNames() []string {
	out := make([]string, 0, len(s.schemes)+len(s.sidecar))
	for name := range s.schemes {
		out = append(out, name)
	}
	for name := range s.sidecar {
		if _, both := s.schemes[name]; !both {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Lookup returns the term by scheme and URI -- the validation gate: only
// terms that resolve here are accepted into suggestions or subject edits.
func (ix *Index) Lookup(scheme, id string) (*Term, bool) {
	snap := ix.load()
	if t, ok := snap.schemes[scheme][id]; ok {
		return t, true
	}
	if sc := snap.sidecar[scheme]; sc != nil {
		return sc.lookup(id)
	}
	return nil, false
}

// LabelResolver adapts the index to the editor's label-companion contract
// (editor.LabelResolver): term IRI -> (scheme, lang->prefLabel).
// Safe on a nil index -- it returns nil, which disables companions.
func (ix *Index) LabelResolver() func(iri string) (string, map[string]string, bool) {
	if ix == nil {
		return nil
	}
	return func(iri string) (string, map[string]string, bool) {
		t, ok := ix.Resolve(iri)
		if !ok {
			return "", nil, false
		}
		return t.Scheme, t.Labels, true
	}
}

// Resolve returns the term for a URI regardless of scheme (schemes checked
// in sorted order for determinism) -- the editor's chip renderer resolves
// stored subject references without knowing where they came from.
// A Homosaurus IRI whose release segment differs from the installed
// release's resolves through its version-stable homoit id;
// Lookup, the write-side validation gate, stays exact so edits store the
// installed release's canonical IRI.
func (ix *Index) Resolve(id string) (*Term, bool) {
	if t, ok := ix.resolveExact(id); ok {
		return t, true
	}
	for _, variant := range homosaurusVariants(id) {
		if t, ok := ix.resolveExact(variant); ok {
			return t, true
		}
	}
	return nil, false
}

// resolveExact is Resolve's exact-IRI pass across every scheme.
func (ix *Index) resolveExact(id string) (*Term, bool) {
	snap := ix.load()
	for _, s := range snap.schemeNames() {
		if t, ok := snap.schemes[s][id]; ok {
			return t, true
		}
		if sc := snap.sidecar[s]; sc != nil {
			if t, ok := sc.lookup(id); ok {
				return t, true
			}
		}
	}
	return nil, false
}

// homosaurusIRI captures a Homosaurus term IRI's release segment and its
// version-stable homoit id. Homosaurus mints a new /vN/ IRI family every
// release while the homoit ids persist, so feed data referencing an older
// release must still resolve against the installed one.
var homosaurusIRI = regexp.MustCompile(`^(https?://homosaurus\.org/)v(\d+)/(homoit\d+)$`)

// homosaurusVersionCap bounds the release-variant probe comfortably above
// Homosaurus's current release number.
const homosaurusVersionCap = 12

// homosaurusVariants returns the id rewritten to every other plausible
// Homosaurus release segment (empty for non-Homosaurus ids).
func homosaurusVariants(id string) []string {
	m := homosaurusIRI.FindStringSubmatch(id)
	if m == nil {
		return nil
	}
	variants := make([]string, 0, homosaurusVersionCap-1)
	for v := 1; v <= homosaurusVersionCap; v++ {
		if cand := fmt.Sprintf("%sv%d/%s", m[1], v, m[3]); cand != id {
			variants = append(variants, cand)
		}
	}
	return variants
}

// Terms returns every term of a scheme ordered by label -- the authorities
// management listing. Retired terms are included (marked by MergedInto).
func (ix *Index) Terms(scheme string) []*Term {
	snap := ix.load()
	// The map overlay is the live-pick cache when a sidecar is armed, and the
	// whole scheme when one is not. It comes first so that dedupe resolves a
	// term held by both backends to the overlay's record -- the precedence
	// Lookup has always applied, and Search and MatchLabel apply too.
	var out []*Term
	for _, t := range snap.schemes[scheme] {
		out = append(out, t)
	}
	if sc := snap.sidecar[scheme]; sc != nil {
		out = append(out, sc.all()...)
	}
	out = dedupeTerms(out, 0)
	sort.Slice(out, func(i, j int) bool {
		li, lj := normLabel(out[i].Label("en")), normLabel(out[j].Label("en"))
		if li != lj {
			return li < lj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Search returns up to limit terms whose pref or alt label (any language)
// starts with q, deduped, ordered by label.
func (ix *Index) Search(scheme, q string, limit int) []*Term {
	snap := ix.load()
	norm := normLabel(q)
	if norm == "" || limit <= 0 {
		return nil
	}
	sc := snap.sidecar[scheme]
	overlay := snap.searchMaps(scheme, norm, limit)
	if sc == nil {
		return hitTerms(overlay)
	}
	// A sidecar-backed scheme may also carry a map overlay: the terms a
	// cataloger picked from this scheme's live tab. Both streams
	// are ordered by matched-label norm, then URI, so merging them reproduces
	// exactly the order a wholly map-backed load of the same terms gives --
	// the parity the sidecar promises. Taking the overlay first on a tie makes
	// a cached pick shadow the snapshot's record, as Lookup already does.
	if len(overlay) == 0 {
		return sc.search(norm, limit)
	}
	return mergeHits(overlay, sc.searchHits(norm, limit), limit)
}

// searchHit is a search result together with the matched label's normalized
// form -- the sort key both backends order by.
type searchHit struct {
	term *Term
	norm string
}

func hitTerms(hits []searchHit) []*Term {
	if len(hits) == 0 {
		return nil
	}
	out := make([]*Term, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.term)
	}
	return out
}

// mergeHits merges two (norm, uri)-ordered streams into one, capping at limit.
//
// A term the overlay and the sidecar both hold is emitted once, from the
// overlay, at the overlay's position: a cached live pick is the fresher record
// and may carry a revised label, so letting the sidecar's copy win on the
// strength of an older label sorting earlier would contradict Lookup, which
// has always preferred the overlay. Right-hand hits are therefore skipped by
// ID rather than deduped positionally.
func mergeHits(left, right []searchHit, limit int) []*Term {
	if len(left) == 0 && len(right) == 0 {
		return nil
	}
	shadowed := make(map[string]bool, len(left))
	for _, l := range left {
		shadowed[l.term.ID] = true
	}
	out := make([]*Term, 0, limit)
	i, j := 0, 0
	for len(out) < limit {
		for j < len(right) && shadowed[right[j].term.ID] {
			j++
		}
		switch {
		case i < len(left) && j < len(right):
			l, r := left[i], right[j]
			if l.norm < r.norm || (l.norm == r.norm && l.term.ID < r.term.ID) {
				out = append(out, l.term)
				i++
			} else {
				out = append(out, r.term)
				j++
			}
		case i < len(left):
			out = append(out, left[i].term)
			i++
		case j < len(right):
			out = append(out, right[j].term)
			j++
		default:
			return out
		}
	}
	return out
}

// searchMaps is the prefix search over a scheme's resident terms.
func (s *snapshot) searchMaps(scheme, norm string, limit int) []searchHit {
	entries := s.search[scheme]
	start := sort.Search(len(entries), func(i int) bool { return entries[i].norm >= norm })
	var out []searchHit
	seen := map[string]bool{}
	for i := start; i < len(entries) && strings.HasPrefix(entries[i].norm, norm); i++ {
		uri := entries[i].uri
		if seen[uri] {
			continue
		}
		seen[uri] = true
		out = append(out, searchHit{term: s.schemes[scheme][uri], norm: entries[i].norm})
		if len(out) >= limit {
			break
		}
	}
	return out
}

// dedupeTerms drops repeated term IDs, keeping the first occurrence, and caps
// the result at limit (0 = no cap).
func dedupeTerms(in []*Term, limit int) []*Term {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, t := range in {
		if t == nil || seen[t.ID] {
			continue
		}
		seen[t.ID] = true
		out = append(out, t)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// Path returns the term's ancestor chain as TermRefs ordered root → … →
// direct parent (the term itself is excluded), following skos:broader.
// A polyhierarchical term takes the shortest chain to a root, ties broken
// by URI order; cycles and broader URIs missing from the scheme terminate
// the walk. A root term (or unknown term) yields nil.
func (ix *Index) Path(scheme, id string) []TermRef {
	snap := ix.load()
	get := snap.termGetter(scheme)
	if get == nil {
		return nil
	}
	start, ok := get(id)
	if !ok || start == nil {
		return nil
	}
	// BFS upward over broader edges: the first dequeued node with no
	// resolvable parent lies on a shortest chain. Broader lists are sorted
	// at load, so equal-length chains resolve to the smallest URIs.
	terms := map[string]*Term{id: start}
	prev := map[string]string{id: ""}
	queue := []string{id}
	root := ""
	for len(queue) > 0 && root == "" {
		cur := queue[0]
		queue = queue[1:]
		parents := 0
		for _, b := range terms[cur].Broader {
			bt, seen := terms[b]
			if !seen {
				bt, _ = get(b)
				terms[b] = bt
			}
			if bt == nil {
				continue
			}
			parents++
			if _, visited := prev[b]; !visited {
				prev[b] = cur
				queue = append(queue, b)
			}
		}
		if parents == 0 {
			root = cur
		}
	}
	if root == "" || root == id {
		return nil
	}
	var path []TermRef
	for cur := root; cur != id; cur = prev[cur] {
		path = append(path, TermRef{Scheme: scheme, ID: cur, Label: terms[cur].Label("en")})
	}
	return path
}

// ancestorsDepthCap bounds the Ancestors walk; real vocabularies top out far
// under it, and a malformed one must not stall a caller.
const ancestorsDepthCap = 12

// Ancestors returns the term's full transitive skos:broader closure (the
// term itself excluded), breadth-first so nearer ancestors come first,
// deterministic (broader lists are sorted at load), cycle-safe, and
// depth-capped. Unlike Path -- one shortest chain for breadcrumbs -- this is
// every ancestor: what a consumer materializing hierarchy metadata (ancestor
// term descriptions on enrichment/publish) needs for
// polyhierarchical terms. Broader URIs that do not resolve in the scheme are
// skipped. A root or unknown term yields nil.
func (ix *Index) Ancestors(scheme, id string) []*Term {
	snap := ix.load()
	get := snap.termGetter(scheme)
	if get == nil {
		return nil
	}
	start, ok := get(id)
	if !ok || start == nil {
		return nil
	}
	seen := map[string]bool{id: true}
	var out []*Term
	frontier := []*Term{start}
	for depth := 0; depth < ancestorsDepthCap && len(frontier) > 0; depth++ {
		var next []*Term
		for _, cur := range frontier {
			for _, b := range cur.Broader {
				if seen[b] {
					continue
				}
				seen[b] = true
				bt, ok := get(b)
				if !ok || bt == nil {
					continue
				}
				out = append(out, bt)
				next = append(next, bt)
			}
		}
		frontier = next
	}
	return out
}

// termGetter returns the scheme's term-by-URI accessor, or nil for an
// unknown scheme. The map path can never fail; the sidecar path reports
// read failures as misses.
func (s *snapshot) termGetter(scheme string) func(uri string) (*Term, bool) {
	if byURI := s.schemes[scheme]; byURI != nil {
		return func(uri string) (*Term, bool) {
			t, ok := byURI[uri]
			return t, ok
		}
	}
	if sc := s.sidecar[scheme]; sc != nil {
		return sc.lookup
	}
	return nil
}

// LabelMatch is one exact-label hit: the term plus whether the match came
// through an alt (used-for) label rather than a preferred label.
type LabelMatch struct {
	Term *Term
	Alt  bool
}

// MatchLabel returns the scheme's terms whose pref or alt label normalizes
// exactly to label -- the auto-linking gate: only whole-heading
// matches produce suggestions, never prefix guesses.
func (ix *Index) MatchLabel(scheme, label string) []LabelMatch {
	snap := ix.load()
	norm := normLabel(label)
	if norm == "" {
		return nil
	}
	out := snap.matchLabelMaps(scheme, norm)
	if sc := snap.sidecar[scheme]; sc != nil {
		// The overlay and the sidecar can both hold the term. Auto-linking
		// gates on whole-heading matches, so a duplicate would suggest the
		// same link twice.
		seen := make(map[string]bool, len(out))
		for _, m := range out {
			seen[m.Term.ID] = true
		}
		for _, m := range sc.matchLabel(norm) {
			if !seen[m.Term.ID] {
				out = append(out, m)
			}
		}
	}
	return out
}

// matchLabelMaps is the exact-normalized-label gate over resident terms.
func (s *snapshot) matchLabelMaps(scheme, norm string) []LabelMatch {
	entries := s.search[scheme]
	start := sort.Search(len(entries), func(i int) bool { return entries[i].norm >= norm })
	var out []LabelMatch
	seen := map[string]bool{}
	for i := start; i < len(entries) && entries[i].norm == norm; i++ {
		if seen[entries[i].uri] {
			continue
		}
		seen[entries[i].uri] = true
		out = append(out, LabelMatch{Term: s.schemes[scheme][entries[i].uri], Alt: entries[i].alt})
	}
	return out
}

func normLabel(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

func appendUnique(list []string, v string) []string {
	if slices.Contains(list, v) {
		return list
	}
	return append(list, v)
}
