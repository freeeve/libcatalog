package batch_test

import (
	"context"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/batch"
	"github.com/freeeve/libcat/backend/editor"
	"github.com/freeeve/libcat/backend/profiles"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
)

// pathError is the error blob.DirStore actually produces when it cannot create
// its temp file -- an *os.PathError naming the blob root, the shard, and the
// temp-file naming scheme. The reporter saw this rendered into the results list.
func pathError() error {
	return &os.PathError{
		Op:   "open",
		Path: "/var/folders/34/_z7403jx0bn7xgtss8vvfpnw0000gn/T/libcat-e2e/site-rw/data/works/31/.blob-12955389",
		Err:  syscall.EACCES,
	}
}

// putFailBlob fails Put on one grain path and behaves normally elsewhere, so a
// batch can hold a broken record and a healthy one -- the reporter's W1 control.
type putFailBlob struct {
	blob.Store
	failPath string
	err      error
}

func (b *putFailBlob) Put(ctx context.Context, path string, data []byte, opts blob.PutOptions) (string, error) {
	if path == b.failPath {
		return "", b.err
	}
	return b.Store.Put(ctx, path, data, opts)
}

// conflictBlob always fails the conditional write with ErrPreconditionFailed, so
// MutateGrain exhausts its retries. That used to surface as an error naming the
// blob path; it is ErrGrainConflict now.
type conflictBlob struct{ blob.Store }

func (b *conflictBlob) Put(context.Context, string, []byte, blob.PutOptions) (string, error) {
	return "", blob.ErrPreconditionFailed
}

// serviceOver builds a batch service over st, seeded with the standard works.
func serviceOver(t *testing.T, st blob.Store, logger *slog.Logger) *batch.Service {
	t.Helper()
	set, err := profiles.LoadDefaults()
	if err != nil {
		t.Fatal(err)
	}
	return &batch.Service{
		Blob: st, DB: store.NewMem(),
		Mapper: &editor.Mapper{WorkProfile: set["work-monograph"], InstanceProfile: set["instance-ebook"]},
		Queue:  suggest.New(store.NewMem(), nil, suggest.Caps{}),
		Logger: logger,
	}
}

func seededMem(t *testing.T) blob.Store {
	t.Helper()
	st := blob.NewMem()
	seedWork(t, st, "wbatch0000001", "Gideon the Ninth", "space opera")
	seedWork(t, st, "wbatch0000002", "Harrow the Ninth", "space opera")
	return st
}

func tagOps() []editor.Op {
	return []editor.Op{{Resource: "work", Path: "tags", Action: "add", Value: &editor.OpValue{V: "zz-e2e"}}}
}

func runIDs(t *testing.T, svc *batch.Service, ids ...string) batch.RunResult {
	t.Helper()
	run, err := svc.Run(t.Context(), batch.Selection{Kind: batch.KindIDs, IDs: ids}, tagOps(), false, "lib@example.org")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return run
}

func resultFor(t *testing.T, run batch.RunResult, workID string) batch.ItemResult {
	t.Helper()
	for _, r := range run.Results {
		if r.WorkID == workID {
			return r
		}
	}
	t.Fatalf("no result for %s in %+v", workID, run.Results)
	return batch.ItemResult{}
}

// leaky is the property the reporter's W2 asserts, deliberately not keyed on the
// string this build happens to produce: a message must name no filesystem path,
// no syscall, and no temp file. A check for "permission denied" would pass
// vacuously the day the error text changes.
var leaky = regexp.MustCompile(`(?i)(/[a-z0-9_.-]+/|\.blob-|permission denied|no such file|syscall|EACCES|open |os\.PathError|blob:)`)

func assertNotLeaky(t *testing.T, msg string) {
	t.Helper()
	if msg == "" {
		t.Fatal("no message at all: a failed record must say something")
	}
	if m := leaky.FindString(msg); m != "" {
		t.Fatalf("the message leaks %q to the cataloger: %q", m, msg)
	}
}

// the raw *os.PathError went into ItemResult.Error and straight on to
// the screen. The single-record route answers "grain write failed" for the
// identical failure; so does this one now.
func TestBatchWriteFailureDoesNotLeakTheStoreError(t *testing.T) {
	st := seededMem(t)
	broken := &putFailBlob{Store: st, failPath: bibframe.GrainPath("wbatch0000001"), err: pathError()}
	svc := serviceOver(t, broken, nil)

	run := runIDs(t, svc, "wbatch0000001", "wbatch0000002")

	failed := resultFor(t, run, "wbatch0000001")
	assertNotLeaky(t, failed.Error)
	if failed.Error != "grain write failed" {
		t.Fatalf("message = %q, want the single-record route's wording", failed.Error)
	}
	// W1: the healthy work in the same batch still applies, so the failure is
	// scoped to one shard rather than a broken instance.
	healthy := resultFor(t, run, "wbatch0000002")
	if healthy.Error != "" || healthy.ETag == "" {
		t.Fatalf("the healthy work did not apply: %+v", healthy)
	}
}

