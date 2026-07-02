package blob

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stores returns every Store implementation under test, each factory yielding
// a fresh empty store.
func stores() map[string]func(t *testing.T) Store {
	return map[string]func(t *testing.T) Store{
		"mem": func(t *testing.T) Store { return NewMem() },
		"dir": func(t *testing.T) Store { return NewDir(t.TempDir()) },
	}
}

func TestStoreConformance(t *testing.T) {
	for name, mk := range stores() {
		t.Run(name, func(t *testing.T) {
			t.Run("GetMissing", func(t *testing.T) { testGetMissing(t, mk(t)) })
			t.Run("PutGetRoundTrip", func(t *testing.T) { testPutGetRoundTrip(t, mk(t)) })
			t.Run("ETagTracksContent", func(t *testing.T) { testETagTracksContent(t, mk(t)) })
			t.Run("IfNoneMatch", func(t *testing.T) { testIfNoneMatch(t, mk(t)) })
			t.Run("IfMatch", func(t *testing.T) { testIfMatch(t, mk(t)) })
			t.Run("ConflictingPreconditions", func(t *testing.T) { testConflictingPreconditions(t, mk(t)) })
			t.Run("Delete", func(t *testing.T) { testDelete(t, mk(t)) })
			t.Run("List", func(t *testing.T) { testList(t, mk(t)) })
			t.Run("ListEmpty", func(t *testing.T) { testListEmpty(t, mk(t)) })
			t.Run("InvalidPaths", func(t *testing.T) { testInvalidPaths(t, mk(t)) })
			t.Run("SinkOf", func(t *testing.T) { testSinkOf(t, mk(t)) })
		})
	}
}

func testGetMissing(t *testing.T, s Store) {
	_, _, err := s.Get(t.Context(), "no/such/object")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing: err = %v, want ErrNotFound", err)
	}
	if err := s.Delete(t.Context(), "no/such/object"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete missing: err = %v, want ErrNotFound", err)
	}
}

func testPutGetRoundTrip(t *testing.T, s Store) {
	want := []byte("grain content\n")
	putTag, err := s.Put(t.Context(), "data/works/ab/w1.nq", want, PutOptions{ContentType: "application/n-quads"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, getTag, err := s.Get(t.Context(), "data/works/ab/w1.nq")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("Get = %q, want %q", got, want)
	}
	if putTag == "" || putTag != getTag {
		t.Fatalf("etags: put %q vs get %q, want equal and non-empty", putTag, getTag)
	}
}

func testETagTracksContent(t *testing.T, s Store) {
	tag1, err := s.Put(t.Context(), "x", []byte("one"), PutOptions{})
	if err != nil {
		t.Fatalf("Put one: %v", err)
	}
	tag2, err := s.Put(t.Context(), "x", []byte("two"), PutOptions{})
	if err != nil {
		t.Fatalf("Put two: %v", err)
	}
	if tag1 == tag2 {
		t.Fatalf("etag unchanged across content change: %q", tag1)
	}
	tag3, err := s.Put(t.Context(), "x", []byte("one"), PutOptions{})
	if err != nil {
		t.Fatalf("Put one again: %v", err)
	}
	if tag3 != tag1 {
		t.Fatalf("etag not deterministic for same content: %q vs %q", tag3, tag1)
	}
}

func testIfNoneMatch(t *testing.T, s Store) {
	if _, err := s.Put(t.Context(), "x", []byte("first"), PutOptions{IfNoneMatch: true}); err != nil {
		t.Fatalf("create-only Put on empty path: %v", err)
	}
	if _, err := s.Put(t.Context(), "x", []byte("second"), PutOptions{IfNoneMatch: true}); !errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("create-only Put on existing: err = %v, want ErrPreconditionFailed", err)
	}
	got, _, err := s.Get(t.Context(), "x")
	if err != nil || string(got) != "first" {
		t.Fatalf("losing Put clobbered object: %q, %v", got, err)
	}
}

func testIfMatch(t *testing.T, s Store) {
	if _, err := s.Put(t.Context(), "x", []byte("v"), PutOptions{IfMatch: "anything"}); !errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("IfMatch on missing object: err = %v, want ErrPreconditionFailed", err)
	}
	tag, err := s.Put(t.Context(), "x", []byte("v1"), PutOptions{})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	tag2, err := s.Put(t.Context(), "x", []byte("v2"), PutOptions{IfMatch: tag})
	if err != nil {
		t.Fatalf("IfMatch with current etag: %v", err)
	}
	if _, err := s.Put(t.Context(), "x", []byte("v3"), PutOptions{IfMatch: tag}); !errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("IfMatch with stale etag: err = %v, want ErrPreconditionFailed", err)
	}
	got, gotTag, err := s.Get(t.Context(), "x")
	if err != nil || string(got) != "v2" || gotTag != tag2 {
		t.Fatalf("object after stale write: %q tag %q, want v2 tag %q (err %v)", got, gotTag, tag2, err)
	}
}

