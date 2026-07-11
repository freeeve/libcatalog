package project

import (
	"fmt"
	"testing"
)

// synthCatalog mirrors similar/bench_test.go's attribute spread -- a subject on
// ~20 works, a denser band on ~645, an author on ~8, series on a tenth -- so the
// DF cap and the singleton floor both bite, and the dense band dominates the
// per-query cost the way it does on a real catalog.
func synthCatalog(n int) *Catalog {
	works := make([]Work, 0, n)
	for i := range n {
		w := Work{
			ID:    fmt.Sprintf("w%08d", i),
			Title: fmt.Sprintf("Work %d", i),
			Subjects: []Subject{
				{ID: fmt.Sprintf("s:%d", i%(n/20+1))},
				{ID: fmt.Sprintf("s:%d", i%97)},
			},
			Contributors: []Contributor{{Name: fmt.Sprintf("Author %d", i%(n/8+1))}},
			Tags:         []string{fmt.Sprintf("t:%d", i%50)},
			Languages:    []string{"en"},
		}
		if i%10 == 0 {
			w.Instances = []Instance{{ID: w.ID + "i"}}
			w.Series = []Series{{Title: fmt.Sprintf("Series %d", i%(n/100+1))}}
		}
		works = append(works, w)
	}
	return &Catalog{Version: SchemaVersion, Works: works}
}

// BenchmarkCatalogSimilar measures the whole-catalog precompute `lcat project`
// runs -- Build once, then Neighbors for every Work, in parallel. The serial cost
// at 62,602 Works is minutes; this is the number that decides whether the sidecar
// belongs in the build step at all.
func BenchmarkCatalogSimilar(b *testing.B) {
	for _, n := range []int{1000, 10000, 62602} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			cat := synthCatalog(n)
			b.ResetTimer()
			for b.Loop() {
				if idx := cat.Similar(8); len(idx.Works) == 0 {
					b.Fatal("no neighbours at all; the fixture stopped exercising the walk")
				}
			}
		})
	}
}
