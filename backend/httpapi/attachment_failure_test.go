package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"
)

var errStorage = errors.New("induced storage failure")

// flakyBlob fails writes to paths under a prefix, leaving every other path --
// notably the grain tree -- working. This is the reporter's chmod on
// data/attachments, in a test: the grain store stays writable, so a failure is
// attributable to the byte write alone.
type flakyBlob struct {
	blob.Store
	failPutPrefix    string
	failDeletePrefix string
}

func (f *flakyBlob) Put(ctx context.Context, path string, data []byte, opts blob.PutOptions) (string, error) {
	if f.failPutPrefix != "" && strings.HasPrefix(path, f.failPutPrefix) {
		return "", errStorage
	}
	return f.Store.Put(ctx, path, data, opts)
}

func (f *flakyBlob) Delete(ctx context.Context, path string) error {
	if f.failDeletePrefix != "" && strings.HasPrefix(path, f.failDeletePrefix) {
		return errStorage
	}
	return f.Store.Delete(ctx, path)
}

func newAttachmentAPI(t *testing.T, bs blob.Store) (http.Handler, *suggest.Service) {
	t.Helper()
	db := store.NewMem()
	verifier := staffVerifier{"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}}}
	queue := suggest.New(db, nil, suggest.Caps{})
	return New(Deps{Blob: bs, DB: db, Verifier: verifier, Suggest: queue}), queue
}

func upload(t *testing.T, h http.Handler, name, body, query string) *httptest.ResponseRecorder {
	t.Helper()
	url := "/v1/works/" + editWorkID + "/attachments?name=" + name + query
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer lib-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func listAttachments(t *testing.T, h http.Handler) []string {
	t.Helper()
	rec := doJSON(t, h, http.MethodGet, "/v1/works/"+editWorkID+"/attachments", "lib-token", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Attachments []string `json:"attachments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Attachments
}

func deleteAttachment(t *testing.T, h http.Handler, name string) *httptest.ResponseRecorder {
	t.Helper()
	return doJSON(t, h, http.MethodDelete, "/v1/works/"+editWorkID+"/attachments/"+name, "lib-token", nil)
}

func auditActions(t *testing.T, queue *suggest.Service) []string {
	t.Helper()
	entries := auditRows(t, queue)
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Action)
	}
	return out
}

// Control: everything works when the store works. Without this the failure
// tests below prove nothing.
func TestAttachmentHappyPath(t *testing.T) {
	bs := blob.NewMem()
	h, queue := newAttachmentAPI(t, bs)
	seedWorkGrain(t, bs)

	if rec := upload(t, h, "scan.txt", "bytes", ""); rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	if got := listAttachments(t, h); len(got) != 1 || got[0] != "scan.txt" {
		t.Fatalf("list = %v", got)
	}
	rec := doJSON(t, h, http.MethodGet, "/v1/works/"+editWorkID+"/attachments/scan.txt", "lib-token", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "bytes" {
		t.Fatalf("download: %d %q", rec.Code, rec.Body)
	}
	if rec := deleteAttachment(t, h, "scan.txt"); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if got := listAttachments(t, h); len(got) != 0 {
		t.Fatalf("after delete, list = %v", got)
	}
	got := auditActions(t, queue)
	seen := map[string]int{}
	for _, a := range got {
		seen[a]++
	}
	if len(got) != 2 || seen["ATTACHMENT_ADD"] != 1 || seen["ATTACHMENT_REMOVE"] != 1 {
		t.Fatalf("audit = %v, want exactly one ATTACHMENT_ADD and one ATTACHMENT_REMOVE", got)
	}
}

// the phantom: an upload whose bytes cannot be stored returned 500
// and left the record claiming an attachment that 404s on download -- and, the
// second time, refused the retry with 409 because the name was already there.
func TestFailedUploadLeavesNoPhantomAttachment(t *testing.T) {
	bs := &flakyBlob{Store: blob.NewMem(), failPutPrefix: "data/attachments/"}
	h, queue := newAttachmentAPI(t, bs)
	seedWorkGrain(t, bs)

	rec := upload(t, h, "phantom.txt", "bytes", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("upload with a failing byte store: %d %s", rec.Code, rec.Body)
	}
	if got := listAttachments(t, h); len(got) != 0 {
		t.Fatalf("the record still claims %v after the bytes failed to store", got)
	}
	rec = doJSON(t, h, http.MethodGet, "/v1/works/"+editWorkID+"/attachments/phantom.txt", "lib-token", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("download of a never-stored attachment: %d", rec.Code)
	}
	// The cataloger can retry: nothing claims the name.
	if rec := upload(t, h, "phantom.txt", "bytes", ""); rec.Code == http.StatusConflict {
		t.Fatal("a retry after a failed upload was refused with 409: the phantom is still in the grain")
	}
	// Nothing happened, so nothing is audited as having happened.
	if got := auditActions(t, queue); len(got) != 0 {
		t.Fatalf("audit = %v, want none: no attachment was ever added", got)
	}
}

// A failed ?replace=true upload must not remove the statement: the previous
// attachment's bytes are still stored, so rolling the statement back would
// delete a working attachment in order to report a failed one.
func TestFailedReplaceKeepsTheExistingAttachment(t *testing.T) {
	mem := blob.NewMem()
	bs := &flakyBlob{Store: mem}
	h, _ := newAttachmentAPI(t, bs)
	seedWorkGrain(t, bs)

	if rec := upload(t, h, "scan.txt", "original", ""); rec.Code != http.StatusCreated {
		t.Fatalf("seed upload: %d %s", rec.Code, rec.Body)
	}
	bs.failPutPrefix = "data/attachments/"

	rec := upload(t, h, "scan.txt", "replacement", "&replace=true")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("failing replace: %d %s", rec.Code, rec.Body)
	}
	if got := listAttachments(t, h); len(got) != 1 || got[0] != "scan.txt" {
		t.Fatalf("a failed replace dropped the existing attachment: list = %v", got)
	}
	bs.failPutPrefix = ""
	rec = doJSON(t, h, http.MethodGet, "/v1/works/"+editWorkID+"/attachments/scan.txt", "lib-token", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "original" {
		t.Fatalf("the original bytes are gone: %d %q", rec.Code, rec.Body)
	}
}

// the mirror: DELETE discarded bs.Delete's error and answered 204,
// so a librarian deleting an attachment for a legal reason was told it worked
// while the bytes stayed on disk.
func TestFailedDeleteReportsAndKeepsTheRecordIntact(t *testing.T) {
	mem := blob.NewMem()
	bs := &flakyBlob{Store: mem}
	h, queue := newAttachmentAPI(t, bs)
	seedWorkGrain(t, bs)

	if rec := upload(t, h, "rights.txt", "sensitive", ""); rec.Code != http.StatusCreated {
		t.Fatalf("seed upload: %d %s", rec.Code, rec.Body)
	}
	bs.failDeletePrefix = "data/attachments/"

	rec := deleteAttachment(t, h, "rights.txt")
	if rec.Code == http.StatusNoContent {
		t.Fatal("a delete that left the bytes on disk answered 204")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("failing delete: %d %s", rec.Code, rec.Body)
	}
	// The record is unchanged, so the attachment is still reachable and a
	// retry is possible: it was not silently unlinked from its bytes.
	if got := listAttachments(t, h); len(got) != 1 || got[0] != "rights.txt" {
		t.Fatalf("the statement was not restored: list = %v", got)
	}
	path, err := bibframe.AttachmentBlobPath(editWorkID, "rights.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := mem.Get(t.Context(), path); err != nil {
		t.Fatalf("the bytes are gone though the delete failed: %v", err)
	}
	// A removal that did not happen is not audited as a removal.
	for _, a := range auditActions(t, queue) {
		if a == "ATTACHMENT_REMOVE" {
			t.Fatal("a failed delete wrote an ATTACHMENT_REMOVE audit entry")
		}
	}
	// And once the store recovers, the delete works.
	bs.failDeletePrefix = ""
	if rec := deleteAttachment(t, h, "rights.txt"); rec.Code != http.StatusNoContent {
		t.Fatalf("delete after recovery: %d %s", rec.Code, rec.Body)
	}
	if got := listAttachments(t, h); len(got) != 0 {
		t.Fatalf("list = %v after a successful delete", got)
	}
}

// A delete of bytes that are already absent is the state the caller asked for.
func TestDeleteToleratesMissingBytes(t *testing.T) {
	bs := blob.NewMem()
	h, _ := newAttachmentAPI(t, bs)
	seedWorkGrain(t, bs)

	if rec := upload(t, h, "scan.txt", "bytes", ""); rec.Code != http.StatusCreated {
		t.Fatalf("seed: %d", rec.Code)
	}
	path, err := bibframe.AttachmentBlobPath(editWorkID, "scan.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := bs.Delete(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	if rec := deleteAttachment(t, h, "scan.txt"); rec.Code != http.StatusNoContent {
		t.Fatalf("delete with the bytes already gone: %d %s", rec.Code, rec.Body)
	}
	if got := listAttachments(t, h); len(got) != 0 {
		t.Fatalf("list = %v", got)
	}
}
