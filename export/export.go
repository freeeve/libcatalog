// Package export derives the downloadable catalog artifacts from an ingest
// output root (tasks/172): catalog.nq.gz is the corpus itself -- the catalog.nq
// found there, which every writer now emits as the merge of the grains, with
// blank labels namespaced by work id (tasks/291, tasks/298) -- and
// catalog.mrc.gz / catalog.xml.gz are per-grain MARC round-trips via
// bibframe.DecodeGrainMARC, which honors editorial override shadows and
// verbatim sidecars (fidelity bounded by docs/marc-fidelity.md). A Manifest
// records sizes, sha256 digests, and record counts for a downloads page.
// This is the static-site counterpart of the backend's on-demand export
// service (backend/export); both read the same grains.
package export

import (
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/freeeve/libcat/bibframe"
	codex "github.com/freeeve/libcodex"
	"github.com/freeeve/libcodex/iso2709"
	"github.com/freeeve/libcodex/marcxml"
)

// Options configures one export run.
type Options struct {
	// In is the ingest output root: the directory holding catalog.nq and
	// data/works/ grains.
	In string
	// Out is the directory the gzipped artifacts are written to.
	Out string
	// PublicSources, when non-nil, is the allowlist of extra/sources names
	// permitted in the nq download; other attributions are stripped, matching
	// project.SanitizeSources on the catalog.json side, so no public surface
	// discloses more provenance than the site does. Nil keeps every source.
	// The on-disk graph of record stays complete either way.
	PublicSources map[string]bool
	// Log receives skip and strip warnings; nil means os.Stderr.
	Log io.Writer
	// OrgCode is the deployment's MARC organization code; when set, the
	// MARC download derives each record's 040 from graph facts at decode
	// time (tasks/192).
	OrgCode string
	// CoversOut, when set, copies uploaded cover images (data/covers/ under
	// In) to this directory as flat files, the site-relative covers/ URLs
	// the editorial lcat:extra/cover statements point at (tasks/215).
	// Empty skips the copy.
	CoversOut string
}

// Manifest is the downloads-page data file: what was generated when, from how
// many works, with per-artifact integrity and record counts.
type Manifest struct {
	Generated string `json:"generated"`
	Works     int    `json:"works"`
	Files     []File `json:"files"`
}

// File is one artifact's manifest entry; the sha256 is over the compressed
// bytes (what a downloader verifies).
type File struct {
	Name    string `json:"name"`
	Bytes   int64  `json:"bytes"`
	SHA256  string `json:"sha256"`
	Records int    `json:"records"`
}

// Run exports the corpus under opts.In to gzipped download artifacts under
// opts.Out and returns their manifest.
func Run(opts Options) (*Manifest, error) {
	if opts.Log == nil {
		opts.Log = os.Stderr
	}
	if err := os.MkdirAll(opts.Out, 0o755); err != nil {
		return nil, err
	}
	grains, err := grainPaths(opts.In)
	if err != nil {
		return nil, err
	}
	if len(grains) == 0 {
		return nil, fmt.Errorf("export: no grains under %s", opts.In)
	}

	var files []File
	nq, err := copyGzip(filepath.Join(opts.In, "catalog.nq"), filepath.Join(opts.Out, "catalog.nq.gz"), opts.PublicSources, opts.Log)
	if err != nil {
		return nil, err
	}
	nq.Records = len(grains) // one Work per grain
	files = append(files, nq)

	if err := copyCovers(opts.In, opts.CoversOut); err != nil {
		return nil, err
	}
	mrc, xml, err := emitMARC(grains, opts.Out, opts.Log, opts.OrgCode)
	if err != nil {
		return nil, err
	}
	files = append(files, mrc, xml)

	return &Manifest{
		Generated: time.Now().UTC().Format(time.RFC3339),
		Works:     len(grains),
		Files:     files,
	}, nil
}

