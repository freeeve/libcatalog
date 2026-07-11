// Package diversity maps a work's controlled subjects to diversity-audit
// categories (libcat tasks/365). It is the "content demographics" half of the
// diversity-audit feature: what works are *about*, derived from their subject
// headings. Creator demographics are a separate, opt-in axis (tasks/368).
//
// The crosswalk is data-driven from an embedded TOML seed, which an operator may
// override with their own file. Matching a subject to a category is a statement
// about the work's topic, never about the identity of its creators.
package diversity

import (
	_ "embed"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// seedTOML is the shipped default crosswalk, embedded so the categorizer is
// data-driven without a runtime file dependency. The golden test guards its shape.
//
//go:embed crosswalk.toml
var seedTOML []byte

// Category is one diversity-audit category and the controlled subjects that map to
// it: an exact authority-URI match or a whole-word/phrase match on a subject's
// heading label. The taxonomy is an editorial choice; this is just its data shape.
type Category struct {
	ID       string   `toml:"id"`
	Label    string   `toml:"label"`
	Keywords []string `toml:"keywords"`
	URIs     []string `toml:"uris"`
}

// crosswalkFile is the on-disk/embedded TOML shape: an array of categories.
type crosswalkFile struct {
	Category []Category `toml:"category"`
}

// Crosswalk categorizes subjects. It is built once (from the seed plus any operator
// overrides) and is read-only thereafter, so it is safe for concurrent use.
type Crosswalk struct {
	categories []Category          // seed order, then appended new override ids -- stable for reporting
	index      map[string]int      // category id -> position in categories
	byURI      map[string][]string // exact subject URI -> category ids (seed order)
	keywords   map[string][]string // category id -> lowercased keywords
}

// Default returns the crosswalk built from the embedded seed alone. A malformed
// seed is a programming error (the embedded file is guarded by a golden test), so
// it panics rather than returning a silently empty categorizer.
func Default() *Crosswalk {
	cw, err := build(seedTOML, nil)
	if err != nil {
		panic("diversity: embedded seed crosswalk is invalid: " + err.Error())
	}
	return cw
}

// Load returns the crosswalk built from the embedded seed with each override file
// merged over it in order. Merge is by category id: an override category unions its
// keywords and uris onto the matching seed category and, when it sets a non-empty
// label, replaces the label; an override category with a new id is appended. A
// missing or malformed override file is an error -- an operator who points libcat
// at a crosswalk expects it to be honored, not silently ignored.
func Load(overridePaths ...string) (*Crosswalk, error) {
	var overrides [][]byte
	for _, p := range overridePaths {
		if p == "" {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("diversity: read override %s: %w", p, err)
		}
		overrides = append(overrides, data)
	}
	return build(seedTOML, overrides)
}

// build parses the seed and overrides and compiles the lookup indexes.
func build(seed []byte, overrides [][]byte) (*Crosswalk, error) {
	var merged crosswalkFile
	if err := toml.Unmarshal(seed, &merged); err != nil {
		return nil, fmt.Errorf("parse seed: %w", err)
	}
	pos := map[string]int{}
	for i, c := range merged.Category {
		pos[c.ID] = i
	}
	for oi, data := range overrides {
		var over crosswalkFile
		if err := toml.Unmarshal(data, &over); err != nil {
			return nil, fmt.Errorf("parse override %d: %w", oi, err)
		}
		for _, oc := range over.Category {
			if oc.ID == "" {
				return nil, fmt.Errorf("override %d: a category is missing its id", oi)
			}
			if i, ok := pos[oc.ID]; ok {
				merged.Category[i].Keywords = unionFold(merged.Category[i].Keywords, oc.Keywords)
				merged.Category[i].URIs = unionExact(merged.Category[i].URIs, oc.URIs)
				if oc.Label != "" {
					merged.Category[i].Label = oc.Label
				}
				continue
			}
			pos[oc.ID] = len(merged.Category)
			merged.Category = append(merged.Category, oc)
		}
	}

	cw := &Crosswalk{
		index:    map[string]int{},
		byURI:    map[string][]string{},
		keywords: map[string][]string{},
	}
	for i, c := range merged.Category {
		cw.categories = append(cw.categories, Category{ID: c.ID, Label: c.Label})
		cw.index[c.ID] = i
		lows := make([]string, 0, len(c.Keywords))
		for _, k := range c.Keywords {
			if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
				lows = append(lows, k)
			}
		}
		cw.keywords[c.ID] = lows
		for _, u := range c.URIs {
			if u = strings.TrimSpace(u); u != "" {
				cw.byURI[u] = append(cw.byURI[u], c.ID)
			}
		}
	}
	return cw, nil
}

