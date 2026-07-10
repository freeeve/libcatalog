// Package blob defines a read-write, path-addressed object store with
// conditional writes -- the seam the dynamic tier uses to hold BIBFRAME grains
// in S3-compatible storage. It is the fuller sibling of storage.Sink: Sink
// stays write-only for build pipelines; Store adds Get/List/Delete and
// ETag-based optimistic concurrency for read-modify-write editorial
// publishing. Implementations here are stdlib-only (local directory, memory);
// cloud stores (S3/R2/MinIO) implement the same interface in their own
// packages so their SDKs never reach the core.
package blob

import (
	"bytes"
	"context"
	"errors"
	"io"
	"iter"
	"strings"
	"time"

	"github.com/freeeve/libcat/storage"
)

// ErrNotFound reports that no object exists at the requested path.
var ErrNotFound = errors.New("blob: not found")

// ErrPreconditionFailed reports that a conditional Put lost its race: the
// object's current state does not satisfy the request's IfMatch/IfNoneMatch.
// Callers recover by re-reading the object and retrying.
var ErrPreconditionFailed = errors.New("blob: precondition failed")

// Entry describes one stored object in a List result.
type Entry struct {
	Path string
	ETag string
	Size int64
}

// PutOptions carries the preconditions and metadata for a Put. IfMatch and
// IfNoneMatch are mutually exclusive: IfMatch (non-empty) requires the object
// to exist with exactly that ETag (a missing object fails the precondition);
// IfNoneMatch requires that no object exists at the path (create-only).
// ContentType is advisory; stores without metadata ignore it.
//
// ContentEncoding is advisory in the same way, and describes the bytes actually
// stored: "gzip" means the object holds a gzip stream that a client is expected
// to decompress transparently, with ContentType naming what it decompresses to.
// It matters because a presigned URL hands the object straight to a browser
// (see export.Service.DownloadURL), so the metadata, not the serving code, is
// what tells that browser how to read it.
type PutOptions struct {
	IfMatch         string
	IfNoneMatch     bool
	ContentType     string
	ContentEncoding string
}

// Store is a path-addressed object store. Paths are relative, slash-separated,
// and must not contain empty, ".", or ".." segments. ETags are opaque strings
// that change whenever an object's content changes; equal content need not
// yield equal ETags across different Store implementations.
type Store interface {
	// Get returns the object's content and current ETag, or ErrNotFound.
	Get(ctx context.Context, path string) (data []byte, etag string, err error)
	// Put writes the object subject to opts' preconditions and returns the
	// new ETag. Violated preconditions return ErrPreconditionFailed.
	Put(ctx context.Context, path string, data []byte, opts PutOptions) (etag string, err error)
	// List yields entries whose path starts with prefix, in lexicographic
	// path order.
	List(ctx context.Context, prefix string) iter.Seq2[Entry, error]
	// Delete removes the object, or returns ErrNotFound.
	Delete(ctx context.Context, path string) error
}

// Signer is an optional Store capability: a time-limited, unauthenticated
// download URL for an object (S3 presigned GETs). Stores without a native
// equivalent simply do not implement it; callers must type-assert and fall
// back to serving the bytes themselves.
type Signer interface {
	SignedGetURL(ctx context.Context, path string, ttl time.Duration) (string, error)
}

// RangeReader is an optional Store capability: read [offset, offset+length)
// of an object without fetching the whole blob (tasks/167 -- range-served
// vocabulary index artifacts). Implementations return exactly length bytes
// unless the range runs past the object's end, where they return the
// available prefix (io.ReaderAt semantics come from the ReaderAt adapter,
// not from GetRange itself). Reads of a missing object return ErrNotFound.
type RangeReader interface {
	GetRange(ctx context.Context, path string, offset, length int64) ([]byte, error)
}

// ReaderAt adapts one object to io.ReaderAt: ranged reads through the
// store's RangeReader capability when present, else one whole-object Get
// held in memory. The returned size is the object's current size; callers
// that cache derived state should key it by the returned ETag.
func ReaderAt(ctx context.Context, s Store, path string) (io.ReaderAt, int64, string, error) {
	rr, ok := s.(RangeReader)
	if !ok {
		data, etag, err := s.Get(ctx, path)
		if err != nil {
			return nil, 0, "", err
		}
		return bytes.NewReader(data), int64(len(data)), etag, nil
	}
	var found *Entry
	for entry, err := range s.List(ctx, path) {
		if err != nil {
			return nil, 0, "", err
		}
		if entry.Path == path {
			e := entry
			found = &e
			break
		}
	}
	if found == nil {
		return nil, 0, "", ErrNotFound
	}
	return &rangeReaderAt{ctx: ctx, rr: rr, path: path, size: found.Size}, found.Size, found.ETag, nil
}

// rangeReaderAt is the io.ReaderAt over a RangeReader capability.
type rangeReaderAt struct {
	ctx  context.Context
	rr   RangeReader
	path string
	size int64
}

func (r *rangeReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= r.size {
		return 0, io.EOF
	}
	data, err := r.rr.GetRange(r.ctx, r.path, off, int64(len(p)))
	if err != nil {
		return 0, err
	}
	n := copy(p, data)
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// StreamPutter is an optional Store capability: write an object from a
// reader without holding the whole payload in memory (tasks/108/110 --
// full-corpus exports and vocabulary snapshot installs are output-sized).
// DirStore streams into its temp file; the S3 store spools to a local temp
// file for a seekable upload body. Use the PutStream helper rather than
// type-asserting directly.
type StreamPutter interface {
	PutStream(ctx context.Context, path string, r io.Reader, opts PutOptions) (etag string, err error)
}

// PutStream writes r to path through the store's streaming capability when
// present, and falls back to buffering the reader into a plain Put (memory
// stores hold the payload either way; wrappers without the capability keep
// their Put semantics, e.g. read-only rejection).
func PutStream(ctx context.Context, s Store, path string, r io.Reader, opts PutOptions) (string, error) {
	if sp, ok := s.(StreamPutter); ok {
		return sp.PutStream(ctx, path, r, opts)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return s.Put(ctx, path, data, opts)
}

// ValidatePath rejects paths that are empty, absolute, or contain empty, ".",
// or ".." segments, so no Store implementation can be walked outside its root.
func ValidatePath(path string) error {
	if path == "" {
		return errors.New("blob: empty path")
	}
	if strings.HasPrefix(path, "/") {
		return errors.New("blob: absolute path")
	}
	for seg := range strings.SplitSeq(path, "/") {
		switch seg {
		case "", ".", "..":
			return errors.New("blob: invalid path segment")
		}
	}
	return nil
}

func checkOptions(opts PutOptions) error {
	if opts.IfMatch != "" && opts.IfNoneMatch {
		return errors.New("blob: IfMatch and IfNoneMatch are mutually exclusive")
	}
	return nil
}

// SinkOf adapts a Store to the write-only storage.Sink so existing build-side
// call sites (grain and artifact writers) can target any Store unchanged.
// Writes are buffered and stored unconditionally on Close.
func SinkOf(s Store) storage.Sink {
	return sinkAdapter{s}
}

type sinkAdapter struct{ s Store }

func (a sinkAdapter) Create(path string) (io.WriteCloser, error) {
	if err := ValidatePath(path); err != nil {
		return nil, err
	}
	return &sinkWriter{s: a.s, path: path}, nil
}

type sinkWriter struct {
	s    Store
	path string
	buf  bytes.Buffer
}

func (w *sinkWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }

func (w *sinkWriter) Close() error {
	_, err := w.s.Put(context.Background(), w.path, w.buf.Bytes(), PutOptions{})
	return err
}
