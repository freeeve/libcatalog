// Package export derives the downloadable catalog artifacts from an ingest
// output root: catalog.nq.gz is the corpus itself -- the catalog.nq
// found there, which every writer now emits as the merge of the grains, with
// blank labels namespaced by work id -- and
// catalog.mrc.gz / catalog.xml.gz are per-grain MARC round-trips via
// bibframe.DecodeGrainMARC, which honors editorial override shadows and
// verbatim sidecars (fidelity bounded by docs/marc-fidelity.md). A Manifest
// records sizes, sha256 digests, and record counts for a downloads page.
// This is the static-site counterpart of the backend's on-demand export
// service (backend/export); both read the same grains.
package export

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
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
	// PublicExtras, when non-nil, is the allowlist of lcat:extra/ *keys*
	// permitted in the nq download, matching project.SanitizeExtras on the
	// catalog.json side. Other extra quads are dropped whole.
	// "sources" is exempt: PublicSources governs it, by value rather than by
	// key. Nil keeps every extra.
	PublicExtras map[string]bool
	// Log receives skip and strip warnings; nil means os.Stderr.
	Log io.Writer
	// OrgCode is the deployment's MARC organization code; when set, the
	// MARC download derives each record's 040 from graph facts at decode
	// time.
	OrgCode string
	// CoversOut, when set, copies uploaded cover images (data/covers/ under
	// In) to this directory as flat files, the site-relative covers/ URLs
	// the editorial lcat:extra/cover statements point at.
	// Empty skips the copy.
	CoversOut string
}

// Manifest is the downloads-page data file: what was generated when, from how
// many works, with per-artifact integrity and record counts.
type Manifest struct {
	Generated string `json:"generated"`
	// Works counts the Works the download describes -- the visible ones, so this
	// number is comparable with catalog.json's. It used to count grains, which
	// cannot disagree with anything: a build that published 3274 records for a
	// 31-work catalog reported 3274 and looked healthy.
	Works int `json:"works"`
	// Hidden counts the grains held back as suppressed or tombstoned. Recorded so
	// a takedown is auditable from the build output rather than inferable from it.
	Hidden int    `json:"hidden"`
	Files  []File `json:"files"`
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

	// `lcat project` drops a suppressed or tombstoned Work before it reaches
	// catalog.json, and every artifact derived from it. The download path used to
	// publish straight from the store, so a takedown removed a record from the
	// OPAC and left it in the RDF, the MARC and the covers. Apply the
	// same stance here, once, and let every artifact below read the result.
	visible, hiddenIRIs, err := partitionByVisibility(grains)
	if err != nil {
		return nil, err
	}
	hidden := len(hiddenIRIs)
	if len(visible) == 0 {
		return nil, fmt.Errorf("export: all %d works are suppressed or tombstoned; refusing to publish an empty catalog", hidden)
	}
	if hidden > 0 && opts.Log != nil {
		fmt.Fprintf(opts.Log, "export: held back %d of %d works as hidden (suppressed or tombstoned)\n", hidden, len(grains))
	}

	filter := nqFilter{sources: opts.PublicSources, extras: opts.PublicExtras}
	var files []File
	nq, err := writeNQ(visible, hiddenIRIs, filepath.Join(opts.Out, "catalog.nq.gz"), filter)
	if err != nil {
		return nil, err
	}
	files = append(files, nq)

	// A cover the public catalog cannot name is a cover no public page renders,
	// so publishing the image would leak exactly what the allowlist withheld.
	if filter.allowsExtra(bibframe.CoverExtraKey) {
		if err := copyCovers(opts.In, opts.CoversOut, visible, opts.Log); err != nil {
			return nil, err
		}
	} else if opts.CoversOut != "" && opts.Log != nil {
		fmt.Fprintf(opts.Log, "export: covers not published -- %q is not on the public-extras allowlist\n", bibframe.CoverExtraKey)
	}
	paths := make([]string, 0, len(visible))
	for _, g := range visible {
		paths = append(paths, g.path)
	}
	mrc, xml, err := emitMARC(paths, opts.Out, opts.Log, opts.OrgCode)
	if err != nil {
		return nil, err
	}
	files = append(files, mrc, xml)

	return &Manifest{
		Generated: time.Now().UTC().Format(time.RFC3339),
		Works:     len(visible),
		Hidden:    hidden,
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

// visibleGrain is one Work the projector would publish, with the cover blob its
// grain claims ("" when it claims none).
type visibleGrain struct {
	path  string
	id    string
	cover string
}

// partitionByVisibility reads each grain once and splits it on the stance
// `lcat project` already honours (project.go's tombstoned/suppressed guards).
// The visible grains come back sorted by work id, which is the order every
// writer of catalog.nq uses (bibframe.SerializeGrains), so an all-visible corpus
// exports the same bytes it did when this was a line copy of catalog.nq.
//
// hiddenIRIs is the set of Work IRIs the download must not name at all, even from
// a visible Work's own grain: see writeNQ.
func partitionByVisibility(paths []string) (visible []visibleGrain, hiddenIRIs map[string]bool, err error) {
	visible = make([]visibleGrain, 0, len(paths))
	hiddenIRIs = map[string]bool{}
	for _, p := range paths {
		id := strings.TrimSuffix(filepath.Base(p), ".nq")
		grain, err := os.ReadFile(p)
		if err != nil {
			return nil, nil, err
		}
		vis, err := bibframe.Visibility(grain, id)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", p, err)
		}
		// Both stances hide the Work from projection. A tombstone's id is public
		// in redirects.json by design, but "this id is gone" is not "here is what
		// it was", so the record leaves the downloads too.
		if vis.Tombstoned || vis.Suppressed {
			hiddenIRIs[bibframe.WorkIRI(id)] = true
			continue
		}
		cover, err := bibframe.CoverOf(grain, id)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", p, err)
		}
		visible = append(visible, visibleGrain{path: p, id: id, cover: cover})
	}
	sort.Slice(visible, func(i, j int) bool { return visible[i].id < visible[j].id })
	return visible, hiddenIRIs, nil
}

