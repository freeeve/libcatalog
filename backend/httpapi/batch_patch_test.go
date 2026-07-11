package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/editor"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
)

const otherWorkID = "wxyz789ghi012"

// seedOtherWork adds a second grain, so a batch can span two works -- the case
// the original batch test never covered, and the reason went unseen.
func seedOtherWork(t *testing.T, bs blob.Store) {
	t.Helper()
	ds := &rdf.Dataset{}
	ds.Add(rdf.NewIRI(bibframe.WorkIRI(otherWorkID)),
		rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/title"),
		rdf.NewLiteral("Another Book", "", ""), bibframe.FeedGraph("overdrive"))
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(otherWorkID), nq, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
}

// newRecordsAPIWithQueue is newRecordsAPI plus a reader over the same document
// store, so a test can see the audit trail the handlers write.
func newRecordsAPIWithQueue(t *testing.T) (http.Handler, blob.Store, *suggest.Service) {
	t.Helper()
	bs := blob.NewMem()
	db := store.NewMem()
	verifier := staffVerifier{
		"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}},
		"mod-token": {Email: "mod@example.org", Roles: []auth.Role{auth.RoleModerator}},
	}
	queue := suggest.New(db, nil, suggest.Caps{})
	return New(Deps{Blob: bs, DB: db, Verifier: verifier, Suggest: queue}), bs, queue
}

type batchResults struct {
	Results []struct {
		WorkID string       `json:"workId"`
		ETag   string       `json:"etag"`
		Diff   *editor.Diff `json:"diff"`
		Error  string       `json:"error"`
	} `json:"results"`
}

func postBatch(t *testing.T, h http.Handler, patch editor.Patch, ids []string, dryRun bool) (int, batchResults) {
	t.Helper()
	rec := request(t, h, http.MethodPost, "/v1/batch", "lib-token", "", map[string]any{
		"workIds": ids, "patch": patch, "dryRun": dryRun,
	})
	var out batchResults
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// a batch patch carries one literal subject. Applied verbatim to
// every selected work, it writes quads describing the first work into every
// other work's grain -- and the dry run reports the change as applied to each,
// so nothing about the preview reveals it. Each work's own Work node is the
// only subject an edit to that work may name.
func TestBatchPatchRebindsTheSubjectToEachWork(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedWorkGrain(t, bs)
	seedOtherWork(t, bs)

	patch := subjectPatch("https://homosaurus.org/v4/batch")
	code, out := postBatch(t, h, patch, []string{editWorkID, otherWorkID}, false)
	if code != http.StatusOK {
		t.Fatalf("batch: %d", code)
	}
	if len(out.Results) != 2 || out.Results[0].Error != "" || out.Results[1].Error != "" {
		t.Fatalf("results = %+v", out.Results)
	}
	// The second work's grain must describe the second work, never the first.
	got, _, err := bs.Get(t.Context(), bibframe.GrainPath(otherWorkID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), bibframe.WorkIRI(editWorkID)) {
		t.Fatalf("%s's grain describes %s:\n%s", otherWorkID, editWorkID, got)
	}
	if !strings.Contains(string(got), bibframe.WorkIRI(otherWorkID)) || !strings.Contains(string(got), "v4/batch") {
		t.Fatalf("%s did not get its own subject statement:\n%s", otherWorkID, got)
	}
	// And the first work still got the edit, bound to itself.
	got, _, err = bs.Get(t.Context(), bibframe.GrainPath(editWorkID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), bibframe.WorkIRI(editWorkID)) || !strings.Contains(string(got), "v4/batch") {
		t.Fatalf("%s did not get the edit:\n%s", editWorkID, got)
	}
	if strings.Contains(string(got), bibframe.WorkIRI(otherWorkID)) {
		t.Fatalf("%s's grain describes %s:\n%s", editWorkID, otherWorkID, got)
	}
}

// The dry run must preview what execute will do. Before the fix it diffed the
// verbatim patch against each grain, so it agreed with a write it never made.
func TestBatchPatchDryRunPreviewsTheRebind(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedWorkGrain(t, bs)
	seedOtherWork(t, bs)

	code, out := postBatch(t, h, subjectPatch("https://homosaurus.org/v4/batch"), []string{otherWorkID}, true)
	if code != http.StatusOK || len(out.Results) != 1 || out.Results[0].Diff == nil {
		t.Fatalf("dry run: %d %+v", code, out.Results)
	}
	added := strings.Join(out.Results[0].Diff.Added, "\n")
	if strings.Contains(added, bibframe.WorkIRI(editWorkID)) {
		t.Fatalf("dry run previews a quad about %s:\n%s", editWorkID, added)
	}
	if !strings.Contains(added, bibframe.WorkIRI(otherWorkID)) {
		t.Fatalf("dry run does not preview a quad about %s:\n%s", otherWorkID, added)
	}
	// Dry run writes nothing.
	got, _, _ := bs.Get(t.Context(), bibframe.GrainPath(otherWorkID))
	if strings.Contains(string(got), "v4/batch") {
		t.Fatal("dry run wrote")
	}
}

// A subject that is not a Work node cannot be rebound: an Instance id names a
// node in one grain and nothing at all in another. Refuse the request rather
// than guess, and refuse it whole rather than per record -- the caller's patch
// is malformed, not their selection.
func TestBatchPatchRefusesUnbindableSubjects(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedWorkGrain(t, bs)

	cases := []struct {
		name    string
		subject string
	}{
		{"an instance node", bibframe.InstanceIRI("iabc123def456")},
		{"a skolem node", "#" + editWorkID + "Work-ed-title"},
		{"an absolute IRI", "https://example.org/work/1"},
		{"another grain's blank-ish node", "#somethingelse"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			patch := editor.Patch{Add: []editor.Statement{{
				S: tc.subject,
				P: "http://id.loc.gov/ontologies/bibframe/subject",
				O: editor.Term{Kind: "iri", Value: "https://homosaurus.org/v4/batch"},
			}}}
			code, _ := postBatch(t, h, patch, []string{editWorkID}, false)
			if code != http.StatusBadRequest {
				t.Fatalf("subject %q accepted with %d, want 400", tc.subject, code)
			}
			got, _, _ := bs.Get(t.Context(), bibframe.GrainPath(editWorkID))
			if strings.Contains(string(got), "v4/batch") {
				t.Fatalf("refused request still wrote:\n%s", got)
			}
		})
	}
}

