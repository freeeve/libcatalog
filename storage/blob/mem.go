package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"iter"
	"sort"
	"sync"
)

// MemStore is an in-memory Store with exact conditional-write semantics --
// the reference implementation for tests and local development.
type MemStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

// NewMem returns an empty MemStore.
func NewMem() *MemStore {
	return &MemStore{objects: make(map[string][]byte)}
}

func contentETag(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Get returns the object's content and ETag, or ErrNotFound.
func (m *MemStore) Get(ctx context.Context, path string) ([]byte, string, error) {
	if err := ValidatePath(path); err != nil {
		return nil, "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[path]
	if !ok {
		return nil, "", ErrNotFound
	}
	out := make([]byte, len(data))
	copy(out, data)
	return out, contentETag(data), nil
}

// Put stores the object subject to opts' preconditions.
func (m *MemStore) Put(ctx context.Context, path string, data []byte, opts PutOptions) (string, error) {
	if err := ValidatePath(path); err != nil {
		return "", err
	}
	if err := checkOptions(opts); err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current, exists := m.objects[path]
	if opts.IfNoneMatch && exists {
		return "", ErrPreconditionFailed
	}
	if opts.IfMatch != "" && (!exists || contentETag(current) != opts.IfMatch) {
		return "", ErrPreconditionFailed
	}
	stored := make([]byte, len(data))
	copy(stored, data)
	m.objects[path] = stored
	return contentETag(stored), nil
}

// List yields entries under prefix in lexicographic path order.
func (m *MemStore) List(ctx context.Context, prefix string) iter.Seq2[Entry, error] {
	return func(yield func(Entry, error) bool) {
		m.mu.Lock()
		paths := make([]string, 0, len(m.objects))
		for p := range m.objects {
			if len(p) >= len(prefix) && p[:len(prefix)] == prefix {
				paths = append(paths, p)
			}
		}
		sizes := make(map[string]Entry, len(paths))
		for _, p := range paths {
			sizes[p] = Entry{Path: p, ETag: contentETag(m.objects[p]), Size: int64(len(m.objects[p]))}
		}
		m.mu.Unlock()
		sort.Strings(paths)
		for _, p := range paths {
			if !yield(sizes[p], nil) {
				return
			}
		}
	}
}

// Delete removes the object, or returns ErrNotFound.
func (m *MemStore) Delete(ctx context.Context, path string) error {
	if err := ValidatePath(path); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.objects[path]; !ok {
		return ErrNotFound
	}
	delete(m.objects, path)
	return nil
}
