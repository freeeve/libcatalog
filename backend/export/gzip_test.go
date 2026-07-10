// tasks/282: exports are stored gzipped. A full-corpus N-Quads dump compresses
// ~20x, and the blob store, the wire and the librarian's disk each pay for the
// difference. CSV hides the compression behind Content-Encoding because it is
// opened in Excel; the machine formats are real .gz artifacts.
package export

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/store"
)

// storedBytes reads the job's object exactly as it sits in the blob store.
func storedBytes(t *testing.T, bs blob.Store, job Job) []byte {
	t.Helper()
	data, _, err := bs.Get(t.Context(), job.OutputPath)
	if err != nil {
		t.Fatalf("read %s: %v", job.OutputPath, err)
	}
	return data
}

func runFormat(t *testing.T, format Format) (*Service, blob.Store, Job) {
	t.Helper()
	bs, workIDs := buildFixtureTree(t)
	svc := newService(t, bs)
	job, err := svc.Create(t.Context(), "lib@example.org", format, Selection{WorkIDs: workIDs})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != StatusDone {
		t.Fatalf("job %+v", job)
	}
	return svc, bs, job
}

// Every format, whatever its delivery, is gzip on disk.
func TestEveryExportIsStoredGzipped(t *testing.T) {
	for _, f := range []Format{FormatCSV, FormatNQuads, FormatJSONLD, FormatMARC} {
		t.Run(string(f), func(t *testing.T) {
			_, bs, job := runFormat(t, f)
			if raw := storedBytes(t, bs, job); !isGzip(raw) {
				t.Fatalf("%s stored uncompressed (first bytes %x)", f, raw[:min(4, len(raw))])
			}
		})
	}
}

// The machine formats are handed over as compressed artifacts, so a 2GB dump
// stays ~100MB once it lands.
func TestArchiveFormatsAreGzArtifacts(t *testing.T) {
	for _, tc := range []struct{ format, suffix string }{
		{"nquads", ".nq.gz"}, {"jsonld", ".jsonld.gz"}, {"marc", ".mrc.gz"},
	} {
		t.Run(tc.format, func(t *testing.T) {
			_, _, job := runFormat(t, Format(tc.format))
			if !strings.HasSuffix(job.OutputPath, tc.suffix) {
				t.Fatalf("outputPath = %q, want a %s artifact", job.OutputPath, tc.suffix)
			}
			del := DeliveryFor(job.ID, job.Format)
			if del.ContentType != "application/gzip" {
				t.Fatalf("contentType = %q, want application/gzip: the browser must not silently inflate it", del.ContentType)
			}
			if del.ContentEncoding != "" {
				t.Fatalf("contentEncoding = %q, want empty: a .gz artifact is not transparently encoded", del.ContentEncoding)
			}
		})
	}
}

// CSV keeps its name and declares the compression instead, so the browser saves
// an ordinary .csv.
func TestCSVIsTransparentlyCompressed(t *testing.T) {
	_, _, job := runFormat(t, FormatCSV)

	if !strings.HasSuffix(job.OutputPath, ".csv") || strings.HasSuffix(job.OutputPath, ".gz") {
		t.Fatalf("outputPath = %q, want a plain .csv name", job.OutputPath)
	}
	del := DeliveryFor(job.ID, FormatCSV)
	if del.ContentType != "text/csv" || del.ContentEncoding != "gzip" {
		t.Fatalf("delivery = %+v, want text/csv + gzip: a presigned URL carries only this metadata", del)
	}
}

// Open is the decompressed view, so every existing caller keeps working.
func TestOpenReturnsPlainBytes(t *testing.T) {
	svc, bs, job := runFormat(t, FormatCSV)

	out, err := svc.Open(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	if isGzip(out) {
		t.Fatal("Open returned a gzip stream; callers expect the decompressed CSV")
	}
	if !bytes.HasPrefix(out, []byte("id,title,")) {
		t.Fatalf("Open returned %.40q, want a CSV header", out)
	}
	if len(out) <= len(storedBytes(t, bs, job)) {
		t.Fatal("the stored object is not smaller than its contents; compression did nothing")
	}
}

// OpenStored hands the compressed bytes straight through, so the download route
// need not decompress and recompress for a client that accepts gzip.
func TestOpenStoredReportsCompression(t *testing.T) {
	svc, bs, job := runFormat(t, FormatNQuads)

	data, gzipped, err := svc.OpenStored(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	if !gzipped {
		t.Fatal("OpenStored did not report the object as gzipped")
	}
	if !bytes.Equal(data, storedBytes(t, bs, job)) {
		t.Fatal("OpenStored altered the stored bytes")
	}
}

// Exports written before tasks/282 hold plain bytes. The magic number decides,
// not the path, so they still download.
func TestOpenReadsLegacyUncompressedOutput(t *testing.T) {
	bs := blob.NewMem()
	svc, err := New(store.NewMem(), bs, "marc", []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	legacy := Job{ID: "old1", Format: FormatCSV, OutputPath: "exports/old1.csv"}
	want := []byte("workId,title\nw1,Old Export\n")
	if _, err := bs.Put(t.Context(), legacy.OutputPath, want, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}

	got, err := svc.Open(t.Context(), legacy)
	if err != nil {
		t.Fatalf("a pre-tasks/282 export became unreadable: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("legacy Open = %q, want %q", got, want)
	}
	if _, gzipped, _ := svc.OpenStored(t.Context(), legacy); gzipped {
		t.Fatal("plain bytes reported as gzipped")
	}
}

// readFailStore fails every grain read, so the emitter aborts partway.
type readFailStore struct {
	blob.Store
	fail error
}

func (s readFailStore) Get(ctx context.Context, path string) ([]byte, string, error) {
	if strings.Contains(path, "data/works/") {
		return nil, "", s.fail
	}
	return s.Store.Get(ctx, path)
}

// An aborted emit must leave no object. Compressed output makes this sharper
// than it was: gzip buffers, so a partial emit that got as far as writing a
// trailer would read back as a complete, short export -- a cataloger cannot tell
// a truncated dump from a small catalog.
func TestAbortedEmitStoresNoObject(t *testing.T) {
	bs, workIDs := buildFixtureTree(t)
	broken := readFailStore{Store: bs, fail: errors.New("disk went away")}
	svc := newService(t, broken)

	job, err := svc.Create(t.Context(), "lib@example.org", FormatNQuads, Selection{WorkIDs: workIDs})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != StatusFailed {
		t.Fatalf("status = %s, want FAILED", job.Status)
	}
	if job.OutputPath != "" {
		t.Fatalf("a failed job advertises outputPath %q", job.OutputPath)
	}
	if _, _, err := bs.Get(t.Context(), DeliveryFor(job.ID, FormatNQuads).Path); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("an object survived the aborted emit: %v", err)
	}
}

// DeliveryFor is the single source of truth Run writes through and the download
// handler reads back. Drift here silently mislabels every download.
func TestDeliveryForMatchesWhatRunWrote(t *testing.T) {
	for _, f := range []Format{FormatCSV, FormatNQuads, FormatJSONLD, FormatMARC} {
		t.Run(string(f), func(t *testing.T) {
			_, _, job := runFormat(t, f)
			if got := DeliveryFor(job.ID, f).Path; got != job.OutputPath {
				t.Fatalf("DeliveryFor path = %q, job wrote %q", got, job.OutputPath)
			}
		})
	}
}
