package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/copycat"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/vocabsrc"
	"github.com/freeeve/libcat/storage/blob"
)

// the admin configuration surface -- vocabulary sources and copycat
// targets -- mutated with no audit entry, while USER_CREATE and COVER_SET were
// audited. These are the highest-blast-radius controls (uninstalling a
// vocabulary un-resolves every heading through it; repointing a target changes
// where records are copied from), and they kept no record of who pulled them.
// These tests pin that every such route now writes one, so a future handler
// added without an audit call fails here.

func adminVerifier() staffVerifier {
	return staffVerifier{
		"admin-token": {Email: "admin@example.org", Roles: []auth.Role{auth.RoleAdmin}},
	}
}

// auditByAction indexes a month's audit entries by action for assertions.
func auditByAction(t *testing.T, queue *suggest.Service) map[string]suggest.AuditEntry {
	t.Helper()
	out := map[string]suggest.AuditEntry{}
	for _, e := range auditRows(t, queue) {
		out[e.Action] = e
	}
	return out
}

func TestCopycatTargetChangesAreAudited(t *testing.T) {
	queue := suggest.New(store.NewMem(), nil, suggest.Caps{})
	svc := &copycat.Service{Blob: blob.NewMem(), DB: store.NewMem()}
	h := New(Deps{Blob: svc.Blob, DB: store.NewMem(), Verifier: adminVerifier(), Copycat: svc, Suggest: queue})

	if rec := request(t, h, http.MethodPost, "/v1/copycat/targets", "admin-token", "", map[string]string{
		"name": "zz-loc", "url": "http://lx2.loc.gov:210/LCDB", "protocol": "sru",
	}); rec.Code != http.StatusOK {
		t.Fatalf("target write = %d %s", rec.Code, rec.Body.String())
	}
	if rec := request(t, h, http.MethodDelete, "/v1/copycat/targets/zz-loc", "admin-token", "", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("target delete = %d %s", rec.Code, rec.Body.String())
	}

	byAction := auditByAction(t, queue)
	set, ok := byAction["COPYCAT_TARGET_SET"]
	if !ok {
		t.Fatal("POST /v1/copycat/targets wrote no COPYCAT_TARGET_SET audit entry")
	}
	if set.Actor != "admin@example.org" || !strings.Contains(set.Note, "lx2.loc.gov") || !strings.Contains(set.Note, "(sru)") {
		t.Fatalf("COPYCAT_TARGET_SET = %+v", set)
	}
	if _, ok := byAction["COPYCAT_TARGET_DELETE"]; !ok {
		t.Fatal("DELETE /v1/copycat/targets/{name} wrote no COPYCAT_TARGET_DELETE audit entry")
	}
}

func TestVocabSourceChangesAreAudited(t *testing.T) {
	queue := suggest.New(store.NewMem(), nil, suggest.Caps{})
	vsvc := &vocabsrc.Service{DB: store.NewMem(), Blob: blob.NewMem(), AuthoritiesPrefix: "data/authorities/"}
	h := New(Deps{Verifier: adminVerifier(), VocabSources: vsvc, Suggest: queue})

	// Register a source, install a one-term dump by hand, uninstall it, delete
	// the source -- the four mutating routes the probe found silent.
	if rec := request(t, h, http.MethodPost, "/v1/vocabsources", "admin-token", "", map[string]string{
		"name": "zzaudit", "scheme": "zzaudit",
	}); rec.Code != http.StatusOK {
		t.Fatalf("source create = %d %s", rec.Code, rec.Body.String())
	}

	nt := `<http://example.org/z/1> <http://www.w3.org/2004/02/skos/core#prefLabel> "Z"@en .` + "\n"
	putReq := httptest.NewRequest(http.MethodPut, "/v1/vocabsources/zzaudit/snapshot", strings.NewReader(nt))
	putReq.Header.Set("Authorization", "Bearer admin-token")
	putRec := httptest.NewRecorder()
	h.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("snapshot install = %d %s", putRec.Code, putRec.Body.String())
	}

	if rec := request(t, h, http.MethodDelete, "/v1/vocabsources/zzaudit/snapshot", "admin-token", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("snapshot remove = %d %s", rec.Code, rec.Body.String())
	}
	if rec := request(t, h, http.MethodDelete, "/v1/vocabsources/zzaudit", "admin-token", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("source delete = %d %s", rec.Code, rec.Body.String())
	}

	byAction := auditByAction(t, queue)
	for _, want := range []string{"VOCAB_SOURCE_CREATE", "VOCAB_SNAPSHOT_INSTALL", "VOCAB_SNAPSHOT_REMOVE", "VOCAB_SOURCE_DELETE"} {
		if _, ok := byAction[want]; !ok {
			t.Errorf("missing audit entry: %s", want)
		}
	}
	// Install records the term count that entered the index...
	if e := byAction["VOCAB_SNAPSHOT_INSTALL"]; !strings.Contains(e.Note, "installed 1 terms") || !strings.Contains(e.Note, "upload") {
		t.Errorf("VOCAB_SNAPSHOT_INSTALL note = %q", e.Note)
	}
	// ...and remove records the transition, not just the act.
	if e := byAction["VOCAB_SNAPSHOT_REMOVE"]; !strings.Contains(e.Note, "1 terms -> removed") {
		t.Errorf("VOCAB_SNAPSHOT_REMOVE note = %q (want a term-count transition)", e.Note)
	}
	if e := byAction["VOCAB_SOURCE_CREATE"]; e.Actor != "admin@example.org" {
		t.Errorf("VOCAB_SOURCE_CREATE actor = %q", e.Actor)
	}
}

// evicting a cached live pick is librarian-gated and audited -- a pick
// joins the crosswalk data every cataloger then resolves through.
func TestVocabCacheRemoveIsAudited(t *testing.T) {
	queue := suggest.New(store.NewMem(), nil, suggest.Caps{})
	vsvc := &vocabsrc.Service{DB: store.NewMem(), Blob: blob.NewMem(), AuthoritiesPrefix: "data/authorities/"}
	h := New(Deps{Verifier: adminVerifier(), VocabSources: vsvc, Suggest: queue})

	id := "http://example.org/c/1"
	if rec := request(t, h, http.MethodPost, "/v1/vocabcache", "admin-token", "", map[string]any{
		"scheme": "zzc", "id": id, "label": "One",
	}); rec.Code != http.StatusOK {
		t.Fatalf("cache = %d %s", rec.Code, rec.Body.String())
	}
	if rec := request(t, h, http.MethodDelete, "/v1/vocabcache?scheme=zzc&id="+url.QueryEscape(id), "admin-token", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("uncache = %d %s", rec.Code, rec.Body.String())
	}
	e, ok := auditByAction(t, queue)["VOCAB_CACHE_REMOVE"]
	if !ok {
		t.Fatal("DELETE /v1/vocabcache wrote no VOCAB_CACHE_REMOVE audit entry")
	}
	if e.Actor != "admin@example.org" || !strings.Contains(e.Note, "zzc") {
		t.Fatalf("VOCAB_CACHE_REMOVE = %+v", e)
	}
	// Evicting an absent pick is a 404, not a silent success.
	if rec := request(t, h, http.MethodDelete, "/v1/vocabcache?scheme=zzc&id="+url.QueryEscape(id), "admin-token", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("re-evict = %d, want 404", rec.Code)
	}
}
