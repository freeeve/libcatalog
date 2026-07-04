// Package profilesvc holds the live editing-profile set: the shipped defaults
// overlaid with a deployment's blob-persisted overrides, editable at runtime.
// Every override is run through profiles.Parse (the "framework test") before it
// is stored, so a structurally-bad profile is rejected at the API and can never
// reach the set the editor and op-builder read from. Overrides live in the blob
// store (durable across restarts, like vocabulary snapshots), keyed by profile
// id under Prefix.
package profilesvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/freeeve/libcatalog/storage/blob"

	"github.com/freeeve/libcatalog/backend/editor"
	"github.com/freeeve/libcatalog/backend/profiles"
)

var (
	// ErrNotFound reports that no profile (or no override) exists for an id.
	ErrNotFound = errors.New("profilesvc: not found")
	// ErrInvalid wraps a profiles.Parse failure on a save.
	ErrInvalid = errors.New("profilesvc: invalid profile")
	// ErrIDMismatch reports that a saved profile's id differs from its path id.
	ErrIDMismatch = errors.New("profilesvc: profile id does not match path")
	// ErrConflict reports a lost optimistic-lock race on save.
	ErrConflict = errors.New("profilesvc: version conflict")
	// ErrReadOnly reports a mutation attempted without a blob store.
	ErrReadOnly = errors.New("profilesvc: no blob store; profiles are read-only")
)

// idPattern bounds profile ids to a safe, single-segment blob path component.
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// snapshot is the immutable active set plus the id->etag map of overrides. A
// Reload builds a fresh one and swaps it in atomically, so every read sees a
// coherent view without locking (the same copy-on-write shape as vocab.Index).
type snapshot struct {
	set       profiles.Set
	overrides map[string]string
}

// ListEntry pairs a profile with whether an override currently shadows it, read
// from one snapshot so the pair is always coherent.
type ListEntry struct {
	Profile    *profiles.Profile
	Overridden bool
}

// Service owns the active profile set. The zero value is unusable; construct
// with New. A nil Blob yields a defaults-only, read-only service.
type Service struct {
	Blob   blob.Store
	Prefix string
	Logger *slog.Logger

	snap atomic.Pointer[snapshot]
}

// New builds the service over blob (nil for defaults-only) rooted at prefix.
func New(bs blob.Store, prefix string, logger *slog.Logger) *Service {
	if prefix == "" {
		prefix = "data/profiles/"
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{Blob: bs, Prefix: prefix, Logger: logger}
	s.snap.Store(&snapshot{set: profiles.Set{}, overrides: map[string]string{}})
	return s
}

// Load builds the initial set: the embedded defaults overlaid with the blob
// overrides. It is Reload, exposed under a boot-friendly name.
func (s *Service) Load(ctx context.Context) error { return s.Reload(ctx) }

// Reload rebuilds the active set from the embedded defaults and the current
// blob overrides. A malformed override is logged and skipped so one bad blob
// cannot brick the whole set (or boot); a broken embedded default is a
// programmer error and fails hard.
func (s *Service) Reload(ctx context.Context) error {
	defs, err := profiles.LoadDefaults()
	if err != nil {
		return fmt.Errorf("profilesvc: load defaults: %w", err)
	}
	active := profiles.Set{}
	maps.Copy(active, defs)
	overrides := map[string]string{}
	if s.Blob != nil {
		for e, err := range s.Blob.List(ctx, s.Prefix) {
			if err != nil {
				return fmt.Errorf("profilesvc: list overrides: %w", err)
			}
			if !strings.HasSuffix(e.Path, ".json") {
				continue
			}
			data, _, err := s.Blob.Get(ctx, e.Path)
			if err != nil {
				return fmt.Errorf("profilesvc: read %s: %w", e.Path, err)
			}
			p, err := profiles.Parse(data)
			if err != nil {
				s.Logger.Warn("skip invalid profile override", "path", e.Path, "err", err)
				continue
			}
			active[p.ID] = p
			overrides[p.ID] = e.ETag
		}
	}
	s.snap.Store(&snapshot{set: active, overrides: overrides})
	return nil
}

// Set returns the active profiles keyed by id. The map is the live snapshot and
// must be treated as read-only.
func (s *Service) Set() profiles.Set {
	return s.snap.Load().set
}

// List returns every profile with its override state, from one coherent
// snapshot (so the overridden flag always matches the returned profile).
func (s *Service) List() []ListEntry {
	snap := s.snap.Load()
	out := make([]ListEntry, 0, len(snap.set))
	for id, p := range snap.set {
		_, overridden := snap.overrides[id]
		out = append(out, ListEntry{Profile: p, Overridden: overridden})
	}
	return out
}

// Get returns the active profile for id, its override etag ("" when the profile
// is the shipped default), and whether it is overridden.
func (s *Service) Get(id string) (p *profiles.Profile, etag string, overridden bool, err error) {
	snap := s.snap.Load()
	p = snap.set[id]
	etag, overridden = snap.overrides[id]
	if p == nil {
		return nil, "", false, ErrNotFound
	}
	return p, etag, overridden, nil
}

// Overridden reports whether id currently has a blob override.
func (s *Service) Overridden(id string) bool {
	_, ok := s.snap.Load().overrides[id]
	return ok
}

// Mapper builds the read/op mapper from the live set, so record rendering and
// batch ops follow runtime edits to the work and instance profiles.
func (s *Service) Mapper() *editor.Mapper {
	set := s.snap.Load().set
	return &editor.Mapper{WorkProfile: set["work-monograph"], InstanceProfile: set["instance-ebook"]}
}

// Put validates data, persists it as id's override under optimistic locking,
// then reloads the set. An empty ifMatch means create-only (the first override
// of a default, or a brand-new profile); a non-empty ifMatch requires the
// stored override to still carry that etag. Returns the new etag.
func (s *Service) Put(ctx context.Context, id string, data []byte, ifMatch string) (string, error) {
	if s.Blob == nil {
		return "", ErrReadOnly
	}
	if !idPattern.MatchString(id) {
		return "", fmt.Errorf("%w: id must be lowercase alphanumeric/hyphen", ErrInvalid)
	}
	p, err := profiles.Parse(data)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if p.ID != id {
		return "", ErrIDMismatch
	}
	opts := blob.PutOptions{ContentType: "application/json"}
	if ifMatch == "" {
		opts.IfNoneMatch = true
	} else {
		opts.IfMatch = ifMatch
	}
	etag, err := s.Blob.Put(ctx, s.path(id), data, opts)
	if errors.Is(err, blob.ErrPreconditionFailed) {
		return "", ErrConflict
	}
	if err != nil {
		return "", err
	}
	if err := s.Reload(ctx); err != nil {
		return "", err
	}
	return etag, nil
}

// DeleteOverride removes id's blob override and reloads, reverting to the
// shipped default (or dropping the profile if it had none). ErrNotFound when no
// override exists.
func (s *Service) DeleteOverride(ctx context.Context, id string) error {
	if s.Blob == nil {
		return ErrReadOnly
	}
	err := s.Blob.Delete(ctx, s.path(id))
	if errors.Is(err, blob.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return s.Reload(ctx)
}

func (s *Service) path(id string) string { return s.Prefix + id + ".json" }