func testConflictingPreconditions(t *testing.T, s Store) {
	_, err := s.Put(t.Context(), "x", []byte("v"), PutOptions{IfMatch: "tag", IfNoneMatch: true})
	if err == nil || errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("conflicting preconditions: err = %v, want distinct validation error", err)
	}
}

func testDelete(t *testing.T, s Store) {
	if _, err := s.Put(t.Context(), "x", []byte("v"), PutOptions{}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Delete(t.Context(), "x"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := s.Get(t.Context(), "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete: err = %v, want ErrNotFound", err)
	}
}

func testList(t *testing.T, s Store) {
	objects := map[string]string{
		"data/works/aa/w1.nq":       "one",
		"data/works/ab/w2.nq":       "two22",
		"data/works/ab/w3.nq":       "three",
		"data/authorities/ho/a1.nq": "auth",
		"exports/job1.mrc":          "marc",
	}
	for p, v := range objects {
		if _, err := s.Put(t.Context(), p, []byte(v), PutOptions{}); err != nil {
			t.Fatalf("Put %s: %v", p, err)
		}
	}
	var got []Entry
	for e, err := range s.List(t.Context(), "data/works/") {
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		got = append(got, e)
	}
	wantPaths := []string{"data/works/aa/w1.nq", "data/works/ab/w2.nq", "data/works/ab/w3.nq"}
	if len(got) != len(wantPaths) {
		t.Fatalf("List returned %d entries, want %d: %v", len(got), len(wantPaths), got)
	}
	for i, e := range got {
		if e.Path != wantPaths[i] {
			t.Fatalf("List order: entry %d = %s, want %s", i, e.Path, wantPaths[i])
		}
		if e.Size != int64(len(objects[e.Path])) {
			t.Fatalf("List size for %s = %d, want %d", e.Path, e.Size, len(objects[e.Path]))
		}
		_, tag, err := s.Get(t.Context(), e.Path)
		if err != nil || tag != e.ETag {
			t.Fatalf("List etag for %s = %q, Get = %q (err %v)", e.Path, e.ETag, tag, err)
		}
	}
	// Early break must not panic or leak.
	for range s.List(t.Context(), "data/") {
		break
	}
}

func testListEmpty(t *testing.T, s Store) {
	for e, err := range s.List(t.Context(), "anything/") {
		t.Fatalf("List on empty store yielded %v, %v", e, err)
	}
}

func testInvalidPaths(t *testing.T, s Store) {
	for _, p := range []string{"", "/abs", "a//b", "a/./b", "../escape", "a/../b", ".."} {
		if _, err := s.Put(t.Context(), p, []byte("v"), PutOptions{}); err == nil {
			t.Fatalf("Put accepted invalid path %q", p)
		}
		if _, _, err := s.Get(t.Context(), p); err == nil || errors.Is(err, ErrNotFound) {
			t.Fatalf("Get accepted invalid path %q (err %v)", p, err)
		}
		if err := s.Delete(t.Context(), p); err == nil || errors.Is(err, ErrNotFound) {
			t.Fatalf("Delete accepted invalid path %q (err %v)", p, err)
		}
	}
}

func testSinkOf(t *testing.T, s Store) {
	sink := SinkOf(s)
	w, err := sink.Create("data/works/aa/w1.nq")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := w.Write([]byte("part one ")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := w.Write([]byte("part two")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got, _, err := s.Get(context.Background(), "data/works/aa/w1.nq")
	if err != nil || string(got) != "part one part two" {
		t.Fatalf("Get after sink write: %q, %v", got, err)
	}
	if _, err := sink.Create("../escape"); err == nil {
		t.Fatal("sink Create accepted invalid path")
	}
}

func TestValidatePath(t *testing.T) {
	valid := []string{"a", "a/b", "data/works/aa/w1.nq", "a.b/c-d_e", "..a/b.."}
	for _, p := range valid {
		if err := ValidatePath(p); err != nil {
			t.Errorf("ValidatePath(%q) = %v, want nil", p, err)
		}
	}
	invalid := []string{"", "/", "/a", "a/", "a//b", ".", "..", "a/..", "../a", "a/./b"}
	for _, p := range invalid {
		if err := ValidatePath(p); err == nil {
			t.Errorf("ValidatePath(%q) = nil, want error", p)
		}
	}
}

// FuzzValidatePath asserts the invariant that protects DirStore from
// path-traversal: any accepted path joins to a location inside the root.
func FuzzValidatePath(f *testing.F) {
	for _, seed := range []string{"a/b", "../x", "a/../../b", "", "/abs", "works/aa/w1.nq"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, path string) {
		if err := ValidatePath(path); err != nil {
			return
		}
		if strings.HasPrefix(path, "/") {
			t.Fatalf("accepted absolute path %q", path)
		}
		for seg := range strings.SplitSeq(path, "/") {
			if seg == "" || seg == "." || seg == ".." {
				t.Fatalf("accepted path %q with traversal segment %q", path, seg)
			}
		}
	})
}
