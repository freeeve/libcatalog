package vocab

import (
	"strings"
)

// pivotCand is one transitive equivalent before the guards rule on it.
type pivotCand struct {
	id, strength, via string
}

// guardPivots applies the anti-over-reach rules to pivot candidates.
// exactMatch is not transitive: two terms sharing a link node are only as
// equivalent as the node is specific, and a broad heading (LCSH "Women",
// "Minorities") collects both its true counterpart and its narrower or
// variant terms. The evidence is the claimant set itself -- guards fire per
// (pivot node, candidate scheme) group:
//
//   - Label-matching candidates (normalized, plural-tolerant, alt labels
//     included) keep full pivot strength: they are the node's true
//     counterpart ("Same-sex marriage" -> "Same-sex marriage",
//     "Masculinity" -> "Masculinities").
//   - A candidate whose group sibling is its ANCESTOR is subtree
//     over-reach and drops: the sibling owns the node, the descendant is a
//     narrower identity ("Women" node claimed by both "Women" and its
//     narrower "Womyn" -- "Womyn" never suggests off plain "Women").
//   - When several claimants survive, the node is a hub, not an identity:
//     non-matching survivors demote one tier (pivot-exact -> pivot-close;
//     an already-weak pivot-close drops).
//
// Candidates outside any loaded vocabulary pass through untouched -- they
// are display-only bridges, never suggestions.
func guardPivots(ix *Index, src *Term, cands []pivotCand) []pivotCand {
	if len(cands) == 0 {
		return cands
	}
	srcForms := labelForms(src)
	srcKey := canonIdentifier(src.ID)
	type groupKey struct{ via, scheme string }
	groups := map[groupKey][]int{}
	terms := make([]*Term, len(cands))
	for i, c := range cands {
		// The source shows up among its own node's claimants (emission
		// excludes it later); it must not count as sibling evidence or
		// fan-in here.
		if canonIdentifier(c.id) == srcKey {
			continue
		}
		if t, ok := ix.Resolve(c.id); ok {
			terms[i] = t
			k := groupKey{via: c.via, scheme: t.Scheme}
			groups[k] = append(groups[k], i)
		}
	}

	matched := map[int]bool{}
	for i, t := range terms {
		if t != nil && formsOverlap(srcForms, labelForms(t)) {
			matched[i] = true
		}
	}

	drop := map[int]bool{}
	demote := map[int]bool{}
	for _, members := range groups {
		if len(members) < 2 {
			continue // no same-scheme sibling evidence at this node
		}
		for _, i := range members {
			if matched[i] {
				continue
			}
			for _, j := range members {
				if i != j && isAncestorTerm(ix, terms[i], terms[j]) {
					drop[i] = true
					break
				}
			}
		}
		survivors := 0
		for _, i := range members {
			if !drop[i] {
				survivors++
			}
		}
		if survivors < 2 {
			continue
		}
		for _, i := range members {
			if !drop[i] && !matched[i] {
				demote[i] = true
			}
		}
	}

	// Cross-scheme rules per pivot node (task 423: the reverse direction --
	// the SOURCE can be the narrow end, and a node's breadth shows in its
	// total fan-in, not just one scheme's claimants).
	srcAncestors := map[string]bool{}
	for _, a := range ix.Ancestors(src.Scheme, src.ID) {
		srcAncestors[a.ID] = true
	}
	byVia := map[string][]int{}
	for i, c := range cands {
		if terms[i] != nil {
			byVia[c.via] = append(byVia[c.via], i)
		}
	}
	for _, members := range byVia {
		// Source-narrow: a via-sibling that is the source's OWN ancestor
		// means the source claims this node from below (homosaurus "Womyn"
		// exactMatch LCSH "Women" while its broader "Women" claims the same
		// node) -- the node equates to the ancestor, not to the source, so
		// nothing non-matching may pivot through it.
		srcNarrow := false
		distinct := map[string]bool{}
		for _, i := range members {
			distinct[terms[i].ID] = true
			if terms[i].Scheme == src.Scheme && srcAncestors[terms[i].ID] {
				srcNarrow = true
			}
		}
		// Fan-in counts the source itself: three or more distinct terms on
		// one node is a broad heading by evidence, whatever their schemes.
		hub := len(distinct)+1 >= 3
		for _, i := range members {
			if matched[i] {
				continue
			}
			switch {
			case srcNarrow:
				drop[i] = true
			case hub:
				demote[i] = true
			}
		}
	}

	// Label-counterpart rule (task 425): in a sparse vocab load -- two
	// schemes, the pivot node's own vocabulary absent -- fan-in and sibling
	// evidence starve, and a lone bad claimant sails through (FAST "Women"
	// -> bare LCSH node <- Homosaurus "Womyn", sole claimant). The signal
	// that survives sparseness: the candidate's OWN scheme already holds a
	// term whose label matches the source. When that counterpart exists and
	// does not claim the pivot node itself, the pivot asserts an equivalence
	// the target vocabulary deliberately does not draw -- drop it. When the
	// counterpart co-claims the node (Homosaurus "Same-sex marriage" on the
	// LCSH node that also yields "Lesbian couples"), the extra claimant is
	// an adjacent concept: demote, keep it reviewable.
	srcLabels := termLabels(src)
	counterparts := map[string]*Term{}
	counterpart := func(scheme string) *Term {
		if cp, ok := counterparts[scheme]; ok {
			return cp
		}
		var found *Term
		for _, l := range srcLabels {
			for _, m := range ix.MatchLabel(scheme, l) {
				if m.Term.MergedInto == "" && canonIdentifier(m.Term.ID) != srcKey {
					found = m.Term
					break
				}
			}
			if found != nil {
				break
			}
		}
		counterparts[scheme] = found
		return found
	}
	claimsNode := func(t *Term, via string) bool {
		key := canonIdentifier(via)
		for _, u := range t.ExactMatch {
			if canonIdentifier(u) == key {
				return true
			}
		}
		for _, u := range t.CloseMatch {
			if canonIdentifier(u) == key {
				return true
			}
		}
		return false
	}
	for i, c := range cands {
		if terms[i] == nil || matched[i] || drop[i] {
			continue
		}
		cp := counterpart(terms[i].Scheme)
		if cp == nil || cp.ID == terms[i].ID {
			continue
		}
		if claimsNode(cp, c.via) {
			demote[i] = true
		} else {
			drop[i] = true
		}
	}

	out := make([]pivotCand, 0, len(cands))
	for i, c := range cands {
		if drop[i] {
			continue
		}
		if demote[i] {
			if c.strength != "pivot-exact" {
				continue // a demoted pivot-close is coincidence-grade noise
			}
			c.strength = "pivot-close"
		}
		out = append(out, c)
	}
	return out
}

