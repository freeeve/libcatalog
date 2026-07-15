package diversity

import (
	"maps"
	"strings"
)

// SubjectRef is one of a work's subjects as the audit sees it: an authority URI
// (may be empty for a bare-string ILS heading), its heading labels (may be empty
// for a URI-only reference), and the vocabulary scheme code ("homosaurus",
// "fast", "lcsh"; may be empty). Every dimension feeds the crosswalk.
type SubjectRef struct {
	URI    string
	Labels []string
	Scheme string
	// Langs are the label languages this subject's controlled term is reachable
	// in -- the subset of the audit's configured languages for which the term
	// carries a prefLabel or altLabel. It drives the per-language subject-label
	// coverage columns (heading reachability, NOT the book's own language). It is
	// empty for an uncontrolled heading (no URI, so no term to consult).
	Langs []string
}

// Report is a coverage-first content-diversity audit of a corpus. It reports what
// the works are *about*, never who created them. Every category share is stated
// against an explicit denominator so undercounting is visible rather than hidden:
// CoveredWorks (works that carry any subject at all) is the honest base for
// representation, while ShareTotal shows the dilution across the whole corpus,
// including works no one has subjected yet.
type Report struct {
	// TotalWorks is every work the audit saw.
	TotalWorks int `json:"totalWorks"`
	// CoveredWorks is the works carrying at least one subject (URI or label) --
	// the only works that can be categorized, and the honest denominator.
	CoveredWorks int `json:"coveredWorks"`
	// Coverage is CoveredWorks/TotalWorks in [0,1]. A low value means the audit
	// speaks for only part of the collection; read every category share with it.
	Coverage float64 `json:"coverage"`
	// TotalWeight is the sum of the per-work weights across every work (0 when
	// the caller supplied no weights). It lets the report be read by collection
	// depth (e.g. copies held) rather than title count; omitted when zero.
	TotalWeight int `json:"totalWeight,omitempty"`
	// Categories are the diversity categories in the crosswalk's reporting order.
	Categories []CategoryTally `json:"categories"`
	// Multiplicity decomposes CoveredWorks exclusively -- how many covered
	// works matched no category, exactly one, or two or more. Unlike the
	// per-category tallies (which overlap), these three sum with the
	// uncovered remainder to TotalWorks, so they stack honestly in a
	// composition chart; MatchedMulti doubles as the intersectionality
	// signal.
	Multiplicity MultiplicityTally `json:"multiplicity"`
}

// MultiplicityTally is the exclusive covered-works decomposition.
type MultiplicityTally struct {
	Uncategorized int `json:"uncategorized"`
	MatchedOne    int `json:"matchedOne"`
	MatchedMulti  int `json:"matchedMulti"`
}

// CategoryTally is one category's representation. Works counts each work once,
// however many of its subjects matched. ShareCovered is Works/CoveredWorks (the
// share among works that could be categorized at all); ShareTotal is
// Works/TotalWorks (the share of the whole corpus).
type CategoryTally struct {
	ID           string  `json:"id"`
	Label        string  `json:"label"`
	Works        int     `json:"works"`
	ShareCovered float64 `json:"shareCovered"`
	ShareTotal   float64 `json:"shareTotal"`
	// Weight is the sum of the per-work weights of this category's works (e.g.
	// copies held), so a category can be read by collection depth rather than
	// title count. Omitted when zero (no weights supplied).
	Weight int `json:"weight,omitempty"`
	// LabelLangWorks counts, per configured subject-label language, how many of
	// this category's works reached it through at least one controlled term
	// carrying a label in that language -- subject-heading reachability, NOT the
	// book's own language. A work counts once per language however many of its
	// subjects carry it. The baseline language (usually "en") approaches Works;
	// the gap to it is the collection's controlled-heading coverage in the other
	// languages. Absent for a corpus audited with no configured languages.
	LabelLangWorks map[string]int `json:"labelLangWorks,omitempty"`
	// Benchmark/BenchmarkSource pass the operator's comparison share through
	// from the crosswalk, when one was configured. The tool never grades the
	// delta: a share against a benchmark is only as good as the coverage
	// above it, and interpreting the gap is the librarian's call.
	Benchmark       *float64 `json:"benchmark,omitempty"`
	BenchmarkSource string   `json:"benchmarkSource,omitempty"`
}

