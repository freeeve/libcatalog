package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/freeeve/libcatalog/storage/blob"

	"github.com/freeeve/libcatalog/backend/auth"
	"github.com/freeeve/libcatalog/backend/profiles"
	"github.com/freeeve/libcatalog/backend/profilesvc"
)

func newProfilesAPI(t *testing.T) (http.Handler, *profilesvc.Service) {
	t.Helper()
	prof := profilesvc.New(blob.NewMem(), "", nil)
	if err := prof.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	verifier := staffVerifier{
		"lib-token":   {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}},
		"admin-token": {Email: "admin@example.org", Roles: []auth.Role{auth.RoleAdmin}},
	}
	return New(Deps{Verifier: verifier, Profiles: prof}), prof
}

// editedWorkProfile returns the shipped work-monograph profile with its first
// field relabelled -- a valid override document.
func editedWorkProfile(t *testing.T, label string) *profiles.Profile {
	t.Helper()
	set, err := profiles.LoadDefaults()
	if err != nil {
		t.Fatal(err)
	}
	p := set["work-monograph"]
	p.Fields[0].Label = label
	return p
}

func TestProfileEditFlow(t *testing.T) {
	h, prof := newProfilesAPI(t)

	// The list is librarian-visible and marks nothing overridden yet.
	rec := request(t, h, http.MethodGet, "/v1/profiles", "lib-token", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "work-monograph") {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}

	// GET one reports the shipped default.
	rec = request(t, h, http.MethodGet, "/v1/profiles/work-monograph", "lib-token", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"isDefault":true`) {
		t.Fatalf("get = %d %s", rec.Code, rec.Body.String())
	}

	// A librarian cannot write.
	if rec := request(t, h, http.MethodPut, "/v1/profiles/work-monograph", "lib-token", "", editedWorkProfile(t, "X")); rec.Code != http.StatusForbidden {
		t.Fatalf("librarian put = %d", rec.Code)
	}

	// An invalid profile is rejected before it can persist.
	if rec := request(t, h, http.MethodPut, "/v1/profiles/work-monograph", "admin-token", "", map[string]any{"id": "work-monograph"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid put = %d %s", rec.Code, rec.Body.String())
	}
	if prof.Overridden("work-monograph") {
		t.Fatal("a rejected save must not persist an override")
	}

	// Admin saves a valid override; the live set reflects it.
	rec = request(t, h, http.MethodPut, "/v1/profiles/work-monograph", "admin-token", "", editedWorkProfile(t, "Uniform Title"))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin put = %d %s", rec.Code, rec.Body.String())
	}
	if got := prof.Set()["work-monograph"].Fields[0].Label; got != "Uniform Title" {
		t.Fatalf("override label = %q, want the edit", got)
	}

	// Revert removes the override; a second revert is a 404.
	if rec := request(t, h, http.MethodDelete, "/v1/profiles/work-monograph", "admin-token", "", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	if prof.Overridden("work-monograph") {
		t.Fatal("override still present after revert")
	}
	if rec := request(t, h, http.MethodDelete, "/v1/profiles/work-monograph", "admin-token", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d", rec.Code)
	}
}
