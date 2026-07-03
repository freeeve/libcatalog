package project

import (
	"os"
	"testing"
)

// benchCorpus loads the benchmark corpus: point LCAT_BENCH_CATALOG at a real
// catalog.nq (e.g. the 5,659-work QLL corpus) for representative numbers;
// without it the benchmark skips rather than flattering itself on a toy file.
//
//	LCAT_BENCH_CATALOG=/path/catalog.nq go test ./project/ -bench . -benchmem
func benchCorpus(b *testing.B) []byte {
	b.Helper()
	path := os.Getenv("LCAT_BENCH_CATALOG")
	if path == "" {
		b.Skip("LCAT_BENCH_CATALOG not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}
	return data
}

func BenchmarkProject(b *testing.B) {
	data := benchCorpus(b)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		cat, err := Project(data, "overdrive")
		if err != nil {
			b.Fatal(err)
		}
		if len(cat.Works) == 0 {
			b.Fatal("empty projection")
		}
	}
}

func BenchmarkFacets(b *testing.B) {
	data := benchCorpus(b)
	cat, err := Project(data, "overdrive")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		f := cat.Facets()
		if len(f.Languages) == 0 {
			b.Fatal("empty facets")
		}
	}
}