// Auditor streams works into a coverage-first content-diversity tally. Build it
// once from a crosswalk, Add each work's subjects, then read Report. It is not safe
// for concurrent Add.
type Auditor struct {
	cw           *Crosswalk
	total        int
	covered      int
	multi        MultiplicityTally
	perCat       map[string]int
	perCatLang   map[string]map[string]int
	weightTotal  int
	perCatWeight map[string]int
}

// NewAuditor returns an Auditor over the given crosswalk.
func NewAuditor(cw *Crosswalk) *Auditor {
	return &Auditor{cw: cw, perCat: map[string]int{}, perCatLang: map[string]map[string]int{}, perCatWeight: map[string]int{}}
}

// Add folds one work's subjects into the tally with no weight. See AddWeighted.
func (a *Auditor) Add(subjects []SubjectRef) {
	a.AddWeighted(subjects, 0)
}

// AddWeighted folds one work's subjects into the tally, also accumulating an
// opaque per-work weight (e.g. copies held) alongside the title count. A work
// counts toward CoveredWorks when it carries at least one subject with a URI or
// a non-empty label, and toward a category once when any of its subjects maps
// there; its weight adds to each matched category and to the corpus total. A
// work with no usable subjects contributes only to TotalWorks (and TotalWeight)
// -- it dilutes coverage, which is the point of reporting coverage.
func (a *Auditor) AddWeighted(subjects []SubjectRef, weight int) {
	a.total++
	a.weightTotal += weight
	covered := false
	cats := map[string]bool{}
	// catLangs holds, per category this work reached, the set of subject-label
	// languages it was reachable through, so the per-language tally counts a work
	// once per language however many of its subjects carry that language.
	catLangs := map[string]map[string]bool{}
	note := func(id string, langs []string) {
		cats[id] = true
		if len(langs) == 0 {
			return
		}
		seen := catLangs[id]
		if seen == nil {
			seen = map[string]bool{}
			catLangs[id] = seen
		}
		for _, l := range langs {
			seen[l] = true
		}
	}
	for _, s := range subjects {
		if s.URI != "" {
			covered = true
			for _, id := range a.cw.Categorize(s.URI, "", s.Scheme) {
				note(id, s.Langs)
			}
		}
		for _, l := range s.Labels {
			if strings.TrimSpace(l) == "" {
				continue
			}
			covered = true
			for _, id := range a.cw.Categorize("", l, "") {
				note(id, s.Langs)
			}
		}
	}
	if covered {
		a.covered++
		switch len(cats) {
		case 0:
			a.multi.Uncategorized++
		case 1:
			a.multi.MatchedOne++
		default:
			a.multi.MatchedMulti++
		}
	}
	for id := range cats {
		a.perCat[id]++
		a.perCatWeight[id] += weight
	}
	for id, langs := range catLangs {
		perLang := a.perCatLang[id]
		if perLang == nil {
			perLang = map[string]int{}
			a.perCatLang[id] = perLang
		}
		for l := range langs {
			perLang[l]++
		}
	}
}

// Report snapshots the tally as a coverage-first Report, with categories in the
// crosswalk's stable reporting order. Shares are 0 when their denominator is 0.
func (a *Auditor) Report() Report {
	r := Report{TotalWorks: a.total, CoveredWorks: a.covered, TotalWeight: a.weightTotal, Multiplicity: a.multi}
	if a.total > 0 {
		r.Coverage = float64(a.covered) / float64(a.total)
	}
	for _, c := range a.cw.Categories() {
		works := a.perCat[c.ID]
		var langWorks map[string]int
		if perLang := a.perCatLang[c.ID]; len(perLang) > 0 {
			langWorks = make(map[string]int, len(perLang))
			maps.Copy(langWorks, perLang)
		}
		t := CategoryTally{ID: c.ID, Label: c.Label, Works: works, Weight: a.perCatWeight[c.ID], LabelLangWorks: langWorks, Benchmark: c.Benchmark, BenchmarkSource: c.BenchmarkSource}
		if a.covered > 0 {
			t.ShareCovered = float64(t.Works) / float64(a.covered)
		}
		if a.total > 0 {
			t.ShareTotal = float64(t.Works) / float64(a.total)
		}
		r.Categories = append(r.Categories, t)
	}
	return r
}