// workIRIRef matches a minted Work IRI wherever it appears in a quad. Minted
// Works are `#<id>Work` fragment IRIs (the identity.ScanGrain convention that
// project.go keys on); relation stubs are blank or external nodes and never match.
var workIRIRef = regexp.MustCompile(`<(#w[0-9a-z]+Work)>`)

// namesHiddenWork reports whether a quad mentions a Work the download omits.
func namesHiddenWork(line string, hidden map[string]bool) bool {
	if len(hidden) == 0 {
		return false
	}
	for _, m := range workIRIRef.FindAllStringSubmatch(line, -1) {
		if hidden[m[1]] {
			return true
		}
	}
	return false
}

// writeNQ builds the nq download from the visible grains rather than copying the
// catalog.nq on disk.
//
// Filtering the copy line-by-line was the obvious alternative and it does not
// work: a grain's Instance, its titles, its notes and its provision activity are
// subjects of their own (`<#...Instance>`), and none of them carries the work id.
// Dropping only the lines that name the id leaves 24 of a 33-quad record on the
// public site. The merge of the visible grains is the whole record or none of it.
//
// Rebuilding is also what catalog.nq *is*: every writer emits
// exactly this merge, so an all-visible corpus produces byte-identical output,
// pinned by TestNothingIsHeldBackWhenNothingIsHidden. It costs a second read of
// each grain and removes the export's dependence on a file it did not write:
// there is no longer a stale catalog.nq for the download to inherit.
//
// Dropping the hidden grains is not sufficient. A *visible* Work's grain may name
// a hidden one -- `bf:hasPart <#wHiddenWork>` -- and that quad would survive,
// publishing the hidden id and a statement about it. The projector strips exactly
// these (resolveRelations keeps only links whose target is still in the
// projection), so the download does too.
func writeNQ(visible []visibleGrain, hidden map[string]bool, dst string, filter nqFilter) (File, error) {
	g, w, err := newGzFile(dst)
	if err != nil {
		return File{}, err
	}
	bw := bufio.NewWriter(w)
	var merged bytes.Buffer
	for _, vg := range visible {
		grain, err := os.ReadFile(vg.path)
		if err != nil {
			return File{}, err
		}
		merged.Reset()
		if err := bibframe.WriteMergedGrain(&merged, vg.id, grain); err != nil {
			return File{}, fmt.Errorf("%s: %w", vg.path, err)
		}
		if filter.inert() && len(hidden) == 0 {
			if _, err := bw.Write(merged.Bytes()); err != nil {
				return File{}, err
			}
			continue
		}
		sc := bufio.NewScanner(bytes.NewReader(merged.Bytes()))
		sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			if namesHiddenWork(line, hidden) {
				continue
			}
			line, keep := filter.apply(line)
			if !keep {
				continue
			}
			if _, err := bw.WriteString(line + "\n"); err != nil {
				return File{}, err
			}
		}
		if err := sc.Err(); err != nil {
			return File{}, err
		}
	}
	if err := bw.Flush(); err != nil {
		return File{}, err
	}
	return g.finish(len(visible))
}