// The closure's own errors are the cataloger's answer, and mapping must not eat
// them. "no such work" is the read path's wording and the write path's too.
func TestBatchWriteFailurePassesTheMutateErrorThrough(t *testing.T) {
	svc := serviceOver(t, seededMem(t), nil)
	run := runIDs(t, svc, "wbatch0000002", "wmissing00001")

	missing := resultFor(t, run, "wmissing00001")
	if missing.Error != "no such work" {
		t.Fatalf("message = %q, want the closure's own error verbatim", missing.Error)
	}
}

// An op the mapper rejects is also the closure's error, and also the answer.
func TestBatchWriteFailurePassesAnOpValidationErrorThrough(t *testing.T) {
	svc := serviceOver(t, seededMem(t), nil)
	ops := []editor.Op{{Resource: "work", Path: "not-a-field", Action: "add", Value: &editor.OpValue{V: "x"}}}
	run, err := svc.Run(t.Context(), batch.Selection{Kind: batch.KindIDs, IDs: []string{"wbatch0000001"}}, ops, false, "lib@example.org")
	if err != nil {
		// A rejected op may fail the whole run rather than one record; either
		// way it must not be swallowed and must not be a store message.
		assertNotLeaky(t, err.Error())
		return
	}
	got := resultFor(t, run, "wbatch0000001").Error
	if got == "" || got == "grain write failed" {
		t.Fatalf("message = %q, want the mapper's own complaint", got)
	}
	assertNotLeaky(t, got)
}

// A read-only deployment is a policy, not a fault, and the batch route is
// allowlisted so it reaches the store. Every record used to report
// the blob package's internal name, "blob: store is read-only".
func TestBatchWriteFailureReportsReadOnlyLikeTheGuard(t *testing.T) {
	svc := serviceOver(t, blob.ReadOnly(seededMem(t)), nil)
	run := runIDs(t, svc, "wbatch0000001")

	got := resultFor(t, run, "wbatch0000001").Error
	assertNotLeaky(t, got)
	if got != batch.ReadOnlyNotice {
		t.Fatalf("message = %q, want %q", got, batch.ReadOnlyNotice)
	}
}

// CAS exhaustion used to return "publish: <blob path>: conditional write kept
// failing", which reached the client the same way.
func TestBatchWriteFailureReportsAConflictWithoutThePath(t *testing.T) {
	svc := serviceOver(t, &conflictBlob{Store: seededMem(t)}, nil)
	run := runIDs(t, svc, "wbatch0000001")

	got := resultFor(t, run, "wbatch0000001").Error
	assertNotLeaky(t, got)
	if !strings.Contains(got, "retry") {
		t.Fatalf("message = %q, want a retryable-conflict message", got)
	}
}

// The operator needs the path the cataloger must not see. Before it
// was rendered and never logged: the one reader who could act on it never saw it.
func TestBatchWriteFailureLogsTheRawErrorForTheOperator(t *testing.T) {
	var sb strings.Builder
	logger := slog.New(slog.NewTextHandler(&sb, &slog.HandlerOptions{Level: slog.LevelError}))
	st := seededMem(t)
	broken := &putFailBlob{Store: st, failPath: bibframe.GrainPath("wbatch0000001"), err: pathError()}

	runIDs(t, serviceOver(t, broken, logger), "wbatch0000001")

	logged := sb.String()
	if !strings.Contains(logged, ".blob-12955389") {
		t.Fatalf("the raw error never reached the log: %q", logged)
	}
	if !strings.Contains(logged, "wbatch0000001") {
		t.Fatalf("the log does not say which work failed: %q", logged)
	}
}

// A nil logger is the zero value and must not panic: most tests, and any
// embedder that never set one, run this path.
func TestBatchWriteFailureWithoutALoggerDoesNotPanic(t *testing.T) {
	st := seededMem(t)
	broken := &putFailBlob{Store: st, failPath: bibframe.GrainPath("wbatch0000001"), err: pathError()}
	run := runIDs(t, serviceOver(t, broken, nil), "wbatch0000001")
	if resultFor(t, run, "wbatch0000001").Error != "grain write failed" {
		t.Fatal("mapping must not depend on a logger being present")
	}
}

// The store's own sentinel must survive publish.MutateGrain's wrap, or a
// read-only deployment reads as a broken one. This is the %v-versus-%w defect
// found one layer up, asserted here at the layer that wraps.
func TestBatchWriteFailurePreservesTheStoreSentinel(t *testing.T) {
	st := seededMem(t)
	broken := &putFailBlob{Store: st, failPath: bibframe.GrainPath("wbatch0000001"), err: blob.ErrReadOnly}
	run := runIDs(t, serviceOver(t, broken, nil), "wbatch0000001")
	if got := resultFor(t, run, "wbatch0000001").Error; got != batch.ReadOnlyNotice {
		t.Fatalf("message = %q: blob.ErrReadOnly did not survive the wrap", got)
	}
}

// The read path already mapped its errors; this pins that it still does, so a
// future edit cannot fix the write path by breaking the read path's precedent.
func TestBatchReadFailureStillMapsItsError(t *testing.T) {
	svc := serviceOver(t, seededMem(t), nil)
	run, err := svc.Run(t.Context(), batch.Selection{Kind: batch.KindIDs, IDs: []string{"wmissing00001"}}, tagOps(), true, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if got := resultFor(t, run, "wmissing00001").Error; got != "no such work" {
		t.Fatalf("dry-run read error = %q, want %q", got, "no such work")
	}
}
