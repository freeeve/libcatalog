package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"iter"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// DirStore is a Store backed by a local directory tree -- the dev/test and
// self-host default. ETags are the sha256 of the object's content. Conditional
// writes are enforced under a process-local mutex, which makes them exact
// within one process and best-effort against concurrent external writers;
// production multi-writer deployments use an object store with native
// conditional PUTs.
//
// Computed ETags are cached against each file's (mtime, size), so List stats
// rather than reads files whose cache entry is current -- the same
// best-effort stance as the CAS: an external writer that preserves both mtime
// and size can serve a stale tag until the next content read.
type DirStore struct {
	root string
	mu   sync.Mutex

	emu   sync.Mutex
	etags map[string]dirETag
}

// dirETag is one cached content ETag with the stat signature it was
// computed under.
type dirETag struct {
	mtime time.Time
	size  int64
	etag  string
}

// NewDir returns a DirStore rooted at dir.
func NewDir(dir string) *DirStore {
	return &DirStore{root: dir, etags: map[string]dirETag{}}
}

// cachedETag returns the cached ETag for path if the stat signature still
// matches.
func (d *DirStore) cachedETag(path string, info fs.FileInfo) (string, bool) {
	d.emu.Lock()
	defer d.emu.Unlock()
	cur, ok := d.etags[path]
	if !ok || !cur.mtime.Equal(info.ModTime()) || cur.size != info.Size() {
		return "", false
	}
	return cur.etag, true
}

// rememberETag caches a freshly computed ETag under the file's current stat
// signature. Callers pass the stat taken around the content read; a racing
// replacement changes mtime, which invalidates the entry on the next check.
func (d *DirStore) rememberETag(path string, info fs.FileInfo, etag string) {
	d.emu.Lock()
	defer d.emu.Unlock()
	d.etags[path] = dirETag{mtime: info.ModTime(), size: info.Size(), etag: etag}
}

func (d *DirStore) forgetETag(path string) {
	d.emu.Lock()
	defer d.emu.Unlock()
	delete(d.etags, path)
}

func (d *DirStore) full(path string) string {
	return filepath.Join(d.root, filepath.FromSlash(path))
}

// Get returns the object's content and ETag, or ErrNotFound.
func (d *DirStore) Get(ctx context.Context, path string) ([]byte, string, error) {
	if err := ValidatePath(path); err != nil {
		return nil, "", err
	}
	full := d.full(path)
	info, statErr := os.Stat(full)
	data, err := os.ReadFile(full)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	etag := contentETag(data)
	if statErr == nil {
		d.rememberETag(path, info, etag)
	}
	return data, etag, nil
}

// GetRange implements the RangeReader capability: [offset, offset+length)
// of the file, clamped to its end.
func (d *DirStore) GetRange(ctx context.Context, path string, offset, length int64) ([]byte, error) {
	if err := ValidatePath(path); err != nil {
		return nil, err
	}
	if offset < 0 || length < 0 {
		return nil, nil
	}
	f, err := os.Open(d.full(path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := make([]byte, length)
	n, err := f.ReadAt(out, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return out[:n], nil
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
	etag := contentETag(data)
	if info, err := os.Stat(full); err == nil {
		d.rememberETag(path, info, etag)
	}
	return etag, nil
}

// PutStream implements blob.StreamPutter: the payload streams straight into
// the temp file (hashed as it copies), so only the copy buffer is held in
// memory. The copy runs outside the store lock; preconditions are checked
// and the rename performed under it, same as Put.
func (d *DirStore) PutStream(ctx context.Context, path string, r io.Reader, opts PutOptions) (string, error) {
	if err := ValidatePath(path); err != nil {
		return "", err
	}
	if err := checkOptions(opts); err != nil {
		return "", err
	}
	full := d.full(path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), ".blob-*")
	if err != nil {
		return "", err
	}
	discard := func() { tmp.Close(); os.Remove(tmp.Name()) }
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), r); err != nil {
		discard()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	current, err := os.ReadFile(full)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		os.Remove(tmp.Name())
		return "", err
	}
	if (opts.IfNoneMatch && exists) || (opts.IfMatch != "" && (!exists || contentETag(current) != opts.IfMatch)) {
		os.Remove(tmp.Name())
		return "", ErrPreconditionFailed
	}
	if err := os.Rename(tmp.Name(), full); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	etag := hex.EncodeToString(h.Sum(nil))
	if info, err := os.Stat(full); err == nil {
		d.rememberETag(path, info, etag)
	}
	return etag, nil
}

// List yields entries under prefix in lexicographic path order. Prefixes are
// plain string prefixes over slash-separated paths (as in object stores), not
// directory boundaries. The walk is scoped to the deepest directory the
// prefix names, files whose cached ETag is current are stat'd rather than
// read, and a file deleted mid-walk is skipped instead of truncating the
// listing.
func (d *DirStore) List(ctx context.Context, prefix string) iter.Seq2[Entry, error] {
	return func(yield func(Entry, error) bool) {
		walkRoot := d.root
		if i := strings.LastIndex(prefix, "/"); i >= 0 {
			walkRoot = filepath.Join(d.root, filepath.FromSlash(prefix[:i]))
		}
		var entries []Entry
		err := filepath.WalkDir(walkRoot, func(p string, ent fs.DirEntry, err error) error {
			if err != nil {
				// A directory (or the walk root) removed mid-walk yields
				// nothing further beneath it; everything else surfaces.
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
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
			entry, err := d.statEntry(path, p, ent)
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			entries = append(entries, entry)
			return nil
		})
		if err != nil {
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

// statEntry builds one List entry, reusing the cached ETag when the file's
// stat signature is unchanged and reading the content only on a cache miss.
func (d *DirStore) statEntry(path, full string, ent fs.DirEntry) (Entry, error) {
	info, err := ent.Info()
	if err != nil {
		return Entry{}, err
	}
	if etag, ok := d.cachedETag(path, info); ok {
		return Entry{Path: path, ETag: etag, Size: info.Size()}, nil
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return Entry{}, err
	}
	etag := contentETag(data)
	d.rememberETag(path, info, etag)
	return Entry{Path: path, ETag: etag, Size: int64(len(data))}, nil
}

// Delete removes the object, or returns ErrNotFound.
func (d *DirStore) Delete(ctx context.Context, path string) error {
	if err := ValidatePath(path); err != nil {
		return err
	}
	d.forgetETag(path)
	err := os.Remove(d.full(path))
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return err
}
