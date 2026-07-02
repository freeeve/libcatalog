package blob

import (
	"context"
	"errors"
	"io/fs"
	"iter"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// DirStore is a Store backed by a local directory tree -- the dev/test and
// self-host default. ETags are the sha256 of the object's content. Conditional
// writes are enforced under a process-local mutex, which makes them exact
// within one process and best-effort against concurrent external writers;
// production multi-writer deployments use an object store with native
// conditional PUTs.
type DirStore struct {
	root string
	mu   sync.Mutex
}

// NewDir returns a DirStore rooted at dir.
func NewDir(dir string) *DirStore {
	return &DirStore{root: dir}
}

func (d *DirStore) full(path string) string {
	return filepath.Join(d.root, filepath.FromSlash(path))
}

// Get returns the object's content and ETag, or ErrNotFound.
func (d *DirStore) Get(ctx context.Context, path string) ([]byte, string, error) {
	if err := ValidatePath(path); err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(d.full(path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	return data, contentETag(data), nil
}

// Put writes the object subject to opts' preconditions, creating parent
// directories as needed. The write is a temp-file rename, so readers never
// observe a partial object.
func (d *DirStore) Put(ctx context.Context, path string, data []byte, opts PutOptions) (string, error) {
	if err := ValidatePath(path); err != nil {
		return "", err
	}
	if err := checkOptions(opts); err != nil {
		return "", err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	full := d.full(path)
	current, err := os.ReadFile(full)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if opts.IfNoneMatch && exists {
		return "", ErrPreconditionFailed
	}
	if opts.IfMatch != "" && (!exists || contentETag(current) != opts.IfMatch) {
		return "", ErrPreconditionFailed
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), ".blob-*")
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), full); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return contentETag(data), nil
}

// List yields entries under prefix in lexicographic path order. Prefixes are
// plain string prefixes over slash-separated paths (as in object stores), not
// directory boundaries.
func (d *DirStore) List(ctx context.Context, prefix string) iter.Seq2[Entry, error] {
	return func(yield func(Entry, error) bool) {
		var entries []Entry
		err := filepath.WalkDir(d.root, func(p string, ent fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if ent.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(d.root, p)
			if err != nil {
				return err
			}
			path := filepath.ToSlash(rel)
			if len(path) < len(prefix) || path[:len(prefix)] != prefix {
				return nil
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			entries = append(entries, Entry{Path: path, ETag: contentETag(data), Size: int64(len(data))})
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			yield(Entry{}, err)
			return
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
		for _, e := range entries {
			if !yield(e, nil) {
				return
			}
		}
	}
}

// Delete removes the object, or returns ErrNotFound.
func (d *DirStore) Delete(ctx context.Context, path string) error {
	if err := ValidatePath(path); err != nil {
		return err
	}
	err := os.Remove(d.full(path))
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return err
}
