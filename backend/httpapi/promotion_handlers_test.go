package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/publish"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/vocab"
)

// newPromotionAPI wires the full stack over mem stores: vocab fixture,
// suggestion service, real publisher, and a grain carrying a folk tag.
func newPromotionAPI(t *testing.T) (http.Handler, blob.Store) {
	t.Helper()
	data, err := os.ReadFile("../vocab/testdata/authorities.nq")
	if err != nil {
		t.Fatal(err)
	}
	authorities := blob.NewMem()
	_, _ = authorities.Put(t.Context(), "a/x.nq", data, blob.PutOptions{})
	ix, err := vocab.Load(t.Context(), authorities, "a/", nil)
	if err != nil {
		t.Fatal(err)
	}
	db := store.NewMem()
	svc := suggest.New(db, ix, suggest.Caps{})
	grains := blob.NewMem()

	// One grain: a Work with an editorial folk tag.
	const bfNS = "http://id.loc.gov/ontologies/bibframe/"
	ds := &rdf.Dataset{}
	feed := bibframe.FeedGraph("overdrive")
	work := rdf.NewIRI(bibframe.WorkIRI("wpromo0000001"))
	ds.Add(work, rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"), rdf.NewIRI(bfNS+"Work"), feed)
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	nq, err = bibframe.ApplyEditorialPatch(nq, bibframe.Patch{Add: []rdf.Quad{
		bibframe.TagQuad("wpromo0000001", "queer joy"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := grains.Put(t.Context(), bibframe.GrainPath("wpromo0000001"), nq, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}

	verifier := staffVerifier{
		"mod-token": {Email: "mod@example.org", Roles: []auth.Role{auth.RoleModerator}},
		"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}},
	}
	pub := &publish.Publisher{Blob: grains, Queue: svc, Vocab: ix}
	h := New(Deps{Suggest: svc, Vocab: ix, Verifier: verifier, Publisher: pub, Blob: grains, DB: db})
	return h, grains
}

func TestTermLookupAndTags(t *testing.T) {
	h, _ := newPromotionAPI(t)
	rec := doJSON(t, h, http.MethodGet, "/v1/term?scheme=homosaurus&id="+transURI, "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Transgender people") {
		t.Fatalf("term lookup: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, h, http.MethodGet, "/v1/term?scheme=homosaurus&id=https://nope", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown term: %d", rec.Code)
	}
	// Tags typeahead is staff-gated and counts carriers.
	if rec := doJSON(t, h, http.MethodGet, "/v1/tags?q=queer", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous tags: %d", rec.Code)
	}
	rec = doJSON(t, h, http.MethodGet, "/v1/tags?q=queer", "mod-token", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("tags: %d %s", rec.Code, rec.Body)
	}
	var tags struct {
		Tags []struct {
			Tag   string `json:"tag"`
			Count int    `json:"count"`
		} `json:"tags"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &tags)
	if len(tags.Tags) != 1 || tags.Tags[0].Tag != "queer joy" || tags.Tags[0].Count != 1 {
		t.Fatalf("tags = %+v", tags.Tags)
	}
}

func TestPromotionRoutes(t *testing.T) {
	h, grains := newPromotionAPI(t)

	propose := map[string]any{
		"tag":  "Queer Joy",
		"term": map[string]string{"scheme": "homosaurus", "id": transURI},
	}
	// Anonymous and unknown-term proposals rejected.
	if rec := doJSON(t, h, http.MethodPost, "/v1/promotions", "", propose); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous propose: %d", rec.Code)
	}
	bad := map[string]any{"tag": "x y", "term": map[string]string{"scheme": "homosaurus", "id": "https://nope"}}
	if rec := doJSON(t, h, http.MethodPost, "/v1/promotions", "mod-token", bad); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown term propose: %d", rec.Code)
	}
	rec := doJSON(t, h, http.MethodPost, "/v1/promotions", "mod-token", propose)
	if rec.Code != http.StatusCreated {
		t.Fatalf("propose: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, h, http.MethodPost, "/v1/promotions", "mod-token", propose); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate propose: %d", rec.Code)
	}

	// Listing shows the pending proposal.
	rec = doJSON(t, h, http.MethodGet, "/v1/promotions?status=PENDING", "mod-token", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "queer joy") {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}

	// Moderators cannot decide; librarians can, and approval executes.
	decide := map[string]any{"tag": "queer joy", "approve": true}
	if rec := doJSON(t, h, http.MethodPost, "/v1/promotions/decide", "mod-token", decide); rec.Code != http.StatusForbidden {
		t.Fatalf("moderator decide: %d", rec.Code)
	}
	rec = doJSON(t, h, http.MethodPost, "/v1/promotions/decide", "lib-token", decide)
	if rec.Code != http.StatusOK {
		t.Fatalf("decide: %d %s", rec.Code, rec.Body)
	}
	var resp struct {
		Works int `json:"works"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Works != 1 {
		t.Fatalf("works = %d, want 1", resp.Works)
	}
	// The grain was rewritten: subject in, editorial tag out, alias recorded.
	g, _, _ := grains.Get(t.Context(), bibframe.GrainPath("wpromo0000001"))
	if !strings.Contains(string(g), transURI) || strings.Contains(string(g), bibframe.PredTag+">") {
		t.Fatalf("grain after promotion:\n%s", g)
	}
	aliases, _, err := grains.Get(t.Context(), "data/authorities/al/aliases.nq")
	if err != nil || !strings.Contains(string(aliases), "queer joy") {
		t.Fatalf("alias grain: %v\n%s", err, aliases)
	}
	// Re-deciding conflicts.
	if rec := doJSON(t, h, http.MethodPost, "/v1/promotions/decide", "lib-token", decide); rec.Code != http.StatusConflict {
		t.Fatalf("re-decide: %d", rec.Code)
	}
}
