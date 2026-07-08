package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/authoritiesvc"
	"github.com/freeeve/libcat/backend/editor"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/vocab"
)

const authoritiesFixture = `<https://homosaurus.org/v4/homoit0001235> <http://www.w3.org/2004/02/skos/core#prefLabel> "Transgender people"@en <authority:homosaurus> .
<https://homosaurus.org/v4/homoit0000508> <http://www.w3.org/2004/02/skos/core#prefLabel> "Gender identity"@en <authority:homosaurus> .
`

// newAuthoritiesAPI wires the full handler with the authorities service and
// suggestion queue over in-memory stores.
func newAuthoritiesAPI(t *testing.T) (http.Handler, blob.Store, *suggest.Service) {
	t.Helper()
	bs := blob.NewMem()
	if _, err := bs.Put(t.Context(), "data/authorities/ho/homosaurus.nq", []byte(authoritiesFixture), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	ix, err := vocab.Load(t.Context(), bs, "data/authorities/", nil)
	if err != nil {
		t.Fatal(err)
	}
	db := store.NewMem()
	queue := suggest.New(db, ix, suggest.Caps{})
	svc := &authoritiesvc.Service{Blob: bs, Vocab: ix, Queue: queue}
	verifier := staffVerifier{
		"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}},
		"mod-token": {Email: "mod@example.org", Roles: []auth.Role{auth.RoleModerator}},
	}
	h := New(Deps{Blob: bs, DB: db, Verifier: verifier, Authorities: svc})
	return h, bs, queue
}

func TestAuthorityCRUDFlow(t *testing.T) {
	h, _, _ := newAuthoritiesAPI(t)

	// Below librarian is forbidden.
	if rec := request(t, h, http.MethodPost, "/v1/authorities", "mod-token", "", map[string]any{
		"prefLabel": map[string]string{"en": "X"},
	}); rec.Code != http.StatusForbidden {
		t.Fatalf("moderator create = %d", rec.Code)
	}

	// The editor form's profile is served (same profile mechanism as records).
	rec := request(t, h, http.MethodGet, "/v1/authorities/profile", "lib-token", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"authority-topic"`) {
		t.Fatalf("profile = %d %s", rec.Code, rec.Body.String())
	}

	// Create.
	rec = request(t, h, http.MethodPost, "/v1/authorities", "lib-token", "", map[string]any{
		"prefLabel": map[string]string{"en": "Cozy fantasy"},
		"altLabel":  map[string][]string{"en": {"Comfort fantasy"}},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	var created struct{ ID, URI, ETag string }
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !authoritiesvc.IDPattern.MatchString(created.ID) || created.ETag == "" {
		t.Fatalf("created = %+v", created)
	}

	// Validation floor.
	if rec := request(t, h, http.MethodPost, "/v1/authorities", "lib-token", "", map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty create = %d", rec.Code)
	}

	// Read.
	rec = request(t, h, http.MethodGet, "/v1/authorities/"+created.ID, "lib-token", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Cozy fantasy") {
		t.Fatalf("get = %d %s", rec.Code, rec.Body.String())
	}

	// The live index serves both listing and label search immediately.
	rec = request(t, h, http.MethodGet, "/v1/authorities?q=cozy", "lib-token", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), created.URI) {
		t.Fatalf("search = %d %s", rec.Code, rec.Body.String())
	}
	rec = request(t, h, http.MethodGet, "/v1/authorities", "lib-token", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), created.URI) {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}

	// Update needs the token; a stale one gets 412 with fresh state.
	update := map[string]any{"prefLabel": map[string]string{"en": "Cozy fantasy (genre)"}}
	if rec := request(t, h, http.MethodPut, "/v1/authorities/"+created.ID, "lib-token", "", update); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no-token update = %d", rec.Code)
	}
	rec = request(t, h, http.MethodPut, "/v1/authorities/"+created.ID, "lib-token", created.ETag, update)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d %s", rec.Code, rec.Body.String())
	}
	rec = request(t, h, http.MethodPut, "/v1/authorities/"+created.ID, "lib-token", created.ETag, update)
	if rec.Code != http.StatusPreconditionFailed || !strings.Contains(rec.Body.String(), "Cozy fantasy (genre)") {
		t.Fatalf("stale update = %d %s", rec.Code, rec.Body.String())
	}

	if rec := request(t, h, http.MethodGet, "/v1/authorities/amissing000001", "lib-token", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing get = %d", rec.Code)
	}
}

func TestAuthorityMergeEndpoint(t *testing.T) {
	h, bs, _ := newAuthoritiesAPI(t)
	rec := request(t, h, http.MethodPost, "/v1/authorities", "lib-token", "", map[string]any{
		"prefLabel": map[string]string{"en": "Trans folks"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d", rec.Code)
	}
	var created struct{ ID, URI string }
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	seedTypedWork(t, bs, "wcarrier00001", nil, created.URI)

	rec = request(t, h, http.MethodPost, "/v1/authorities/merge", "lib-token", "", map[string]any{
		"loser":  created.ID,
		"winner": map[string]string{"scheme": "homosaurus", "id": "https://homosaurus.org/v4/homoit0001235"},
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"rewritten":1`) {
		t.Fatalf("merge = %d %s", rec.Code, rec.Body.String())
	}
	grain, _, err := bs.Get(t.Context(), bibframe.GrainPath("wcarrier00001"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(grain), created.URI) || !strings.Contains(string(grain), "homoit0001235") {
		t.Fatalf("carrier after merge:\n%s", grain)
	}
	// Retired terms drop out of search.
	rec = request(t, h, http.MethodGet, "/v1/authorities?q=trans+folks", "lib-token", "", nil)
	if strings.Contains(rec.Body.String(), created.URI) {
		t.Fatalf("retired term searchable: %s", rec.Body.String())
	}

	if rec := request(t, h, http.MethodPost, "/v1/authorities/merge", "lib-token", "", map[string]any{
		"loser": "amissing000001", "winner": map[string]string{"scheme": "homosaurus", "id": "https://homosaurus.org/v4/homoit0001235"},
	}); rec.Code != http.StatusNotFound {
		t.Fatalf("missing merge = %d", rec.Code)
	}
}

// TestRecordSaveAutoLinks proves the write path hands saved Works to the
// auto-linker: a record edit adding an uncontrolled tag that names an
// authority heading lands a PIPELINE suggestion in the queue.
func TestRecordSaveAutoLinks(t *testing.T) {
	h, bs, queue := newAuthoritiesAPI(t)
	const workID = "wautolink0001"
	seedTypedWork(t, bs, workID, []string{}, "")
	_, etag, err := bs.Get(t.Context(), bibframe.GrainPath(workID))
	if err != nil {
		t.Fatal(err)
	}
	patch := editor.Patch{Add: []editor.Statement{{
		S: bibframe.WorkIRI(workID),
		P: bibframe.PredTag,
		O: editor.Term{Kind: "literal", Value: "transgender people"},
	}}}
	rec := request(t, h, http.MethodPut, "/v1/works/"+workID, "lib-token", etag, patch)
	if rec.Code != http.StatusOK {
		t.Fatalf("record edit = %d %s", rec.Code, rec.Body.String())
	}
	items, err := queue.ForWork(t.Context(), workID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Provenance != suggest.ProvenancePipeline ||
		items[0].Term.ID != "https://homosaurus.org/v4/homoit0001235" {
		t.Fatalf("queue = %+v", items)
	}
}

// seedTypedWork writes a typed Work grain (rdf:type bf:Work, required by the
// summarizer) with optional feed tags and an optional editorial subject.
func seedTypedWork(t *testing.T, bs blob.Store, workID string, tags []string, subjectURI string) {
	t.Helper()
	const bfNS = "http://id.loc.gov/ontologies/bibframe/"
	ds := &rdf.Dataset{}
	feed := bibframe.FeedGraph("overdrive")
	work := rdf.NewIRI(bibframe.WorkIRI(workID))
	ds.Add(work, rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"), rdf.NewIRI(bfNS+"Work"), feed)
	title := rdf.NewBlank("t0")
	ds.Add(work, rdf.NewIRI(bfNS+"title"), title, feed)
	ds.Add(title, rdf.NewIRI(bfNS+"mainTitle"), rdf.NewLiteral("A Book", "", ""), feed)
	for i, tag := range tags {
		node := rdf.NewBlank("s" + string(rune('0'+i)))
		ds.Add(work, rdf.NewIRI(bfNS+"subject"), node, feed)
		ds.Add(node, rdf.NewIRI("http://www.w3.org/2000/01/rdf-schema#label"), rdf.NewLiteral(tag, "", ""), feed)
	}
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if subjectURI != "" {
		nq, err = bibframe.AppendAuthoritySubject(nq, workID, bibframe.AuthoritySubject{
			URI: subjectURI, Labels: map[string]string{"en": "Old Heading"},
		}, authoritiesvc.LocalScheme)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(workID), nq, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
}
