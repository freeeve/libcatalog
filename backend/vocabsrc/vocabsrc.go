// Package vocabsrc manages public authority sources (tasks/067): a registry
// of live-suggest and downloadable vocabulary sources seeded with built-ins
// (id.loc.gov datasets, Wikidata, VIAF), snapshot download jobs that convert
// public SKOS RDF dumps into authority-tree N-Quads the vocab index loads
// atomically, and the live typeahead proxy the picker and enrichment
// reconcile through. Custom sources (GND, Getty, MeSH, Homosaurus, ...) are
// drop-in registry entries, not code -- see docs/authority-sources.md.
package vocabsrc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/vocab"
)

// ErrValidation reports a source description the registry refuses.
var ErrValidation = errors.New("vocabsrc: invalid source")

// ErrNotFound reports a missing source, job, or installed snapshot.
var ErrNotFound = errors.New("vocabsrc: not found")

// ErrConflict reports an operation refused by the state it would leave behind --
// deleting a source whose snapshot is still installed (tasks/255). The caller can
// make it succeed by doing something first, which is what separates it from
// ErrValidation.
var ErrConflict = errors.New("vocabsrc: conflict")

// Source is one public authority source. A source may offer live typeahead
// (SuggestURL), a downloadable SKOS dump (SnapshotURL), or both.
type Source struct {
	Name   string `json:"name"`
	Scheme string `json:"scheme"` // vocab scheme key its terms live under
	// License and Homepage surface in the download list so a deployment
	// sees what it is installing.
	License  string `json:"license,omitempty"`
	Homepage string `json:"homepage,omitempty"`
	// Live typeahead capability. SuggestFlavor selects the response dialect
	// (suggest2 | wikidata | viaf); SuggestDataset is the suggest2 dataset
	// path (e.g. "authorities/subjects").
	SuggestFlavor  string `json:"suggestFlavor,omitempty"`
	SuggestURL     string `json:"suggestUrl,omitempty"`
	SuggestDataset string `json:"suggestDataset,omitempty"`
	// SnapshotURL points at a downloadable SKOS RDF dump (N-Triples or
	// N-Quads, optionally gzipped) convertible into the vocab index.
	SnapshotURL string `json:"snapshotUrl,omitempty"`
	// Builtin marks a shipped source (not deletable; a stored source of the
	// same name overrides it).
	Builtin bool `json:"builtin,omitempty"`
}

// CanSuggest reports whether the source offers live typeahead.
func (s Source) CanSuggest() bool { return s.SuggestURL != "" && s.SuggestFlavor != "" }

// CanSnapshot reports whether the source offers a downloadable dump.
func (s Source) CanSnapshot() bool { return s.SnapshotURL != "" }

// Builtins returns the shipped sources: the id.loc.gov datasets (subjects,
// genre/form, children's subjects downloadable; the name authority file is
// live-only -- its dump is ~11M concepts), OCLC FAST (live-only, tasks/132),
// Wikidata, and VIAF.
func Builtins() []Source {
	return []Source{
		{
			Name: "lcsh", Scheme: "lcsh", Builtin: true,
			License:       "Free of known restrictions (US federal)",
			Homepage:      "https://id.loc.gov/authorities/subjects.html",
			SuggestFlavor: FlavorSuggest2, SuggestURL: "https://id.loc.gov",
			SuggestDataset: "authorities/subjects",
			SnapshotURL:    "https://id.loc.gov/download/authorities/subjects.skosrdf.nt.gz",
		},
		{
			Name: "lcgft", Scheme: "lcgft", Builtin: true,
			License:       "Free of known restrictions (US federal)",
			Homepage:      "https://id.loc.gov/authorities/genreForms.html",
			SuggestFlavor: FlavorSuggest2, SuggestURL: "https://id.loc.gov",
			SuggestDataset: "authorities/genreForms",
			SnapshotURL:    "https://id.loc.gov/download/authorities/genreForms.skosrdf.nt.gz",
		},
		{
			Name: "lcshac", Scheme: "lcshac", Builtin: true,
			License:       "Free of known restrictions (US federal)",
			Homepage:      "https://id.loc.gov/authorities/childrensSubjects.html",
			SuggestFlavor: FlavorSuggest2, SuggestURL: "https://id.loc.gov",
			SuggestDataset: "authorities/childrensSubjects",
			SnapshotURL:    "https://id.loc.gov/download/authorities/childrensSubjects.skosrdf.nt.gz",
		},
		{
			Name: "lcnaf", Scheme: "lcnaf", Builtin: true,
			License:       "Free of known restrictions (US federal)",
			Homepage:      "https://id.loc.gov/authorities/names.html",
			SuggestFlavor: FlavorSuggest2, SuggestURL: "https://id.loc.gov",
			SuggestDataset: "authorities/names",
		},
		{
			// Suggest-only (tasks/132): the full FAST dump is ~2M concepts --
			// not resident-index-shaped for small deployments; a corpus subset
			// snapshot (lcat vocab-subset) supplies display labels instead.
			Name: "fast", Scheme: "fast", Builtin: true,
			License:       "ODC-BY (OCLC FAST)",
			Homepage:      "https://fast.oclc.org/",
			SuggestFlavor: FlavorSearchFAST, SuggestURL: "https://fast.oclc.org",
		},
		{
			Name: "wikidata", Scheme: "wikidata", Builtin: true,
			License:       "CC0",
			Homepage:      "https://www.wikidata.org",
			SuggestFlavor: FlavorWikidata, SuggestURL: "https://www.wikidata.org",
		},
		{
			Name: "viaf", Scheme: "viaf", Builtin: true,
			License:       "ODC-BY",
			Homepage:      "https://viaf.org",
			SuggestFlavor: FlavorVIAF, SuggestURL: "https://viaf.org",
		},
	}
}

