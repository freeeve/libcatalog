package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/freeeve/libcatalog/bibframe"
	"github.com/freeeve/libcatalog/project"
	"github.com/freeeve/libcatalog/storage/blob"

	"github.com/freeeve/libcatalog/backend/auth"
	"github.com/freeeve/libcatalog/backend/store"
)

func newMaintenanceAPI(t *testing.T) (http.Handler, blob.Store) {
	t.Helper()
	bs := blob.NewMem()
	seedTypedWork(t, bs, "wvis00000001", nil, "")
	verifier := staffVerifier{"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}}}
	return New(Deps{Blob: bs, DB: store.NewMem(), Verifier: verifier}), bs
}

func TestVisibilityFlow(t *testing.T) {
	h, bs := newMaintenanceAPI(t)
	const workID = "wvis00000001"

	// Suppress hides from projection without a redirect.
	rec := request(t, h, http.MethodPost, "/v1/works/"+workID+"/visibility", "lib-token", "",
		map[string]string{"action": "suppress"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"suppressed":true`) {
		t.Fatalf("suppress = %d %s", rec.Code, rec.Body.String())
	}
	grain, _, _ := bs.Get(t.Context(), bibframe.GrainPath(workID))
	cat, err := project.Project(grain, "overdrive")
	if err != nil || len(cat.Works) != 0 {
		t.Fatalf("suppressed still projects: %v works, %v", len(cat.Works), err)
	}
	rm, _ := project.Redirects(grain)
	if len(rm.Redirects) != 0 {
		t.Fatalf("suppress redirected: %+v", rm.Redirects)
	}

	// Unsuppress restores; tombstone with a successor leaves a redirect.
	request(t, h, http.MethodPost, "/v1/works/"+workID+"/visibility", "lib-token", "",
		map[string]string{"action": "unsuppress"})
	rec = request(t, h, http.MethodPost, "/v1/works/"+workID+"/visibility", "lib-token", "",
		map[string]string{"action": "tombstone", "redirectTo": "wsucc0000001"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"tombstoned":true`) {
		t.Fatalf("tombstone = %d %s", rec.Code, rec.Body.String())
	}
	grain, _, _ = bs.Get(t.Context(), bibframe.GrainPath(workID))
	cat, _ = project.Project(grain, "overdrive")
	if len(cat.Works) != 0 {
		t.Fatal("tombstoned still projects")
	}
	rm, _ = project.Redirects(grain)
	if len(rm.Redirects) != 1 || rm.Redirects[0].From != workID || rm.Redirects[0].To != "wsucc0000001" {
		t.Fatalf("redirects = %+v", rm.Redirects)
	}

	// State reads back; untombstone restores.
	rec = request(t, h, http.MethodGet, "/v1/works/"+workID+"/visibility", "lib-token", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"redirectTo":"wsucc0000001"`) {
		t.Fatalf("get visibility = %d %s", rec.Code, rec.Body.String())
	}
	request(t, h, http.MethodPost, "/v1/works/"+workID+"/visibility", "lib-token", "",
		map[string]string{"action": "untombstone"})
	grain, _, _ = bs.Get(t.Context(), bibframe.GrainPath(workID))
	cat, _ = project.Project(grain, "overdrive")
	if len(cat.Works) != 1 {
		t.Fatal("untombstone did not restore")
	}
}

// TestItemsRoundTrip is the tasks/051 acceptance: bf:Item fields round-trip
// grain -> editor -> projection.
func TestItemsRoundTrip(t *testing.T) {
	h, bs := newMaintenanceAPI(t)
	const workID = "wvis00000001"

	// The seeded work has no instances; give it one via the grain directly.
	grain, _, _ := bs.Get(t.Context(), bibframe.GrainPath(workID))
	withInst := string(grain) +
		`<#wvis00000001Work> <http://id.loc.gov/ontologies/bibframe/hasInstance> <#ivis00000001Instance> <feed:overdrive> .` + "\n" +
		`<#ivis00000001Instance> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Instance> <feed:overdrive> .` + "\n"
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(workID), []byte(withInst), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}

	rec := request(t, h, http.MethodPut, "/v1/works/"+workID+"/items", "lib-token", "", map[string]any{
		"instanceId": "ivis00000001",
		"items": []map[string]string{
			{"callNumber": "813.6 MUI", "location": "Main - Adult Fiction", "barcode": "300123", "note": "signed copy"},
			{"callNumber": "813.6 MUI c.2", "location": "Branch"},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("put items = %d %s", rec.Code, rec.Body.String())
	}

	// Editor read-back.
	rec = request(t, h, http.MethodGet, "/v1/works/"+workID+"/items", "lib-token", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get items = %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Items map[string][]bibframe.Item `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	items := got.Items["ivis00000001"]
	if len(items) != 2 || items[0].CallNumber != "813.6 MUI" || items[0].Barcode != "300123" || items[1].Location != "Branch" {
		t.Fatalf("items = %+v", items)
	}

	// Projection carries them.
	grain, _, _ = bs.Get(t.Context(), bibframe.GrainPath(workID))
	cat, err := project.Project(grain, "overdrive")
	if err != nil || len(cat.Works) != 1 || len(cat.Works[0].Instances) != 1 {
		t.Fatalf("projection = %+v, %v", cat, err)
	}
	proj := cat.Works[0].Instances[0].Items
	if len(proj) != 2 || proj[0].CallNumber != "813.6 MUI" || proj[0].Note != "signed copy" || proj[1].Location != "Branch" {
		t.Fatalf("projected items = %+v", proj)
	}

	// Replace shrinks the set (no stale item statements).
	rec = request(t, h, http.MethodPut, "/v1/works/"+workID+"/items", "lib-token", "", map[string]any{
		"instanceId": "ivis00000001",
		"items":      []map[string]string{{"callNumber": "813.6 MUI", "location": "Main"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("replace items = %d", rec.Code)
	}
	grain, _, _ = bs.Get(t.Context(), bibframe.GrainPath(workID))
	cat, _ = project.Project(grain, "overdrive")
	if got := cat.Works[0].Instances[0].Items; len(got) != 1 || got[0].Location != "Main" {
		t.Fatalf("after replace = %+v", got)
	}
}

func TestDuplicatesWorklist(t *testing.T) {
	bs := blob.NewMem()
	verifier := staffVerifier{"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}}}
	h := New(Deps{Blob: bs, DB: store.NewMem(), Verifier: verifier})
	// Two works with the same author+title+language clustering key, one
	// bystander.
	for _, id := range []string{"wdupa0000001", "wdupb0000001"} {
		seedTypedWork(t, bs, id, nil, "")
	}
	seedBatchWork(t, bs, "wother000001", "Something Else")

	rec := request(t, h, http.MethodGet, "/v1/duplicates", "lib-token", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("duplicates = %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Groups []struct {
			Key   string `json:"key"`
			Works []struct {
				WorkID string `json:"workId"`
				Title  string `json:"title"`
			} `json:"works"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Groups) != 1 || len(got.Groups[0].Works) != 2 {
		t.Fatalf("groups = %+v", got.Groups)
	}
	ids := got.Groups[0].Works[0].WorkID + got.Groups[0].Works[1].WorkID
	if !strings.Contains(ids, "wdupa0000001") || !strings.Contains(ids, "wdupb0000001") {
		t.Fatalf("group works = %+v", got.Groups[0].Works)
	}
	if got.Groups[0].Works[0].Title != "A Book" {
		t.Fatalf("titles missing: %+v", got.Groups[0].Works)
	}
}