// nqFilter is the pair of allowlists the nq download applies to lcat:extra/
// quads: `sources` is filtered within its literal, every other extra by its key
// . A struct rather than two adjacent map[string]bool
// parameters, which nothing would have caught being swapped.
type nqFilter struct {
	sources map[string]bool
	extras  map[string]bool
}

// inert reports whether the filter would pass every quad through, letting
// writeNQ copy a grain's bytes without scanning it line by line.
func (f nqFilter) inert() bool { return f.sources == nil && f.extras == nil }

// allowsExtra reports whether key survives to the public face. An unset
// allowlist keeps everything, and `sources` is never dropped by key.
func (f nqFilter) allowsExtra(key string) bool {
	return f.extras == nil || key == extraSourcesKey || f.extras[key]
}

// apply filters one quad, returning the (possibly rewritten) line and whether
// to keep it. Quads that are not lcat:extra/ statements pass through untouched.
//
// The key allowlist decides first, for every extra including `sources` -- that is
// the single place the exemption lives, so a change to allowsExtra cannot leave
// one of the two callers behind. A surviving `sources` quad is then narrowed
// within its literal by the value allowlist.
func (f nqFilter) apply(line string) (string, bool) {
	key := extraKey(line)
	if key == "" {
		return line, true
	}
	if !f.allowsExtra(key) {
		return "", false
	}
	if key == extraSourcesKey && f.sources != nil {
		filtered := filterSourcesQuad(line, f.sources)
		return filtered, filtered != ""
	}
	return line, true
}

// extraSourcesKey is the one extra the key allowlist never governs: it has its
// own, and it is filtered by value. Mirrors project.SanitizeExtras.
const extraSourcesKey = "sources"

// extraKey returns the lcat:extra/ key a quad's predicate names, or "" when the
// quad is not an extra statement. The predicate is read positionally (N-Quads
// field 2) rather than by substring search, so a literal object that happens to
// quote an extra IRI cannot be mistaken for one.
func extraKey(line string) string {
	_, rest, ok := strings.Cut(line, " ")
	if !ok {
		return ""
	}
	pred, _, ok := strings.Cut(rest, " ")
	if !ok {
		return ""
	}
	key, ok := strings.CutPrefix(pred, "<"+bibframe.ExtraPred)
	if !ok {
		return ""
	}
	key, ok = strings.CutSuffix(key, ">")
	if !ok {
		return ""
	}
	return key
}

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
// matching the covers/ URLs the OPAC's cover slot loads. A
// missing covers tree is a no-op -- most catalogs have no uploads.
// copyCovers publishes exactly the covers the visible Works claim.
//
// It used to walk data/covers and copy every blob it found, which published the
// cover of every suppressed and tombstoned Work at covers/<workID>.<ext> -- a
// guessable URL, and for a tombstone a *derivable* one, since redirects.json
// names the id. `lcat covers --reap` cannot collect these: a hidden
// Work still has a grain and still claims its cover, so it is an orphan by none
// of the reaper's three reasons -- and reaping a blob after it has reached a CDN
// does not unpublish it.
//
// Driving from the claims also drops the stale-format residue for
// free: a Work names one cover, and any other blob bearing its id is not it.
func copyCovers(in, out string, visible []visibleGrain, log io.Writer) error {
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
	copied, missing := 0, 0
	for _, vg := range visible {
		if vg.cover == "" {
			continue
		}
		// The claim is a site-relative URL ("covers/<id>.<ext>"). The blob is
		// SHARDED: bibframe.CoverBlobPath writes data/covers/<xx>/<id>.<ext>, and
		// the site serves it flattened. Constructing the read path here without
		// the shard missed every cover in the store, and `os.IsNotExist -> continue`
		// turned that into silence: no error, no log line, zero covers published
		//. Ask the same function the writer used.
		name := path.Base(vg.cover)
		ext := strings.TrimPrefix(filepath.Ext(name), ".")
		data, err := os.ReadFile(filepath.Join(in, filepath.FromSlash(bibframe.CoverBlobPath(vg.id, ext))))
		if os.IsNotExist(err) {
			// The Work claims a cover the store no longer holds. Real, and benign
			// -- but counted and reported, because "every cover is missing" and
			// "one cover is missing" must not look alike from the build log.
			missing++
			continue
		}
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(out, name), data, 0o644); err != nil {
			return err
		}
		copied++
	}
	if missing > 0 && log != nil {
		fmt.Fprintf(log, "export: %d of %d claimed covers are not in the store and were not published\n",
			missing, missing+copied)
	}
	return nil
}