// termLabels flattens a term's preferred and alternate labels (raw, every
// language) for whole-label counterpart lookups.
func termLabels(t *Term) []string {
	var out []string
	for _, l := range t.Labels {
		out = append(out, l)
	}
	for _, alts := range t.AltLabels {
		out = append(out, alts...)
	}
	return out
}

// isAncestorTerm reports whether anc sits on child's skos:broader chain
// (same scheme; Ancestors already breaks cycles).
func isAncestorTerm(ix *Index, child, anc *Term) bool {
	if child == nil || anc == nil || child.Scheme != anc.Scheme {
		return false
	}
	for _, a := range ix.Ancestors(child.Scheme, child.ID) {
		if a.ID == anc.ID {
			return true
		}
	}
	return false
}

// labelForms folds a term's preferred AND alternate labels (every language)
// into normalized comparison forms.
func labelForms(t *Term) map[string]bool {
	forms := map[string]bool{}
	if t == nil {
		return forms
	}
	addForm := func(s string) {
		if n := pivotLabelNorm(s); n != "" {
			forms[n] = true
		}
	}
	for _, l := range t.Labels {
		addForm(l)
	}
	for _, alts := range t.AltLabels {
		for _, l := range alts {
			addForm(l)
		}
	}
	return forms
}

// formsOverlap reports whether any label form matches across the two sets,
// tolerating shallow plural inflection per token ("Masculinity" matches
// "Masculinities"; "Women" does NOT match "Womyn" -- different words, which
// is exactly why that pair needs the structural guard instead).
func formsOverlap(a, b map[string]bool) bool {
	for fa := range a {
		if b[fa] {
			return true
		}
		for fb := range b {
			if pluralTolerantEqual(fa, fb) {
				return true
			}
		}
	}
	return false
}

// pivotLabelNorm lowercases and reduces a label to space-joined alphanumeric
// tokens, so punctuation and casing never block a match.
func pivotLabelNorm(s string) string {
	var b strings.Builder
	lastSpace := true
	for _, r := range strings.ToLower(s) {
		alnum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		switch {
		case alnum:
			b.WriteRune(r)
			lastSpace = false
		case !lastSpace:
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

// pluralTolerantEqual compares token sequences allowing each pair to differ
// by a trailing -s/-es/-ies inflection in either direction.
func pluralTolerantEqual(a, b string) bool {
	ta, tb := strings.Fields(a), strings.Fields(b)
	if len(ta) != len(tb) {
		return false
	}
	for i := range ta {
		if ta[i] == tb[i] || singular(ta[i]) == singular(tb[i]) {
			continue
		}
		return false
	}
	return true
}

// singular strips a shallow plural inflection: -ies -> -y, -es, -s.
func singular(w string) string {
	switch {
	case len(w) > 3 && strings.HasSuffix(w, "ies"):
		return w[:len(w)-3] + "y"
	case len(w) > 3 && strings.HasSuffix(w, "es"):
		return w[:len(w)-2]
	case len(w) > 2 && strings.HasSuffix(w, "s"):
		return w[:len(w)-1]
	}
	return w
}
