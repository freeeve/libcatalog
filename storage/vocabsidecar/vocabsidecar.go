// Package vocabsidecar owns the on-disk layout and lifecycle of a vocabulary
// scheme's sidecar artifacts -- where they live under the authorities tree, the
// manifest that arms a scheme, and the layout-only operations (remove, orphan
// sweep) that need no knowledge of how the artifacts are built or read.
//
// It lives in the root module so both the builder/loader (backend/vocab, which
// depends on root) and the offline CLI (cmd/lcat) can share it without the CLI
// having to import the backend module. Keeping the edge one-way -- everything
// points into root -- is what lets release.sh tag root, hugo and backend in
// lockstep without the CLI lagging a backend version.
//
// Layout under <prefix>sidecar/:
//
//	<scheme>.rrsr.bin/.idx  full Term JSON per doc (RRSR record store)
//	<scheme>.uri.rril       term URI -> doc, retired terms included
//	<scheme>.id1/2/3.rril   canon identifier tiers (own/exactMatch/closeMatch)
//	<scheme>.search.rrt     RRTI over normalized labels
//	<scheme>.manifest.json  source snapshot path+ETag; presence arms the scheme
package vocabsidecar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/freeeve/libcat/storage/blob"
)

// Layout constants shared by the builder, the loader and the sweep.
const (
	// Version is the artifact-set version; a manifest at a different version
	// arms nothing (the loader falls back to maps and the sweep collects it).
	Version = 2
	// DirPart is the subdirectory, under the authorities prefix, that holds
	// every scheme's sidecar artifacts.
	DirPart = "sidecar/"
	// ManifestSuffix names the per-scheme manifest whose presence arms the scheme.
	ManifestSuffix = ".manifest.json"
)

// SidecarManifest arms a scheme for sidecar serving: it names the source
// snapshot (and its ETag at build time) the artifacts were built from. A
// mismatched or missing source, or loose quads for the scheme elsewhere in
// the authorities tree, bypasses the sidecar for that snapshot build -- the
// map path remains the correctness backstop.
type SidecarManifest struct {
	Version    int    `json:"version"`
	Scheme     string `json:"scheme"`
	Source     string `json:"source"`
	SourceETag string `json:"sourceETag"`
	// SourceSchemes lists every authority scheme the source file carries --
	// the loader may skip parsing the file only when all of them are
	// sidecar-armed, so a shared source never silently drops a scheme.
	SourceSchemes []string `json:"sourceSchemes"`
	Terms         int      `json:"terms"`
	Live          int      `json:"live"`
}

// Path is the blob key for one of a scheme's sidecar artifacts.
func Path(prefix, scheme, suffix string) string {
	return prefix + DirPart + scheme + suffix
}

// Suffixes is every file a scheme's sidecar occupies, manifest first and the
// pre-v2 search blob last. TestRemoveSidecarLeavesNothingBuildSidecarWrote (in
// backend/vocab, where BuildSidecar lives) holds this in step with the artifacts
// BuildSidecar puts.
var Suffixes = []string{
	ManifestSuffix,
	".rrsr.bin", ".rrsr.idx",
	".uri.rril", ".id1.rril", ".id2.rril", ".id3.rril",
	".search.rrt",
	".search.bin", // pre-v2 LCVS search blob; a rebuild orphans it
}

// RemoveSidecar deletes a scheme's sidecar artifacts, undoing BuildSidecar. It is
// the caller's job to remove the snapshot the manifest names; removing that alone
// leaves the artifacts resident forever.
//
// The manifest goes first, mirroring the order BuildSidecar writes it in: the
// manifest is what arms a scheme, so a process that dies mid-delete leaves the
// scheme served from maps rather than armed on a half-deleted index. A missing
// artifact is not an error -- the set has changed across sidecar versions, and
// removing a scheme twice must be as harmless as removing it once.
//
// Artifacts are keyed by scheme, not by source name. Two sources declaring the same
// scheme already overwrite each other's sidecar in BuildSidecar; removal follows the
// same keying, so the survivor serves from maps until its next install.
func RemoveSidecar(ctx context.Context, st blob.Store, prefix, scheme string) error {
	for _, suffix := range Suffixes {
		p := Path(prefix, scheme, suffix)
		if err := st.Delete(ctx, p); err != nil && !errors.Is(err, blob.ErrNotFound) {
			return fmt.Errorf("vocabsidecar: remove sidecar %s: %w", p, err)
		}
	}
	return nil
}

// Orphan-sidecar reasons, kept distinct because they tell an operator different
// things: a live snapshot went away under a complete sidecar, versus a manifest
// that never parsed and so armed nothing to begin with. They are the values that
// land in OrphanSidecar.Reason, surfaced to operators by `lcat vocab-gc`.
const (
	ReasonSourceMissing      = "the snapshot the manifest names is gone"
	ReasonManifestUnreadable = "the manifest does not parse"
)

// OrphanSidecar is one scheme's sidecar artifact set that no live snapshot backs,
// so it serves nothing and RemoveSidecar can collect it.
type OrphanSidecar struct {
	Scheme string `json:"scheme"`
	// Source is the snapshot the manifest named, now missing (empty when the
	// manifest itself did not parse).
	Source string `json:"source,omitempty"`
	Reason string `json:"reason"`
}

// OrphanSidecars lists the sidecar artifact sets under prefix that no live snapshot
// backs. RemoveSnapshot deletes a scheme's artifacts as of but a removal
// before that shipped left them resident, and nothing collects them at boot: the
// loader detects the same staleness and serves the scheme from maps, but leaves the
// files where they are. This is the read half of the sweep; the caller deletes.
//
// A scheme is an orphan when the snapshot its manifest names is definitively absent
// -- a not-found on the source blob, never a transient read error, so a blob store
// hiccup cannot condemn a live index. A manifest that no longer parses is an orphan
// too: it arms nothing, and its scheme is recovered from the file name (the artifacts
// are named the same way) so it can still be collected.
func OrphanSidecars(ctx context.Context, st blob.Store, prefix string) ([]OrphanSidecar, error) {
	var out []OrphanSidecar
	for entry, err := range st.List(ctx, prefix+DirPart) {
		if err != nil {
			return nil, fmt.Errorf("vocabsidecar: list sidecars: %w", err)
		}
		if !strings.HasSuffix(entry.Path, ManifestSuffix) {
			continue
		}
		scheme := strings.TrimSuffix(path.Base(entry.Path), ManifestSuffix)
		data, _, err := st.Get(ctx, entry.Path)
		if err != nil {
			return nil, fmt.Errorf("vocabsidecar: read %s: %w", entry.Path, err)
		}
		m := &SidecarManifest{}
		if err := json.Unmarshal(data, m); err != nil || m.Version != Version || m.Scheme == "" {
			out = append(out, OrphanSidecar{Scheme: scheme, Reason: ReasonManifestUnreadable})
			continue
		}
		if _, _, err := st.Get(ctx, m.Source); errors.Is(err, blob.ErrNotFound) {
			out = append(out, OrphanSidecar{Scheme: m.Scheme, Source: m.Source, Reason: ReasonSourceMissing})
		} else if err != nil {
			return nil, fmt.Errorf("vocabsidecar: stat snapshot %s: %w", m.Source, err)
		}
	}
	return out, nil
}
