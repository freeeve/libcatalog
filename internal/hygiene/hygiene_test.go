// Package hygiene holds repo-wide invariants that no single package owns.
package hygiene

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Extensions that must be plain text. Binary fixtures (MARC records, PNGs,
// wasm) are excluded by not appearing here, rather than by a denylist that
// would have to grow with every new fixture.
var textExtensions = map[string]bool{
	".go": true, ".ts": true, ".js": true, ".mjs": true, ".cjs": true,
	".svelte": true, ".css": true, ".html": true, ".json": true,
	".md": true, ".yaml": true, ".yml": true, ".toml": true, ".sh": true,
	".sql": true, ".txt": true, ".nq": true, ".nt": true, ".ttl": true,
}

// TestNoControlBytesInTextFiles fails on a raw control byte in a tracked text
// file.
//
// A single NUL in backend/ui/src/lib/api.ts made grep, git diff, git grep, and
// most review UIs treat the whole 870-line file as binary. Nothing failed: it
// compiled, the SPA worked, svelte-check was clean. The damage was that `grep
// runBatch api.ts` printed nothing and exited 1, which reads as "the symbol is
// not there" rather than "I did not look" -- and a reader acted on that, and
// went off to test the wrong endpoint.
//
// Tab, LF and CR are the control bytes text legitimately contains.
func TestNoControlBytesInTextFiles(t *testing.T) {
	// The test runs in its own directory; git must list the whole tree.
	const root = "../.."
	out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		t.Skipf("git ls-files: %v", err)
	}
	var checked int
	for _, name := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if name == "" || !textExtensions[strings.ToLower(filepath.Ext(name))] {
			continue
		}
		// The built SPA bundle is generated, minified, and not read by hand.
		if strings.HasPrefix(name, "backend/ui/dist/") {
			continue
		}
		path := filepath.Join(root, name)
		data, err := readIfExists(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if data == nil {
			continue
		}
		checked++
		if i := indexControlByte(data); i >= 0 {
			t.Errorf("%s: control byte %#02x at offset %d makes the file binary to grep and git diff; write it as an escape, or use a printable separator",
				name, data[i], i)
		}
	}
	if checked < 100 {
		t.Fatalf("only %d text files checked; the walk is not finding the tree", checked)
	}
}

// readIfExists reads path, returning nil when the file is tracked but absent
// from the working tree (a staged deletion).
func readIfExists(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// indexControlByte returns the offset of the first control byte that is not
// tab, LF or CR, or -1.
func indexControlByte(data []byte) int {
	return bytes.IndexFunc(data, func(r rune) bool {
		switch r {
		case '\t', '\n', '\r':
			return false
		}
		return r < 0x20 || r == 0x7f
	})
}