// grainPaths lists the work grain files under root in deterministic (lexical)
// order, so exports are stable across runs.
func grainPaths(root string) ([]string, error) {
	var paths []string
	dir := filepath.Join(root, "data", "works")
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".nq") {
			paths = append(paths, path)
		}
		return nil
	})
	return paths, err
}

// gzFile is one gzipped output artifact with its size and digest tracked as
// bytes flow through: file <- gzip <- (writer given to the caller), with the
// sha256 taken over the compressed bytes.
type gzFile struct {
	f    *os.File
	gz   *gzip.Writer
	h    hash.Hash
	name string
}

func newGzFile(path string) (*gzFile, io.Writer, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, nil, err
	}
	g := &gzFile{f: f, h: sha256.New(), name: filepath.Base(path)}
	g.gz = gzip.NewWriter(io.MultiWriter(f, g.h))
	return g, g.gz, nil
}

// finish flushes and closes the artifact and returns its manifest entry.
func (g *gzFile) finish(records int) (File, error) {
	if err := g.gz.Close(); err != nil {
		return File{}, err
	}
	info, err := g.f.Stat()
	if err != nil {
		return File{}, err
	}
	if err := g.f.Close(); err != nil {
		return File{}, err
	}
	return File{
		Name:    g.name,
		Bytes:   info.Size(),
		SHA256:  hex.EncodeToString(g.h.Sum(nil)),
		Records: records,
	}, nil
}

// copyGzip gzips src to dst, rewriting extra/sources quads to the public
// allowlist on the way when one is set. Line-based: source names are plain
// strings without quotes or escapes by convention, so the literal is the span
// between the first and last '"'.
func copyGzip(src, dst string, public map[string]bool, log io.Writer) (File, error) {
	in, err := os.Open(src)
	if err != nil {
		return File{}, err
	}
	defer in.Close()
	g, w, err := newGzFile(dst)
	if err != nil {
		return File{}, err
	}
	sourcesPred := "<" + bibframe.ExtraPred + "sources>"
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	bw := bufio.NewWriter(w)
	stale := false
	for sc.Scan() {
		line := sc.Text()
		if !stale && traversalLabel.MatchString(line) {
			stale = true
		}
		if public != nil && strings.Contains(line, sourcesPred) {
			line = filterSourcesQuad(line, public)
			if line == "" {
				continue
			}
		}
		if _, err := bw.WriteString(line + "\n"); err != nil {
			return File{}, err
		}
	}
	if err := sc.Err(); err != nil {
		return File{}, err
	}
	// This file is one we did not write. Since tasks/298 every writer of
	// catalog.nq emits grain-derived labels, so `_:b1`-style traversal labels mean
	// the file predates that and its sha256 will move on the next rebuild for a
	// corpus that has not changed -- the churn tasks/291 was filed about. Say so;
	// the fix is one `lcat serialize` away.
	if stale && log != nil {
		fmt.Fprintf(log, "export: %s carries traversal-order blank-node labels, so its sha256 will move on the next rebuild even for a catalog that did not change. It was written by a pre-v0.120 lcat, or by an ingest step whose serialize never ran. Regenerate it from the grains: lcat serialize --dir %s\n", src, filepath.Dir(src))
	}
	if err := bw.Flush(); err != nil {
		return File{}, err
	}
	return g.finish(0)
}

// traversalLabel matches the `_:b1, _:b2, …` labels a shared rdf.Encoder assigns
// in traversal order. No writer emits them since tasks/298; a file that carries
// them is stale.
var traversalLabel = regexp.MustCompile(`(^|\s)_:b\d+(\s|\.)`)