// Service is the authority-source surface: registry CRUD, live suggest, and
// snapshot download/install/remove over the shared vocab index.
type Service struct {
	DB   store.Store
	Blob blob.Store
	// Index is the shared term index snapshots swap into.
	Index *vocab.Index
	// AuthoritiesPrefix roots the authority tree the index loads; installed
	// snapshots land under it at vocab/<name>.nq. Empty = "data/authorities/".
	AuthoritiesPrefix string
	// BaseSchemes is the deployment's configured scheme filter (nil = all
	// authority graphs load, so installs never need scheme bookkeeping).
	BaseSchemes []string
	// Suggest overrides the live-typeahead client (tests). nil = defaults.
	Suggest *SuggestClient
	// HTTPClient fetches snapshot dumps. nil = a 15-minute-timeout client.
	HTTPClient *http.Client
	// MaxSnapshotMB caps a snapshot dump's decompressed size (0 = the 4GB
	// default) -- the tasks/110 defensive ceiling against a hostile or
	// misconfigured endpoint.
	MaxSnapshotMB int
	Logger        *slog.Logger
	// Now overrides the clock (tests).
	Now func() time.Time
}

func (s *Service) clock() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) prefix() string {
	if s.AuthoritiesPrefix == "" {
		return "data/authorities/"
	}
	return s.AuthoritiesPrefix
}

func (s *Service) snapshotPath(name string) string { return s.prefix() + "vocab/" + name + ".nq" }
func (s *Service) metaPath(name string) string     { return s.prefix() + "vocab/" + name + ".json" }

func sourceKey(name string) store.Key { return store.Key{PK: "VOCABSRC", SK: "S#" + name} }

