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
	type groupKey struct{ via, scheme string }
	groups := map[groupKey][]int{}
	terms := make([]*Term, len(cands))
	for i, c := range cands {
		if t, ok := ix.Resolve(c.id); ok {
			terms[i] = t
			k := groupKey{via: c.via, scheme: t.Scheme}
			groups[k] = append(groups[k], i)
		}
	}

	drop := map[int]bool{}
	demote := map[int]bool{}
	for _, members := range groups {
		if len(members) < 2 {
			continue // no sibling evidence, nothing to judge
		}
		matched := map[int]bool{}
		for _, i := range members {
			if formsOverlap(srcForms, labelForms(terms[i])) {
				matched[i] = true
			}
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
