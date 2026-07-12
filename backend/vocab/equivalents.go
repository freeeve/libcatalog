package vocab

import (
	"sort"
	"strings"
)

// Equivalent is one cross-scheme equivalent of a term, found through
// skos:exactMatch/closeMatch links in either direction, or through a one-hop
// pivot (two terms linking the same intermediate URI -- the FAST -> LCSH <-
// Homosaurus shape). Strength names the weakest hop, because a suggestion is
// only as good as its shakiest link; the editor must say which.
type Equivalent struct {
	ID     string            `json:"id"`
	Scheme string            `json:"scheme,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
	// Strength: "exact" | "close" (direct links) | "pivot-exact" |
	// "pivot-close" (transitive; the weaker hop names it).
	Strength string `json:"strength"`
	// Via is the pivot URI for transitive equivalents.
	Via string `json:"via,omitempty"`
	// Known is false when the URI is only a link target -- not a term in any
	// loaded vocabulary. Still reported: an LCSH link is meaningful even when
	// LCSH is not snapshot locally.
	Known bool `json:"known"`
}

// strengthRank orders strengths for dedupe (keep the strongest) and display.
func strengthRank(s string) int {
	switch s {
	case "exact":
		return 4
	case "close":
		return 3
	case "pivot-exact":
		return 2
	case "pivot-close":
		return 1
	}
	return 0
}

// pivotStrength is the transitive strength: exact only when both hops are.
func pivotStrength(h1, h2 string) string {
	if h1 == "exact" && h2 == "exact" {
		return "pivot-exact"
	}
	return "pivot-close"
}

// buildReverse indexes every live map-backed term under each of its match
// targets, multi-valued -- unlike matchTiers, which keeps one winner per key
// for reconciliation, equivalents needs every term that links a given URI
// (two schemes bridging the same LCSH heading is the point, not a collision).
// Sidecar-backed schemes contribute inbound through their reverse-match
// artifact instead (sidecar.go revMatch); artifact sets built before it
// existed degrade to outbound and pivot lookups for that scheme.
func (s *snapshot) buildReverse() {
	s.revExact = map[string][]*Term{}
	s.revClose = map[string][]*Term{}
	for _, byURI := range s.schemes {
		for _, t := range byURI {
			if t.MergedInto != "" {
				continue
			}
			for _, u := range t.ExactMatch {
				if key := canonIdentifier(u); key != "" {
					s.revExact[key] = append(s.revExact[key], t)
				}
			}
			for _, u := range t.CloseMatch {
				if key := canonIdentifier(u); key != "" {
					s.revClose[key] = append(s.revClose[key], t)
				}
			}
		}
	}
}

// Equivalents returns a term's cross-scheme equivalents in strength order:
// direct outbound links, direct inbound links (terms whose matches point at
// it -- the load-bearing direction for community vocabularies that link TO
// LCSH), and one-hop pivots in both shapes (shared link target; a linked
// term's other links). The source term itself is excluded; duplicates keep
// their strongest strength. False is returned when the URI is not a term in
// any loaded vocabulary.
func (ix *Index) Equivalents(uri string) ([]Equivalent, bool) {
	src, ok := ix.Resolve(uri)
	if !ok {
		return nil, false
	}
	snap := ix.load()
	srcKey := canonIdentifier(src.ID)

	found := map[string]*Equivalent{} // canon URI -> best equivalent
	add := func(id, strength, via string) {
		key := canonIdentifier(id)
		if key == "" || key == srcKey {
			return
		}
		if have, ok := found[key]; ok && strengthRank(have.Strength) >= strengthRank(strength) {
			return
		}
		eq := &Equivalent{ID: id, Strength: strength, Via: via}
		if t, ok := ix.Resolve(id); ok {
			eq.ID, eq.Scheme, eq.Labels, eq.Known = t.ID, t.Scheme, t.Labels, true
		}
		found[key] = eq
	}

	// srcLinks pairs each of the source's outbound link URIs with its hop
	// strength; inbound pairs each linking term with the strength it used.
	type hop struct {
		uri, strength string
	}
	var srcLinks []hop
	for _, u := range src.ExactMatch {
		srcLinks = append(srcLinks, hop{u, "exact"})
	}
	for _, u := range src.CloseMatch {
		srcLinks = append(srcLinks, hop{u, "close"})
	}
	type inHop struct {
		t        *Term
		strength string
	}
	var inbound []inHop
	for _, t := range snap.revExact[srcKey] {
		inbound = append(inbound, inHop{t, "exact"})
	}
	for _, t := range snap.revClose[srcKey] {
		inbound = append(inbound, inHop{t, "close"})
	}
	// Sidecar-backed schemes contribute inbound through their reverse-match
	// artifact (absent on pre-artifact builds: they degrade to outbound and
	// pivots only).
	for _, strength := range []string{"exact", "close"} {
		for _, sc := range snap.sidecarSorted() {
			for _, uri := range sc.revMatch(strength, srcKey) {
				if t, ok := sc.lookup(uri); ok && t.MergedInto == "" {
					inbound = append(inbound, inHop{t, strength})
				}
			}
		}
	}

	// Direct, both directions.
	for _, h := range srcLinks {
		add(h.uri, h.strength, "")
	}
	for _, in := range inbound {
		add(in.t.ID, in.strength, "")
	}
	// Pivots collect as candidates first: match links are NOT transitive,
	// so a shared node proves nothing by itself, and guardPivots decides
	// what emits and at what strength (task 420's over-reach class:
	// broad + narrow terms sharing one broad LCSH heading).
	var pivots []pivotCand
	pivot := func(id, strength, via string) {
		pivots = append(pivots, pivotCand{id: id, strength: strength, via: via})
	}
	// Pivot shape 1 -- shared target: src -> I <- sibling.
	for _, h := range srcLinks {
		key := canonIdentifier(h.uri)
		for _, sib := range snap.revExact[key] {
			pivot(sib.ID, pivotStrength(h.strength, "exact"), h.uri)
		}
		for _, sib := range snap.revClose[key] {
			pivot(sib.ID, pivotStrength(h.strength, "close"), h.uri)
		}
		for _, strength := range []string{"exact", "close"} {
			for _, sc := range snap.sidecarSorted() {
				for _, uri := range sc.revMatch(strength, key) {
					pivot(uri, pivotStrength(h.strength, strength), h.uri)
				}
			}
		}
	}
	// Pivot shape 2 -- a linking term's other links: sibling <- I -> src,
	// where I is a loaded term pointing at both.
	for _, in := range inbound {
		for _, u := range in.t.ExactMatch {
			pivot(u, pivotStrength(in.strength, "exact"), in.t.ID)
		}
		for _, u := range in.t.CloseMatch {
			pivot(u, pivotStrength(in.strength, "close"), in.t.ID)
		}
	}
	// Pivot shape 3 -- a linked term's onward links: src -> I -> sibling,
	// where I is a loaded term (e.g. LCSH is snapshot and carries its own
	// matches outward).
	for _, h := range srcLinks {
		mid, ok := ix.Resolve(h.uri)
		if !ok {
			continue
		}
		for _, u := range mid.ExactMatch {
			pivot(u, pivotStrength(h.strength, "exact"), mid.ID)
		}
		for _, u := range mid.CloseMatch {
			pivot(u, pivotStrength(h.strength, "close"), mid.ID)
		}
	}
	for _, p := range guardPivots(ix, src, pivots) {
		add(p.id, p.strength, p.via)
	}

	out := make([]Equivalent, 0, len(found))
	for _, eq := range found {
		out = append(out, *eq)
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := strengthRank(out[i].Strength), strengthRank(out[j].Strength); a != b {
			return a > b
		}
		if out[i].Scheme != out[j].Scheme {
			return out[i].Scheme < out[j].Scheme
		}
		return strings.Compare(out[i].ID, out[j].ID) < 0
	})
	return out, true
}
