// Package diversity maps a work's controlled subjects to diversity-audit
// categories. It is the "content demographics" half of the
// diversity-audit feature: what works are *about*, derived from their subject
// headings. Creator demographics are a separate, opt-in axis.
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
// it: an exact authority-URI match, a whole-word/phrase match on a subject's
// heading label (tolerating plural inflections of each keyword), or a
// whole-vocabulary match by scheme code (e.g. every homosaurus-scheme subject is
// LGBTQIA+-relevant). The taxonomy is an editorial choice; this is its data shape.
type Category struct {
	ID       string   `toml:"id" json:"id"`
	Label    string   `toml:"label" json:"label,omitempty"`
	Keywords []string `toml:"keywords" json:"keywords,omitempty"`
	URIs     []string `toml:"uris" json:"uris,omitempty"`
	Schemes  []string `toml:"schemes" json:"schemes,omitempty"`
	// Roots are authority URIs whose transitive skos:narrower closure joins
	// the category -- "these URIs plus everything beneath them", expanded at
	// audit time from the loaded scheme (WithNarrower), so the facet
	// self-maintains as the vocabulary gains terms. A small CURATED set, not
	// one URI: closure only descends, so siblings and high parents each need
	// their own root; and concept/-ism terms with no broader edge stay the
	// keywords' job. Roots union with uris and keywords.
	Roots []string `toml:"roots" json:"roots,omitempty"`
	// Benchmark is an operator-supplied comparison share in [0,1] (service-area
	// demographics, publishing output, or the collection's own baseline), with
	// BenchmarkSource naming where it came from ("ACS 2024 service area",
	// "CCBC 2025"). The seed ships none: there is no standard target percentage,
	// and a benchmark without a named source is a number pretending to be a goal.
	Benchmark       *float64 `toml:"benchmark,omitempty" json:"benchmark,omitempty"`
	BenchmarkSource string   `toml:"benchmarkSource,omitempty" json:"benchmarkSource,omitempty"`
}

// crosswalkFile is the on-disk/embedded TOML shape: an array of categories.
type crosswalkFile struct {
	Category []Category `toml:"category"`
}

// Crosswalk categorizes subjects. It is built once (from the seed plus any operator
// overrides) and is read-only thereafter, so it is safe for concurrent use.
type Crosswalk struct {
	categories []Category            // seed order, then appended new override ids -- stable for reporting
	full       []Category            // same order, with merged keyword/uri/scheme detail
	index      map[string]int        // category id -> position in categories
	byURI      map[string][]string   // exact subject URI -> category ids (seed order)
	byScheme   map[string][]string   // vocabulary scheme code -> category ids (seed order)
	keywords   map[string][][]string // category id -> keyword token sequences
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

// FromBytes returns the crosswalk built from the embedded seed with each override
// document (raw TOML bytes) merged over it in order -- Load for callers whose
// override lives somewhere other than a file, e.g. a persisted server-side
// override.
func FromBytes(overrides ...[]byte) (*Crosswalk, error) {
	return build(seedTOML, overrides)
}

// Seed returns the embedded seed's categories with full matching detail, for
// surfaces that present the built-in taxonomy alongside an operator's override.
func Seed() []Category {
	var f crosswalkFile
	if err := toml.Unmarshal(seedTOML, &f); err != nil {
		panic("diversity: embedded seed crosswalk is invalid: " + err.Error())
	}
	return copyCategories(f.Category)
}

// ParseCategories parses one crosswalk TOML document into its categories,
// validating that every category names an id. It does not merge with the seed;
// use FromBytes to validate the full merged build.
func ParseCategories(data []byte) ([]Category, error) {
	var f crosswalkFile
	if err := toml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("diversity: parse crosswalk: %w", err)
	}
	for i, c := range f.Category {
		if strings.TrimSpace(c.ID) == "" {
			return nil, fmt.Errorf("diversity: category %d is missing its id", i)
		}
		if err := validBenchmark(c); err != nil {
			return nil, fmt.Errorf("diversity: category %d: %w", i, err)
		}
	}
	return f.Category, nil
}

