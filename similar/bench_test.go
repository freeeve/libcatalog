package similar

import (
	"fmt"
	"testing"
)

// synthCatalog builds n Works with a realistic attribute spread: a long tail of
// subjects, a shorter tail of contributors, a few tags, and series on a tenth.
// The DF cap and the singleton floor both bite here, which is the point -- a
// benchmark over unique attributes would measure an empty walk.
func synthCatalog(n int) []Work {
	works := make([]Work, 0, n)
	for i := range n {
		s := Work{
			WorkID: fmt.Sprintf("w%08d", i),
			Subjects: []string{
				fmt.Sprintf("s:%d", i%(n/20+1)), // ~20 works per subject
				fmt.Sprintf("s:%d", i%97),       // a denser band
			},
			Contributors: []string{fmt.Sprintf("Author %d", i%(n/8+1))},
			Tags:         []string{fmt.Sprintf("t:%d", i%50)},
			Languages:    []string{"eng"},
		}
		if i%10 == 0 {
			s.Series = []string{fmt.Sprintf("Series %d", i%(n/40+1))}
		}
		works = append(works, s)
	}
	return works
}

func BenchmarkBuild(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 62_602} {
		works := synthCatalog(n)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				Build(works, DefaultOptions())
			}
		})
	}
}

func BenchmarkNeighbors(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 62_602} {
		ix := Build(synthCatalog(n), DefaultOptions())
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				ix.Neighbors(fmt.Sprintf("w%08d", i%n), 8)
				i++
			}
		})
	}
}

// The OPAC build step needs neighbours for every Work, so this is the number
// that decides whether the sidecar is precomputable.
func BenchmarkNeighborsWholeCatalog(b *testing.B) {
	const n = 10_000
	works := synthCatalog(n)
	ix := Build(works, DefaultOptions())
	b.ReportAllocs()
	for b.Loop() {
		for _, w := range works {
			ix.Neighbors(w.WorkID, 8)
		}
	}
}