// Sources merges the built-ins with the stored registry (a stored source
// overrides a built-in of the same name), sorted by name.
func (s *Service) Sources(ctx context.Context) ([]Source, error) {
	byName := map[string]Source{}
	for _, src := range Builtins() {
		byName[src.Name] = src
	}
	for rec, err := range s.DB.Query(ctx, "VOCABSRC", "S#", store.QueryOpt{}) {
		if err != nil {
			return nil, err
		}
		var src Source
		if json.Unmarshal(rec.Data, &src) == nil {
			src.Builtin = byName[src.Name].Builtin
			byName[src.Name] = src
		}
	}
	out := make([]Source, 0, len(byName))
	for _, src := range byName {
		out = append(out, src)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// GetSource resolves one source by name.
func (s *Service) GetSource(ctx context.Context, name string) (Source, error) {
	sources, err := s.Sources(ctx)
	if err != nil {
		return Source{}, err
	}
	for _, src := range sources {
		if src.Name == name {
			return src, nil
		}
	}
	return Source{}, ErrNotFound
}

// PutSource creates or replaces a registry entry -- the drop-in-config path
// for sources beyond the built-ins (and for overriding a built-in's URLs).
func (s *Service) PutSource(ctx context.Context, src Source) error {
	if err := validateSource(src); err != nil {
		return err
	}
	src.Builtin = false
	data, err := json.Marshal(src)
	if err != nil {
		return err
	}
	_, err = s.DB.Put(ctx, store.Record{Key: sourceKey(src.Name), Data: data}, store.CondNone)
	return err
}

// DeleteSource removes a stored registry entry. A built-in cannot be deleted
// (deleting a stored override restores the shipped definition).
//
// It refuses with ErrConflict while a snapshot is installed, which is what the
// screen's tooltip has always promised (tasks/255): deleting the row out from under
// an install leaves an orphan whose Upload and Delete actions can only 404, and the
// admin was told the server would stop them. RemoveSnapshot first, then delete.
//
// Deleting a stored *override* of a built-in is exempt: the shipped definition takes
// its place, so the install keeps a source and is never orphaned.
//
// The refusal is not a statement about the vocabulary. An install still outlives its
// source row by other routes -- an offline lcat vocab-install (tasks/163), or a
// registry that reset because the deployment has no document store -- so Views still
// synthesizes the orphan, and RemoveSnapshot is still the way out of it.
func (s *Service) DeleteSource(ctx context.Context, name string) error {
	if !isBuiltin(name) {
		if _, _, err := s.Blob.Get(ctx, s.metaPath(name)); err == nil {
			return fmt.Errorf("%w: %q has an installed snapshot; remove it before deleting the source", ErrConflict, name)
		} else if !errors.Is(err, blob.ErrNotFound) {
			return err
		}
	}
	err := s.DB.Delete(ctx, store.Record{Key: sourceKey(name)}, store.CondNone)
	if errors.Is(err, store.ErrNotFound) {
		if isBuiltin(name) {
			return fmt.Errorf("%w: %q is builtin; override it instead", ErrValidation, name)
		}
		return ErrNotFound
	}
	return err
}

// isBuiltin reports whether name is one of the shipped sources.
func isBuiltin(name string) bool {
	for _, b := range Builtins() {
		if b.Name == name {
			return true
		}
	}
	return false
}

func validateSource(src Source) error {
	if src.Name == "" || strings.ContainsAny(src.Name, "/# ") {
		return fmt.Errorf("%w: a source needs a name without '/', '#', or spaces", ErrValidation)
	}
	if src.Scheme == "" {
		return fmt.Errorf("%w: a source needs a scheme", ErrValidation)
	}
	// A source with neither a suggest endpoint nor a snapshot URL is still
	// registrable: it installs by hand-uploaded dump (InstallUpload).
	if src.SuggestURL != "" {
		switch src.SuggestFlavor {
		case FlavorSuggest2, FlavorWikidata, FlavorVIAF:
		default:
			return fmt.Errorf("%w: unknown suggest flavor %q", ErrValidation, src.SuggestFlavor)
		}
	}
	for _, u := range []string{src.SuggestURL, src.SnapshotURL, src.Homepage} {
		if u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			return fmt.Errorf("%w: urls must be http(s)", ErrValidation)
		}
	}
	return nil
}

// InstallInfo is an installed snapshot's sidecar metadata, stored beside the
// .nq in the blob store so install state survives restarts.
type InstallInfo struct {
	Source      string    `json:"source"`
	Scheme      string    `json:"scheme"`
	Terms       int       `json:"terms"`
	InstalledAt time.Time `json:"installedAt"`
	SnapshotURL string    `json:"snapshotUrl"`
}

// Installed lists the installed snapshots by reading the sidecars under the
// vocab/ subtree.
func (s *Service) Installed(ctx context.Context) ([]InstallInfo, error) {
	out := []InstallInfo{}
	for entry, err := range s.Blob.List(ctx, s.prefix()+"vocab/") {
		if err != nil {
			return nil, err
		}
		if !strings.HasSuffix(entry.Path, ".json") {
			continue
		}
		data, _, err := s.Blob.Get(ctx, entry.Path)
		if err != nil {
			return nil, err
		}
		var info InstallInfo
		if json.Unmarshal(data, &info) == nil {
			out = append(out, info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out, nil
}

// Schemes computes the index's effective scheme filter: the configured base
// filter plus every installed snapshot's and cached live pick's scheme, or
// nil (= everything) when no base filter is configured.
func (s *Service) Schemes(ctx context.Context) ([]string, error) {
	if len(s.BaseSchemes) == 0 {
		return nil, nil
	}
	schemes := append([]string{}, s.BaseSchemes...)
	installed, err := s.Installed(ctx)
	if err != nil {
		return nil, err
	}
	for _, info := range installed {
		if !slices.Contains(schemes, info.Scheme) {
			schemes = append(schemes, info.Scheme)
		}
	}
	cached, err := s.cachedSchemes(ctx)
	if err != nil {
		return nil, err
	}
	for _, scheme := range cached {
		if !slices.Contains(schemes, scheme) {
			schemes = append(schemes, scheme)
		}
	}
	return schemes, nil
}

// Reload rebuilds the shared index from the authorities tree with the
// effective schemes -- an atomic snapshot swap under concurrent readers.
func (s *Service) Reload(ctx context.Context) error {
	if s.Index == nil {
		return nil
	}
	schemes, err := s.Schemes(ctx)
	if err != nil {
		return err
	}
	return s.Index.Reload(ctx, s.Blob, s.prefix(), schemes)
}

// RemoveSnapshot deletes an installed snapshot, its install meta, and the sidecar
// index artifacts built from it, then reloads the index so the scheme's terms drop
// out.
//
// The scheme comes from the install meta rather than the source registry, because
// removing a snapshot whose source row is already gone -- the orphan install Views
// synthesizes so it stays removable -- is exactly the case that leaves artifacts
// behind (tasks/252). The sidecar goes first: its manifest is what arms the scheme,
// so an interrupted removal degrades the scheme to the map loader instead of arming
// it on an index whose snapshot has been deleted.
// It returns the removed snapshot's InstallInfo so a caller can record what was
// uninstalled (its scheme and term count) in an audit note (tasks/259).
func (s *Service) RemoveSnapshot(ctx context.Context, name string) (InstallInfo, error) {
	meta, _, err := s.Blob.Get(ctx, s.metaPath(name))
	if errors.Is(err, blob.ErrNotFound) {
		return InstallInfo{}, ErrNotFound
	} else if err != nil {
		return InstallInfo{}, err
	}
	var info InstallInfo
	if err := json.Unmarshal(meta, &info); err != nil {
		return InstallInfo{}, fmt.Errorf("vocabsrc: install meta for %q is unreadable, so its sidecar cannot be located: %w", name, err)
	}
	if info.Scheme != "" {
		if err := vocab.RemoveSidecar(ctx, s.Blob, s.prefix(), info.Scheme); err != nil {
			return InstallInfo{}, err
		}
	}
	if err := s.Blob.Delete(ctx, s.snapshotPath(name)); err != nil && !errors.Is(err, blob.ErrNotFound) {
		return InstallInfo{}, err
	}
	if err := s.Blob.Delete(ctx, s.metaPath(name)); err != nil {
		return InstallInfo{}, err
	}
	return info, s.Reload(ctx)
}

// SourceView is the list surface: a source plus its install state and its
// most recent download job.
type SourceView struct {
	Source
	Installed *InstallInfo `json:"installed,omitempty"`
	Job       *Job         `json:"job,omitempty"`
	// Orphan marks a row synthesized from an install with no source record behind
	// it. Such a row can only be removed: everything else the screen offers needs a
	// source to act on, and answers 404 without one (tasks/255). An empty
	// SnapshotURL is not a proxy for this -- an upload-only source has none either.
	Orphan bool `json:"orphan,omitempty"`
}

// Views assembles the download-list screen's rows.
func (s *Service) Views(ctx context.Context) ([]SourceView, error) {
	sources, err := s.Sources(ctx)
	if err != nil {
		return nil, err
	}
	installed, err := s.Installed(ctx)
	if err != nil {
		return nil, err
	}
	byName := map[string]InstallInfo{}
	for _, info := range installed {
		byName[info.Source] = info
	}
	jobs, err := s.Jobs(ctx)
	if err != nil {
		return nil, err
	}
	latest := map[string]Job{}
	for _, job := range jobs {
		if prev, ok := latest[job.Source]; !ok || job.CreatedAt.After(prev.CreatedAt) {
			latest[job.Source] = job
		}
	}
	views := make([]SourceView, 0, len(sources))
	registered := map[string]bool{}
	for _, src := range sources {
		registered[src.Name] = true
		v := SourceView{Source: src}
		if info, ok := byName[src.Name]; ok {
			v.Installed = &info
		}
		if job, ok := latest[src.Name]; ok {
			v.Job = &job
		}
		views = append(views, v)
	}
	// Orphan installs -- a snapshot present without a registered source (an
	// offline vocab-install, tasks/163, or a registry that reset because the
	// deployment has no document store). Synthesized from the sidecar so the
	// vocabulary stays visible and removable.
	for name, info := range byName {
		if !registered[name] {
			views = append(views, SourceView{Source: Source{Name: name, Scheme: info.Scheme}, Installed: &info, Orphan: true})
		}
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	return views, nil
}
