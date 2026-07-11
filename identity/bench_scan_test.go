package identity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func benchGrains(b *testing.B) [][]byte {
	b.Helper()
	root := os.Getenv("LCAT_BENCH_GRAINS")
	if root == "" {
		b.Skip("LCAT_BENCH_GRAINS not set")
	}
	var grains [][]byte
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".nq") || len(grains) >= 2000 {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		grains = append(grains, data)
		return nil
	})
	if err != nil {
		b.Fatal(err)
	}
	if len(grains) == 0 {
		b.Fatal("no grains under LCAT_BENCH_GRAINS")
	}
	return grains
}

// BenchmarkScanGrain covers the identity sweep run per grain at workindex
// boot/refresh and by editor doc assembly.
func BenchmarkScanGrain(b *testing.B) {
	grains := benchGrains(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		if _, err := ScanGrain(grains[i%len(grains)]); err != nil {
			b.Fatal(err)
		}
	}
}