// Categories returns the categories in stable reporting order (seed order, then any
// operator-added categories). Keyword/URI detail is omitted; this is for labeling
// aggregated counts.
func (c *Crosswalk) Categories() []Category {
	out := make([]Category, len(c.categories))
	copy(out, c.categories)
	return out
}

// Label returns a category's display label, or the id itself if unknown.
func (c *Crosswalk) Label(id string) string {
	if i, ok := c.index[id]; ok {
		return c.categories[i].Label
	}
	return id
}

// Categorize returns the ids of every category a single subject maps to, in stable
// reporting order. A subject matches a category by exact authority URI (when uri is
// non-empty) or by a whole-word/phrase match of any of the category's keywords
// against the heading label. Passing "" for either argument skips that dimension.
func (c *Crosswalk) Categorize(uri, label string) []string {
	hit := map[string]bool{}
	if uri != "" {
		for _, id := range c.byURI[uri] {
			hit[id] = true
		}
	}
	if label != "" {
		low := strings.ToLower(label)
		for id, kws := range c.keywords {
			if hit[id] {
				continue
			}
			for _, kw := range kws {
				if containsWord(low, kw) {
					hit[id] = true
					break
				}
			}
		}
	}
	return c.order(hit)
}

// CategorizeSubjects returns the ids of every category any of the given subjects
// maps to, deduplicated and in stable reporting order -- the work-level roll-up a
// caller (tasks/366) uses to count a work once per matched category. Each subject is
// a (uri, label) pair; either may be empty.
func (c *Crosswalk) CategorizeSubjects(subjects []struct{ URI, Label string }) []string {
	hit := map[string]bool{}
	for _, s := range subjects {
		for _, id := range c.Categorize(s.URI, s.Label) {
			hit[id] = true
		}
	}
	return c.order(hit)
}

// order returns the hit set as category ids in stable reporting order.
func (c *Crosswalk) order(hit map[string]bool) []string {
	if len(hit) == 0 {
		return nil
	}
	out := make([]string, 0, len(hit))
	for id := range hit {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return c.index[out[i]] < c.index[out[j]] })
	return out
}

// containsWord reports whether needle occurs in haystack (both already lowercased)
// bounded by non-letter characters or string edges, so "gay" matches "gay pride"
// but not "gaya" and "poor" does not match "poore". Multi-word needles are matched
// as a contiguous phrase; only the phrase's outer edges are boundary-checked.
func containsWord(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	from := 0
	for {
		i := strings.Index(haystack[from:], needle)
		if i < 0 {
			return false
		}
		start := from + i
		end := start + len(needle)
		if !letterBefore(haystack, start) && !letterAfter(haystack, end) {
			return true
		}
		from = start + 1
		if from >= len(haystack) {
			return false
		}
	}
}

// letterBefore reports whether the byte just before pos is an ASCII letter.
func letterBefore(s string, pos int) bool {
	return pos > 0 && isLetter(s[pos-1])
}

// letterAfter reports whether the byte at pos is an ASCII letter.
func letterAfter(s string, pos int) bool {
	return pos < len(s) && isLetter(s[pos])
}

// isLetter reports whether b is an ASCII letter (headings are matched lowercased,
// but guard both cases so the boundary test is independent of prior normalization).
func isLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// unionFold appends to base every value of add whose lowercased form is not already
// present (case-insensitive), preserving order.
func unionFold(base, add []string) []string {
	seen := map[string]bool{}
	for _, b := range base {
		seen[strings.ToLower(strings.TrimSpace(b))] = true
	}
	for _, a := range add {
		if k := strings.ToLower(strings.TrimSpace(a)); k != "" && !seen[k] {
			seen[k] = true
			base = append(base, a)
		}
	}
	return base
}

// unionExact appends to base every value of add not already present (exact),
// preserving order.
func unionExact(base, add []string) []string {
	seen := map[string]bool{}
	for _, b := range base {
		seen[strings.TrimSpace(b)] = true
	}
	for _, a := range add {
		if k := strings.TrimSpace(a); k != "" && !seen[k] {
			seen[k] = true
			base = append(base, a)
		}
	}
	return base
}
