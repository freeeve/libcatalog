package copycat_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	codex "github.com/freeeve/libcodex"

	"github.com/freeeve/libcat/backend/copycat"
)

// hit builds a minimal MARC record so a fan-out result is recognizable by title.
func hit(title string) *codex.Record {
	r := codex.NewRecord()
	r.AddField(codex.NewControlField("001", title))
	r.AddField(codex.NewDataField("245", '1', '0', codex.NewSubfield('a', title)))
	return r
}

// twoTargets configures a fan-out over "alpha" and "beta" and installs search.
func twoTargets(t *testing.T, search copycat.SearchFunc) (*copycat.Service, context.Context) {
	t.Helper()
	svc, _, _ := newService(t)
	ctx := t.Context()
	for _, name := range []string{"alpha", "beta"} {
		if err := svc.PutTarget(ctx, copycat.Target{Name: name, URL: "x", Protocol: "sru"}); err != nil {
			t.Fatal(err)
		}
	}
	svc.Search = search
	return svc, ctx
}

func titles(results []copycat.SearchResult) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		out = append(out, r.Title)
	}
	return out
}

// the whole of it: a stream that breaks after delivering records is
// neither a success nor a failure. Its hits must survive the fan-out, and its
// reason must reach the client -- as a warning, because a failure would suppress
// the hits and a silent success would claim the short set is the whole set.
func TestSearchAllReportsAPartialStreamAsAWarningAndKeepsItsHits(t *testing.T) {
	broke := errors.New("sru: parse response: XML syntax error")
	svc, ctx := twoTargets(t, func(_ context.Context, tgt copycat.Target, _ []copycat.FieldTerm, _ int) ([]*codex.Record, error) {
		if tgt.Name == "beta" {
			return []*codex.Record{hit("Beta Book")}, &copycat.PartialError{Got: 1, Err: broke}
		}
		return []*codex.Record{hit("Alpha Book")}, nil
	})

	results, failures, warnings, err := svc.SearchAll(ctx, "gideon", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := titles(results); len(got) != 2 {
		t.Fatalf("results = %v, want both targets' hits: the partial target's record was dropped", got)
	}
	if failures["beta"] != "" {
		t.Fatalf("failures = %v, want none: a partial answer is not a failed one", failures)
	}
	if warnings["beta"] == "" {
		t.Fatal("the broken stream was reported as a clean success -- this is the bug")
	}
	if !strings.Contains(warnings["beta"], "XML syntax error") {
		t.Fatalf("warning = %q, want the underlying reason", warnings["beta"])
	}
	if warnings["alpha"] != "" {
		t.Fatalf("warnings = %v, want nothing for the clean target", warnings)
	}
}

// The same error before any record is still a failure, and still suppresses
// nothing else: alpha's hits come back regardless. This is the page-1 half of
// the report -- it worked before, and must keep working.
func TestSearchAllStillReportsAnEmptyBreakAsAFailure(t *testing.T) {
	svc, ctx := twoTargets(t, func(_ context.Context, tgt copycat.Target, _ []copycat.FieldTerm, _ int) ([]*codex.Record, error) {
		if tgt.Name == "beta" {
			return nil, errors.New("sru: parse response: XML syntax error")
		}
		return []*codex.Record{hit("Alpha Book")}, nil
	})

	results, failures, warnings, err := svc.SearchAll(ctx, "gideon", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %v, want alpha's hit", titles(results))
	}
	if failures["beta"] == "" {
		t.Fatalf("failures = %v, want beta", failures)
	}
	if warnings["beta"] != "" {
		t.Fatalf("warnings = %v, want none: nothing arrived, so nothing is partial", warnings)
	}
}

// Page 1 and page 2 are the same error. The remote server's page size decides
// which one a cataloger meets, so the two must not answer differently in kind:
// both are reported, neither is silent.
func TestSearchAllNeverSilentlySwallowsAStreamError(t *testing.T) {
	broke := errors.New("connection reset by peer")
	for _, tc := range []struct {
		name string
		recs []*codex.Record
		err  error
	}{
		{"breaks on the first read", nil, broke},
		{"breaks after one page", []*codex.Record{hit("Beta Book")}, &copycat.PartialError{Got: 1, Err: broke}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, ctx := twoTargets(t, func(_ context.Context, tgt copycat.Target, _ []copycat.FieldTerm, _ int) ([]*codex.Record, error) {
				if tgt.Name == "beta" {
					return tc.recs, tc.err
				}
				return nil, nil
			})
			_, failures, warnings, err := svc.SearchAll(ctx, "gideon", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if failures["beta"] == "" && warnings["beta"] == "" {
				t.Fatal("beta's stream broke and the client is told nothing")
			}
		})
	}
}

// A capped target answered fully as far as it went, but the set is short. The
// cataloger asking "is my book in this catalog?" is answered wrongly by a
// truncated set, so the cap is a warning too.
func TestSearchAllReportsTheSearchCapAsAWarning(t *testing.T) {
	svc, ctx := twoTargets(t, func(_ context.Context, tgt copycat.Target, _ []copycat.FieldTerm, _ int) ([]*codex.Record, error) {
		if tgt.Name == "beta" {
			return []*codex.Record{hit("Beta Book")}, copycat.ErrCapped
		}
		return []*codex.Record{hit("Alpha Book")}, nil
	})

	results, failures, warnings, err := svc.SearchAll(ctx, "gideon", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %v, want both: a capped target's hits are real hits", titles(results))
	}
	if len(failures) != 0 {
		t.Fatalf("failures = %v, want none", failures)
	}
	if warnings["beta"] == "" {
		t.Fatal("a truncated result set was presented as complete")
	}
}

// Incomplete is the seam every caller uses to tell "these records, but not all
// of them" from "this search failed". Getting it wrong in either direction
// either hides hits or hides breakage.
func TestIncompleteClassifiesEachOutcome(t *testing.T) {
	broke := errors.New("connection reset by peer")
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"a clean stream", nil, false},
		{"an outright failure", broke, false},
		{"a broken stream that delivered records", &copycat.PartialError{Got: 3, Err: broke}, true},
		{"the search cap", copycat.ErrCapped, true},
		{"a wrapped cap", errors.Join(copycat.ErrCapped, errors.New("showing the first 20")), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := copycat.Incomplete(tc.err); got != tc.want {
				t.Fatalf("Incomplete(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// A PartialError keeps the stream's error reachable: a caller that wants to
// distinguish a timeout from a malformed response must not have to parse a
// string to do it.
func TestPartialErrorUnwrapsToTheStreamError(t *testing.T) {
	broke := errors.New("context deadline exceeded")
	err := error(&copycat.PartialError{Got: 2, Err: broke})
	if !errors.Is(err, broke) {
		t.Fatalf("the stream error was lost: %v", err)
	}
	if !strings.Contains(err.Error(), "2 record") {
		t.Fatalf("message = %q, want the count that arrived", err.Error())
	}
}