// validBenchmark checks a category's operator benchmark: a share in [0,1],
// and never a bare number -- a benchmark without a named source reads as a
// target the tool endorsed rather than data the operator chose.
func validBenchmark(c Category) error {
	if c.Benchmark == nil {
		if c.BenchmarkSource != "" {
			return fmt.Errorf("category %q names a benchmarkSource without a benchmark", c.ID)
		}
		return nil
	}
	// The comparison form matters: every ordered comparison with NaN is
	// false, so "< 0 || > 1" would wave NaN through -- and one NaN poisons
	// every JSON encode that later touches the tally.
	if !(*c.Benchmark >= 0 && *c.Benchmark <= 1) {
		return fmt.Errorf("category %q benchmark %v is not a share in [0,1]", c.ID, *c.Benchmark)
	}
	if strings.TrimSpace(c.BenchmarkSource) == "" {
		return fmt.Errorf("category %q has a benchmark but no benchmarkSource naming where it came from", c.ID)
	}
	return nil
}

// EncodeCategories renders categories as a crosswalk TOML document -- the same
// format Load reads and `lcat diversity-audit --crosswalk` takes, so a
// server-persisted override stays portable to the CLI.
func EncodeCategories(cats []Category) ([]byte, error) {
	data, err := toml.Marshal(crosswalkFile{Category: cats})
	if err != nil {
		return nil, fmt.Errorf("diversity: encode crosswalk: %w", err)
	}
	return data, nil
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
			if err := validBenchmark(oc); err != nil {
				return nil, fmt.Errorf("override %d: %w", oi, err)
			}
			if i, ok := pos[oc.ID]; ok {
				merged.Category[i].Keywords = unionFold(merged.Category[i].Keywords, oc.Keywords)
				merged.Category[i].URIs = unionExact(merged.Category[i].URIs, oc.URIs)
				merged.Category[i].Schemes = unionFold(merged.Category[i].Schemes, oc.Schemes)
				merged.Category[i].Roots = unionExact(merged.Category[i].Roots, oc.Roots)
				if oc.Label != "" {
					merged.Category[i].Label = oc.Label
				}
				if oc.Benchmark != nil {
					merged.Category[i].Benchmark = oc.Benchmark
					merged.Category[i].BenchmarkSource = oc.BenchmarkSource
				}
				continue
			}
			pos[oc.ID] = len(merged.Category)
			merged.Category = append(merged.Category, oc)
		}
	}

	cw := &Crosswalk{
		full:     copyCategories(merged.Category),
		index:    map[string]int{},
		byURI:    map[string][]string{},
		byScheme: map[string][]string{},
		keywords: map[string][][]string{},
	}
	for i, c := range merged.Category {
		cw.categories = append(cw.categories, Category{ID: c.ID, Label: c.Label, Benchmark: c.Benchmark, BenchmarkSource: c.BenchmarkSource})
		cw.index[c.ID] = i
		seqs := make([][]string, 0, len(c.Keywords))
		for _, k := range c.Keywords {
			if toks := tokens(k); len(toks) > 0 {
				seqs = append(seqs, toks)
			}
		}
		cw.keywords[c.ID] = seqs
		for _, u := range c.URIs {
			if u = strings.TrimSpace(u); u != "" {
				cw.byURI[u] = append(cw.byURI[u], c.ID)
			}
		}
		// A root is itself a URI match; its descendants join via
		// WithNarrower when a hierarchy source is available.
		for _, u := range c.Roots {
			if u = strings.TrimSpace(u); u != "" && !contains(cw.byURI[u], c.ID) {
				cw.byURI[u] = append(cw.byURI[u], c.ID)
			}
		}
		for _, s := range c.Schemes {
			if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
				cw.byScheme[s] = append(cw.byScheme[s], c.ID)
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

// Definitions returns the categories in stable reporting order WITH their merged
// matching detail (keywords, uris, schemes) -- the shape a crosswalk editor
// presents. The copy is deep, so callers cannot mutate the crosswalk.
func (c *Crosswalk) Definitions() []Category {
	return copyCategories(c.full)
}

// copyCategories deep-copies categories including their slices.
func copyCategories(cats []Category) []Category {
	out := make([]Category, len(cats))
	for i, c := range cats {
		out[i] = Category{
			ID:              c.ID,
			Label:           c.Label,
			Keywords:        append([]string(nil), c.Keywords...),
			URIs:            append([]string(nil), c.URIs...),
			Schemes:         append([]string(nil), c.Schemes...),
			Roots:           append([]string(nil), c.Roots...),
			BenchmarkSource: c.BenchmarkSource,
		}
		if c.Benchmark != nil {
			b := *c.Benchmark
			out[i].Benchmark = &b
		}
	}
	return out
}

// WithNarrower returns a crosswalk whose categories' root sets are expanded
// through the transitive skos:narrower closure the resolver describes: for
// each category root, every descendant URI matches the category exactly as
// an explicit uris entry would. The receiver is untouched (it stays safe
// for concurrent use); a nil resolver, or a crosswalk with no roots,
// returns the receiver itself. Polyhierarchy and cycles are fine -- closure
// membership is a set.
func (c *Crosswalk) WithNarrower(narrower func(uri string) []string) *Crosswalk {
	if narrower == nil {
		return c
	}
	hasRoots := false
	for _, cat := range c.full {
		if len(cat.Roots) > 0 {
			hasRoots = true
			break
		}
	}
	if !hasRoots {
		return c
	}
	out := &Crosswalk{
		categories: c.categories,
		full:       c.full,
		index:      c.index,
		byScheme:   c.byScheme,
		keywords:   c.keywords,
		byURI:      make(map[string][]string, len(c.byURI)),
	}
	for u, ids := range c.byURI {
		out.byURI[u] = ids
	}
	for _, cat := range c.full {
		if len(cat.Roots) == 0 {
			continue
		}
		visited := map[string]bool{}
		queue := append([]string(nil), cat.Roots...)
		for len(queue) > 0 {
			u := queue[0]
			queue = queue[1:]
			if u == "" || visited[u] {
				continue
			}
			visited[u] = true
			if !contains(out.byURI[u], cat.ID) {
				// Clone-on-extend: the slice may be shared with the receiver.
				out.byURI[u] = append(append([]string(nil), out.byURI[u]...), cat.ID)
			}
			queue = append(queue, narrower(u)...)
		}
	}
	return out
}

// contains reports whether list holds v.
func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Label returns a category's display label, or the id itself if unknown.
func (c *Crosswalk) Label(id string) string {
	if i, ok := c.index[id]; ok {
		return c.categories[i].Label
	}
	return id
}

// Categorize returns the ids of every category a single subject maps to, in stable
// reporting order. A subject matches a category by exact authority URI, by its
// vocabulary scheme code (e.g. every homosaurus-scheme subject), or by
// a whole-word/phrase match of any of the category's keywords against the heading
// label, tolerating plural inflections. Passing "" for an argument skips that
// dimension.
func (c *Crosswalk) Categorize(uri, label, scheme string) []string {
	hit := map[string]bool{}
	if uri != "" {
		for _, id := range c.byURI[uri] {
			hit[id] = true
		}
	}
	if scheme != "" {
		for _, id := range c.byScheme[strings.ToLower(scheme)] {
			hit[id] = true
		}
	}
	if label != "" {
		toks := tokens(label)
		for id, seqs := range c.keywords {
			if hit[id] {
				continue
			}
			for _, seq := range seqs {
				if containsSeq(toks, seq) {
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
// caller uses to count a work once per matched category.
func (c *Crosswalk) CategorizeSubjects(subjects []SubjectRef) []string {
	hit := map[string]bool{}
	for _, s := range subjects {
		for _, id := range c.Categorize(s.URI, "", s.Scheme) {
			hit[id] = true
		}
		for _, l := range s.Labels {
			for _, id := range c.Categorize("", l, "") {
				hit[id] = true
			}
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

// tokens lowercases s and splits it into alphanumeric word tokens ("Drag queens
// (Fiction)" -> ["drag","queens","fiction"]; "2SLGBTQ" stays one token). Keywords
// and heading labels tokenize the same way, so matching is punctuation-blind.
func tokens(s string) []string {
	s = strings.ToLower(s)
	var out []string
	start := -1
	for i := 0; i < len(s); i++ {
		if isAlnum(s[i]) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			out = append(out, s[start:i])
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, s[start:])
	}
	return out
}

// containsSeq reports whether the keyword token sequence seq occurs contiguously in
// the heading tokens, where each heading token may also be the plural inflection of
// its keyword token: "Lesbians" matches keyword "lesbian", "Drag
// queens" matches "drag queen". The tolerance is one-directional -- a plural
// keyword does not match a singular heading -- and deliberately shallow (-s/-es
// only), so "Gaya" still never matches "gay" and "Poore" never matches "poor".
func containsSeq(heading, seq []string) bool {
	if len(seq) == 0 || len(seq) > len(heading) {
		return false
	}
	for i := 0; i+len(seq) <= len(heading); i++ {
		ok := true
		for j, kw := range seq {
			if !tokenMatches(heading[i+j], kw) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// tokenMatches reports whether a heading token equals a keyword token or its
// plural inflection (keyword+"s" or keyword+"es").
func tokenMatches(heading, kw string) bool {
	return heading == kw || heading == kw+"s" || heading == kw+"es"
}

// isAlnum reports whether b is an ASCII letter or digit.
func isAlnum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
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