// A grain-local object names a node in the origin grain, which means nothing in
// any other. Objects must be absolute IRIs or literals.
func TestBatchPatchRefusesGrainLocalObjects(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedWorkGrain(t, bs)
	patch := editor.Patch{Add: []editor.Statement{{
		S: bibframe.WorkIRI(editWorkID),
		P: "http://id.loc.gov/ontologies/bibframe/subject",
		O: editor.Term{Kind: "iri", Value: "#" + editWorkID + "n4"},
	}}}
	code, _ := postBatch(t, h, patch, []string{editWorkID}, false)
	if code != http.StatusBadRequest {
		t.Fatalf("grain-local object accepted with %d, want 400", code)
	}
}

// The same corruption has a front door: PUT /v1/works/{id} takes the same raw
// patch. A subject naming another work wrote a quad about that work into this
// work's grain -- one record at a time instead of five hundred. Nothing can
// rebind a single-record patch (the caller named the work in the URL), so it is
// refused.
func TestPutWorkRefusesAnotherWorksSubject(t *testing.T) {
	h, bs := newRecordsAPI(t)
	grain := seedWorkGrain(t, bs)
	_, etag, err := bs.Get(t.Context(), bibframe.GrainPath(editWorkID))
	if err != nil {
		t.Fatal(err)
	}
	_ = grain

	// subjectPatch names editWorkID; aim it at otherWorkID's URL.
	seedOtherWork(t, bs)
	rec := request(t, h, http.MethodPut, "/v1/works/"+editWorkID, "lib-token", etag,
		editor.Patch{Add: []editor.Statement{{
			S: bibframe.WorkIRI(otherWorkID),
			P: "http://id.loc.gov/ontologies/bibframe/subject",
			O: editor.Term{Kind: "iri", Value: "https://homosaurus.org/v4/batch"},
		}}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("cross-work subject accepted with %d, want 400: %s", rec.Code, rec.Body)
	}
	got, _, _ := bs.Get(t.Context(), bibframe.GrainPath(editWorkID))
	if strings.Contains(string(got), bibframe.WorkIRI(otherWorkID)) {
		t.Fatalf("%s's grain describes %s:\n%s", editWorkID, otherWorkID, got)
	}
}

// The preview must refuse what the write refuses.
func TestValidateRefusesAnotherWorksSubject(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedWorkGrain(t, bs)
	seedOtherWork(t, bs)
	rec := request(t, h, http.MethodPost, "/v1/works/"+editWorkID+"/validate", "lib-token", "",
		editor.Patch{Add: []editor.Statement{{
			S: bibframe.WorkIRI(otherWorkID),
			P: "http://id.loc.gov/ontologies/bibframe/subject",
			O: editor.Term{Kind: "iri", Value: "https://homosaurus.org/v4/batch"},
		}}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("validate accepted a cross-work subject with %d: %s", rec.Code, rec.Body)
	}
}

// A single-record patch still mints its own skolem and instance nodes: the
// guard names Work nodes only, so the editor's real write shapes are untouched.
func TestPutWorkStillAcceptsItsOwnNodes(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedWorkGrain(t, bs)
	_, etag, err := bs.Get(t.Context(), bibframe.GrainPath(editWorkID))
	if err != nil {
		t.Fatal(err)
	}
	for _, subject := range []string{
		bibframe.WorkIRI(editWorkID),
		bibframe.WorkIRI(editWorkID) + "-ed-title",
		bibframe.InstanceIRI("iabc123def456"),
	} {
		rec := request(t, h, http.MethodPut, "/v1/works/"+editWorkID, "lib-token", etag,
			editor.Patch{Add: []editor.Statement{{
				S: subject,
				P: "http://www.w3.org/2000/01/rdf-schema#label",
				O: editor.Term{Kind: "literal", Value: "ok"},
			}}})
		if rec.Code != http.StatusOK {
			t.Fatalf("subject %q refused with %d: %s", subject, rec.Code, rec.Body)
		}
		_, etag, _ = bs.Get(t.Context(), bibframe.GrainPath(editWorkID))
	}
}

// POST /v1/batch wrote one audit entry carrying no work id, so every
// record it rewrote read "0 entries" in its own History tab. Its note was a JSON
// blob of the results cut at 512 bytes, which past ~7 works stops being
// parseable and can split a UTF-8 rune at the boundary.
func TestBatchPatchAuditsEveryChangedRecord(t *testing.T) {
	h, bs, queue := newRecordsAPIWithQueue(t)
	seedWorkGrain(t, bs)
	seedOtherWork(t, bs)

	code, _ := postBatch(t, h, subjectPatch("https://homosaurus.org/v4/batch"),
		[]string{editWorkID, otherWorkID, "wmissing00001"}, false)
	if code != http.StatusOK {
		t.Fatalf("batch: %d", code)
	}
	entries, err := queue.Audit(t.Context(), time.Now().UTC().Format("2006-01"))
	if err != nil {
		t.Fatal(err)
	}
	perWork := map[string]suggest.AuditEntry{}
	var aggregate []suggest.AuditEntry
	for _, e := range entries {
		if e.Action != "BATCH_EDIT" {
			continue
		}
		if e.WorkID == "" {
			aggregate = append(aggregate, e)
			continue
		}
		perWork[e.WorkID] = e
	}
	if len(aggregate) != 1 {
		t.Fatalf("want one aggregate entry, got %d", len(aggregate))
	}
	if len(perWork) != 2 {
		t.Fatalf("want a per-record entry for each of the two real works, got %d: %v", len(perWork), perWork)
	}
	if _, ok := perWork["wmissing00001"]; ok {
		t.Fatal("the missing work was audited as edited")
	}
	for id, e := range perWork {
		if e.ETag == "" || e.RunID == "" || e.RunID != aggregate[0].RunID {
			t.Fatalf("%s entry = %+v (aggregate runId %q)", id, e, aggregate[0].RunID)
		}
	}
	// The aggregate note stays valid JSON and names the records it rewrote.
	note := aggregate[0].Note
	if !utf8.ValidString(note) {
		t.Fatalf("the aggregate note is not valid UTF-8: %q", note)
	}
	var parsed suggest.RunNote
	if err := json.Unmarshal([]byte(note), &parsed); err != nil {
		t.Fatalf("the aggregate note is not parseable JSON: %v\n%q", err, note)
	}
	if parsed.Matched != 3 || parsed.Rewritten != 2 || parsed.Failed != 1 {
		t.Fatalf("aggregate note does not summarize the run: %+v", parsed)
	}
	for _, id := range []string{editWorkID, otherWorkID} {
		if !strings.Contains(note, id) {
			t.Fatalf("the aggregate note does not name %s: %q", id, note)
		}
	}
	if strings.Contains(note, "wmissing00001") {
		t.Fatalf("the aggregate note names the work it failed to write: %q", note)
	}
}

// Remove statements rebind too, or a batch retraction would silently no-op on
// every work but the one the caller happened to name.
func TestBatchPatchRebindsRemoveStatements(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedWorkGrain(t, bs)
	seedOtherWork(t, bs)
	ids := []string{editWorkID, otherWorkID}

	if code, _ := postBatch(t, h, subjectPatch("https://homosaurus.org/v4/batch"), ids, false); code != http.StatusOK {
		t.Fatalf("seed edit: %d", code)
	}
	removal := editor.Patch{Remove: subjectPatch("https://homosaurus.org/v4/batch").Add}
	if code, out := postBatch(t, h, removal, ids, false); code != http.StatusOK {
		t.Fatalf("removal: %d %+v", code, out.Results)
	}
	for _, id := range ids {
		got, _, _ := bs.Get(t.Context(), bibframe.GrainPath(id))
		if strings.Contains(string(got), "v4/batch") {
			t.Fatalf("%s kept the retracted subject:\n%s", id, got)
		}
	}
}