// filterSourcesQuad rewrites one extra/sources quad's literal to the public
// allowlist, or returns "" to drop the quad when nothing public remains.
// Names compare trimmed and kept names re-join ", ", matching
// project.SanitizeSources.
func filterSourcesQuad(line string, public map[string]bool) string {
	open := strings.Index(line, `"`)
	close := strings.LastIndex(line, `"`)
	if open < 0 || close <= open {
		return line
	}
	var kept []string
	for s := range strings.SplitSeq(line[open+1:close], ",") {
		if s = strings.TrimSpace(s); s != "" && public[s] {
			kept = append(kept, s)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return line[:open+1] + strings.Join(kept, ", ") + line[close:]
}

// emitMARC streams every grain once, decoding to MARC and writing each record
// to both the ISO 2709 and MARCXML artifacts. Records the format cannot
// represent (ISO 2709 caps a record at 99,999 bytes and a field at 9,999) are
// skipped from both artifacts with a warning, so one pathological record does
// not abort the export; both artifacts always contain the same record set.
func emitMARC(grains []string, out string, log io.Writer, org string) (File, File, error) {
	none := File{}
	gm, wm, err := newGzFile(filepath.Join(out, "catalog.mrc.gz"))
	if err != nil {
		return none, none, err
	}
	gx, wx, err := newGzFile(filepath.Join(out, "catalog.xml.gz"))
	if err != nil {
		return none, none, err
	}
	mw := iso2709.NewWriter(wm)
	xw := marcxml.NewWriter(wx)
	records, skipped := 0, 0
	for _, path := range grains {
		grain, err := os.ReadFile(path)
		if err != nil {
			return none, none, err
		}
		recs, err := bibframe.DecodeGrainMARCSource(grain, org)
		if err != nil {
			return none, none, fmt.Errorf("%s: %w", path, err)
		}
		for _, rec := range recs {
			wrote, err := emitRecord(mw, xw, path, rec, log)
			if err != nil {
				return none, none, err
			}
			if wrote {
				records++
			} else {
				skipped++
			}
		}
	}
	if records == 0 {
		return none, none, fmt.Errorf("export: every record was skipped (%d); refusing to emit empty MARC artifacts", skipped)
	}
	if skipped > 0 {
		fmt.Fprintf(log, "export: skipped %d of %d records the ISO 2709 format cannot encode\n", skipped, records+skipped)
	}
	if err := xw.Close(); err != nil {
		return none, none, err
	}
	mrc, err := gm.finish(records)
	if err != nil {
		return none, none, err
	}
	xml, err := gx.finish(records)
	if err != nil {
		return none, none, err
	}
	return mrc, xml, nil
}

// emitRecord writes rec to both MARC artifacts, reporting false (and no error)
// when the record is one ISO 2709 cannot represent, so it is skipped from both
// artifacts and they stay in lockstep. Format-constraint failures are
// recognized by the "iso2709:" prefix libcodex puts on every encode error;
// the encoder builds the full record in memory before touching the stream, so
// a failed encode leaves the artifact clean. Any other error (I/O) aborts.
func emitRecord(mw *iso2709.Writer, xw *marcxml.Writer, path string, rec *codex.Record, log io.Writer) (bool, error) {
	if err := mw.Write(rec); err != nil {
		if strings.HasPrefix(err.Error(), "iso2709:") {
			fmt.Fprintf(log, "export: skipping %s: %v\n", path, err)
			return false, nil
		}
		return false, fmt.Errorf("%s: mrc: %w", path, err)
	}
	if err := xw.Write(rec); err != nil {
		return false, fmt.Errorf("%s: xml: %w", path, err)
	}
	return true, nil
}

// copyCovers flattens data/covers/<shard>/<file> under in to out/<file>,
// matching the covers/ URLs the OPAC's cover slot loads (tasks/215). A
// missing covers tree is a no-op -- most catalogs have no uploads.
func copyCovers(in, out string) error {
	if out == "" {
		return nil
	}
	root := filepath.Join(in, "data", "covers")
	if _, err := os.Stat(root); err != nil {
		return nil
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(out, filepath.Base(path)), data, 0o644)
	})
}
