package project

import (
	"maps"
	"slices"
	"sort"
	"strings"
)

// Merge unions per-feed projections by work id. Project views one
// provenance graph at a time (feed:<provider> + editorial:), so a multi-feed
// corpus projects each feed separately and merges here: earlier catalogs win a
// shared id -- list the primary feed first, its records are richer than a
// sidecar's -- and the result is sorted by id like Project's output. The input
// catalog.nq must cover every feed's works; after a multi-feed ingest, `lcat
// serialize` regenerates it, since each ingest run rewrites catalog.nq with
// only its own run's works.
//
// The vocabulary sideband (Catalog.Terms) merges by term id,
// field-wise: labels fill per language (earlier catalogs win a language, like
// works win an id), broader edges union, the first non-empty scheme sticks --
// per-feed projections describe the same authority term from different
// coverage, so the union is at least as rich as any one input.
func Merge(cats []*Catalog) *Catalog {
	merged := &Catalog{Version: SchemaVersion}
	seen := map[string]bool{}
	terms := map[string]*Term{}
	for _, c := range cats {
		for _, w := range c.Works {
			if seen[w.ID] {
				continue
			}
			seen[w.ID] = true
			merged.Works = append(merged.Works, w)
		}
		for _, t := range c.Terms {
			cur := terms[t.ID]
			if cur == nil {
				// Private copies: later collisions fill labels/broader in
				// place, and the inputs must stay untouched.
				terms[t.ID] = &Term{ID: t.ID, Scheme: t.Scheme, Labels: maps.Clone(t.Labels), Broader: slices.Clone(t.Broader)}
				continue
			}
			for lang, label := range t.Labels {
				if _, ok := cur.Labels[lang]; !ok {
					if cur.Labels == nil {
						cur.Labels = map[string]string{}
					}
					cur.Labels[lang] = label
				}
			}
			for _, parent := range t.Broader {
				if !slices.Contains(cur.Broader, parent) {
					cur.Broader = append(cur.Broader, parent)
				}
			}
			if cur.Scheme == "" {
				cur.Scheme = t.Scheme
			}
		}
	}
	sort.Slice(merged.Works, func(i, j int) bool { return merged.Works[i].ID < merged.Works[j].ID })
	for _, id := range slices.Sorted(maps.Keys(terms)) {
		t := *terms[id]
		sort.Strings(t.Broader)
		merged.Terms = append(merged.Terms, t)
	}
	return merged
}

// SanitizeSources rewrites every Work's extra "sources" attribution list to
// the public allowlist, dropping the key when nothing public remains, and
// returns how many attributions were stripped. Provenance under
// lcat:extra/sources may name sources whose attribution must not leak on a
// public surface; the same allowlist governs the nq download (export
// package). Values are comma-joined by convention and compared trimmed; kept
// values are re-joined ", " and keep their order.
func SanitizeSources(cat *Catalog, public map[string]bool) int {
	stripped := 0
	for i := range cat.Works {
		e := cat.Works[i].Extra
		raw, ok := e["sources"]
		if !ok {
			continue
		}
		var kept []string
		for s := range strings.SplitSeq(raw, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if public[s] {
				kept = append(kept, s)
			} else {
				stripped++
			}
		}
		if len(kept) == 0 {
			delete(e, "sources")
		} else {
			e["sources"] = strings.Join(kept, ", ")
		}
	}
	return stripped
}

// SanitizeExtras drops every Work extra whose key is not on the public
// allowlist, and returns how many values it removed.
//
// `lcat:extra/*` is the adopter passthrough, so it carries whatever the ingest
// mapping put there -- including institution-private holdings ("this library
// already has it"), acquisition flags, and internal notes. Those belong in the
// grains, which is where cataloguers work, and not on the public face. The two
// faces project from the same grains by design, so the only place to draw the
// line is here.
//
// "sources" is exempt: SanitizeSources governs it, filtering *within* the value
// rather than dropping the key, and running both would let public-extras silently
// undo a configured public-sources allowlist.
//
// The grains are untouched. This is a projection-time filter, exactly as
// SanitizeSources is.
func SanitizeExtras(cat *Catalog, public map[string]bool) int {
	stripped := 0
	for i := range cat.Works {
		e := cat.Works[i].Extra
		for key := range e {
			if key == extraSourcesKey || public[key] {
				continue
			}
			delete(e, key)
			stripped++
		}
		if len(e) == 0 {
			cat.Works[i].Extra = nil
		}
	}
	return stripped
}

// extraSourcesKey is the one extra SanitizeExtras must never drop: it has its own
// allowlist, and it is filtered by value rather than by key.
const extraSourcesKey = "sources"

// SourceSet parses a comma-separated name list into an allowlist set. It backs
// both `public-sources` (source attributions, filtered by SanitizeSources and the
// export nq filter) and `public-extras` (extra keys, filtered by SanitizeExtras).
// Names are trimmed; empty entries are ignored, so "" yields an empty
// (strip-everything) set.
func SourceSet(csv string) map[string]bool {
	set := map[string]bool{}
	for s := range strings.SplitSeq(csv, ",") {
		if s = strings.TrimSpace(s); s != "" {
			set[s] = true
		}
	}
	return set
}
