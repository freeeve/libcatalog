package publish_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/publish"
)

const grainPath = "data/works/31/w0000000000001.nq"

// pathErr is what blob.DirStore hands back when it cannot create its temp file.
func pathErr() error {
	return &os.PathError{Op: "open", Path: "/srv/libcat/site/data/works/31/.blob-12955389", Err: syscall.EACCES}
}

// failStore fails Get or Put with a chosen error; the other passes through.
type failStore struct {
	blob.Store
	getErr, putErr error
}

func (s *failStore) Get(ctx context.Context, p string) ([]byte, string, error) {
	if s.getErr != nil {
		return nil, "", s.getErr
	}
	return s.Store.Get(ctx, p)
}

func (s *failStore) Put(ctx context.Context, p string, b []byte, o blob.PutOptions) (string, error) {
	if s.putErr != nil {
		return "", s.putErr
	}
	return s.Store.Put(ctx, p, b, o)
}

func keep(old []byte) ([]byte, error) { return append([]byte("x "), old...), nil }

func seeded(t *testing.T) blob.Store {
	t.Helper()
	st := blob.NewMem()
	if _, err := st.Put(t.Context(), grainPath, []byte("seed"), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	return st
}

// MutateGrain returned the store's read error, the store's write
// error and the caller's own mutate error the same way, so no caller could sort
// them. The store's two now wrap ErrGrainStore.
func TestMutateGrainWrapsAWriteFailure(t *testing.T) {
	st := &failStore{Store: seeded(t), putErr: pathErr()}
	_, err := publish.MutateGrain(t.Context(), st, grainPath, keep)

	if !errors.Is(err, publish.ErrGrainStore) {
		t.Fatalf("err = %v, want ErrGrainStore", err)
	}
	var pe *os.PathError
	if !errors.As(err, &pe) {
		t.Fatal("the store's own error must stay reachable for the operator's log")
	}
}

func TestMutateGrainWrapsAReadFailure(t *testing.T) {
	st := &failStore{Store: seeded(t), getErr: pathErr()}
	_, err := publish.MutateGrain(t.Context(), st, grainPath, keep)

	if !errors.Is(err, publish.ErrGrainStore) {
		t.Fatalf("err = %v, want ErrGrainStore", err)
	}
}

// A read-only store is a deployment policy, not a fault. Its sentinel must
// survive the wrap or every caller reports a broken server -- the %v-versus-%w
// defect found in the single-record route's own CAS loop.
func TestMutateGrainKeepsTheStoreSentinelReachable(t *testing.T) {
	_, err := publish.MutateGrain(t.Context(), blob.ReadOnly(seeded(t)), grainPath, keep)

	if !errors.Is(err, publish.ErrGrainStore) {
		t.Fatalf("err = %v, want ErrGrainStore", err)
	}
	if !errors.Is(err, blob.ErrReadOnly) {
		t.Fatalf("err = %v: blob.ErrReadOnly did not survive the wrap", err)
	}
}

// A missing grain is not a store failure: MutateGrain treats ErrNotFound as
// "create it", and the closure decides what that means.
func TestMutateGrainTreatsNotFoundAsCreate(t *testing.T) {
	etag, err := publish.MutateGrain(t.Context(), blob.NewMem(), grainPath, keep)
	if err != nil {
		t.Fatalf("err = %v, want a created grain", err)
	}
	if etag == "" {
		t.Fatal("no etag")
	}
}

// The closure's error is the caller's own, phrased for whoever asked. Wrapping
// it would force every caller to unwrap before it could show the message.
func TestMutateGrainPassesTheMutateErrorThrough(t *testing.T) {
	mine := errors.New("no such work")
	_, err := publish.MutateGrain(t.Context(), seeded(t), grainPath, func([]byte) ([]byte, error) {
		return nil, mine
	})
	if !errors.Is(err, mine) {
		t.Fatalf("err = %v, want the closure's error", err)
	}
	if errors.Is(err, publish.ErrGrainStore) {
		t.Fatal("the closure's error was misfiled as a store failure")
	}
	if err.Error() != "no such work" {
		t.Fatalf("err = %q, want it verbatim", err.Error())
	}
}

// Exhausting the CAS retries used to return "publish: <blob path>: conditional
// write kept failing", and that string reached the client.
func TestMutateGrainConflictNamesNoPath(t *testing.T) {
	st := &failStore{Store: seeded(t), putErr: blob.ErrPreconditionFailed}
	_, err := publish.MutateGrain(t.Context(), st, grainPath, keep)

	if !errors.Is(err, publish.ErrGrainConflict) {
		t.Fatalf("err = %v, want ErrGrainConflict", err)
	}
	if strings.Contains(err.Error(), grainPath) || strings.Contains(err.Error(), "/") {
		t.Fatalf("the conflict error names a path: %q", err.Error())
	}
	// A conflict is not a store fault; a caller mapping ErrGrainStore to a 500
	// must not swallow it.
	if errors.Is(err, publish.ErrGrainStore) {
		t.Fatal("a lost race is not a broken store")
	}
}
