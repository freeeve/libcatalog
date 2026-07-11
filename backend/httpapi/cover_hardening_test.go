package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"
)

// Minimal byte sequences the net/http sniffer recognizes. They are not decodable
// images, which is the point: the check is "are these the format they claim",
// not "do they render".
var (
	pngBytes  = []byte("\x89PNG\r\n\x1a\nfakebytes")
	jpegBytes = []byte("\xff\xd8\xff\xe0fakebytes")
	htmlBytes = []byte("<html><script>alert(1)</script></html>")
)

func putCover(t *testing.T, h http.Handler, workID string, body []byte, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/v1/works/"+workID+"/cover", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer lib-token")
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func getCover(t *testing.T, h http.Handler, file string, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/covers/"+file, nil)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// finding 1: the old image kept serving from its public,
// unauthenticated, guessable URL after being replaced. Nothing referenced it, so
// nothing would ever collect it -- and a cataloger replaces a cover exactly when
// the old one must stop being published.
func TestReplacingACoverStopsServingTheOldFormat(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedWorkGrain(t, bs)

	if rec := putCover(t, h, editWorkID, jpegBytes, "image/jpeg"); rec.Code != http.StatusOK {
		t.Fatalf("jpeg upload = %d %s", rec.Code, rec.Body)
	}
	if rec := getCover(t, h, editWorkID+".jpg", ""); rec.Code != http.StatusOK {
		t.Fatalf("the jpeg does not serve: %d", rec.Code)
	}
	if rec := putCover(t, h, editWorkID, pngBytes, "image/png"); rec.Code != http.StatusOK {
		t.Fatalf("png replacement = %d %s", rec.Code, rec.Body)
	}

	if rec := getCover(t, h, editWorkID+".jpg", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("the replaced jpeg still serves: %d", rec.Code)
	}
	if rec := getCover(t, h, editWorkID+".png", ""); rec.Code != http.StatusOK {
		t.Fatalf("the new png does not serve: %d", rec.Code)
	}
	// The blob is gone, not merely unreachable through the route.
	if _, _, err := bs.Get(t.Context(), bibframe.CoverBlobPath(editWorkID, "jpg")); err == nil {
		t.Fatal("the replaced jpeg blob is still stored")
	}
	// The grain points at the survivor.
	grain, _, err := bs.Get(t.Context(), bibframe.GrainPath(editWorkID))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(grain, []byte("covers/"+editWorkID+".png")) || bytes.Contains(grain, []byte("covers/"+editWorkID+".jpg")) {
		t.Fatalf("grain does not name exactly the surviving cover:\n%s", grain)
	}
}

// A same-format replacement must keep serving: the sweep skips the extension it
// just wrote. Getting this wrong deletes the cover the upload just stored.
func TestReplacingACoverWithTheSameFormatKeepsIt(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedWorkGrain(t, bs)
	if rec := putCover(t, h, editWorkID, pngBytes, "image/png"); rec.Code != http.StatusOK {
		t.Fatalf("first png = %d", rec.Code)
	}
	second := append(append([]byte{}, pngBytes...), []byte("-v2")...)
	if rec := putCover(t, h, editWorkID, second, "image/png"); rec.Code != http.StatusOK {
		t.Fatalf("second png = %d", rec.Code)
	}
	rec := getCover(t, h, editWorkID+".png", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("the replacement does not serve: %d", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), second) {
		t.Fatal("the server returned the old bytes")
	}
	_ = bs
}

// finding 2: the declared content type was trusted and the bytes never
// looked at, so an HTML document was stored and served as image/png, and a JPEG
// uploaded as image/png was stored at a .png path.
func TestCoverUploadChecksTheBytesAgainstTheDeclaredType(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedWorkGrain(t, bs)

	cases := []struct {
		name        string
		body        []byte
		contentType string
		wantCode    int
	}{
		{"html claiming to be a png", htmlBytes, "image/png", http.StatusBadRequest},
		{"a jpeg claiming to be a png", jpegBytes, "image/png", http.StatusBadRequest},
		{"a png claiming to be a jpeg", pngBytes, "image/jpeg", http.StatusBadRequest},
		{"a real png", pngBytes, "image/png", http.StatusOK},
		{"a real jpeg", jpegBytes, "image/jpeg", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := putCover(t, h, editWorkID, tc.body, tc.contentType)
			if rec.Code != tc.wantCode {
				t.Fatalf("got %d, want %d: %s", rec.Code, tc.wantCode, rec.Body)
			}
		})
	}
	// Nothing was stored for the refused uploads: only the last accepted format
	// survives the sweep, and it is a real image.
	if _, _, err := bs.Get(t.Context(), bibframe.CoverBlobPath(editWorkID, "jpg")); err != nil {
		t.Fatal("the accepted jpeg was not stored")
	}
	data, _, err := bs.Get(t.Context(), bibframe.CoverBlobPath(editWorkID, "png"))
	if err == nil && bytes.Contains(data, []byte("<script>")) {
		t.Fatal("the html body was stored as a png")
	}
}

// finding 3: no validator on a publicly cached response, so a
// same-format correction kept serving the old image from caches for an hour.
func TestCoverResponseIsRevalidatable(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedWorkGrain(t, bs)
	if rec := putCover(t, h, editWorkID, pngBytes, "image/png"); rec.Code != http.StatusOK {
		t.Fatalf("upload = %d", rec.Code)
	}
	rec := getCover(t, h, editWorkID+".png", "")
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the cover response")
	}
	if cc := rec.Header().Get("Cache-Control"); !bytes.Contains([]byte(cc), []byte("must-revalidate")) {
		t.Fatalf("Cache-Control = %q, want it to require revalidation", cc)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type = %q", got)
	}

	// A conditional request on the current etag is a 304.
	if again := getCover(t, h, editWorkID+".png", etag); again.Code != http.StatusNotModified {
		t.Fatalf("conditional GET = %d, want 304", again.Code)
	}
	// After a same-format replacement the etag changes, so the stale
	// conditional request gets the new bytes rather than a 304.
	second := append(append([]byte{}, pngBytes...), []byte("-v2")...)
	if r := putCover(t, h, editWorkID, second, "image/png"); r.Code != http.StatusOK {
		t.Fatalf("replacement = %d", r.Code)
	}
	fresh := getCover(t, h, editWorkID+".png", etag)
	if fresh.Code != http.StatusOK {
		t.Fatalf("stale conditional GET = %d, want 200 with the new bytes", fresh.Code)
	}
	if !bytes.Equal(fresh.Body.Bytes(), second) {
		t.Fatal("the stale validator returned the old bytes")
	}
	if fresh.Header().Get("ETag") == etag {
		t.Fatal("the etag did not change when the bytes did")
	}
	_ = bs
}

// finding 4: RFC 9110 §8.3.1 makes media type and subtype
// case-insensitive, so "Image/PNG" is a correct spelling and was 415'd.
func TestCoverContentTypeIsCaseInsensitive(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedWorkGrain(t, bs)
	for _, ct := range []string{"image/png", "Image/PNG", "IMAGE/PNG", " image/png ", "image/png; charset=binary"} {
		if rec := putCover(t, h, editWorkID, pngBytes, ct); rec.Code != http.StatusOK {
			t.Fatalf("Content-Type %q = %d %s", ct, rec.Code, rec.Body)
		}
	}
	// A genuinely unsupported type still 415s, and says what it got.
	rec := putCover(t, h, editWorkID, pngBytes, "image/gif")
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("image/gif = %d", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("image/gif")) {
		t.Fatalf("the 415 does not say what was sent: %s", rec.Body)
	}
	_ = bs
}

// the editor's Cover panel read the cover out of doc.work.fields,
// where no profile declares it, so every reloaded page reported "none" -- and
// Remove renders only when the panel knows a cover exists. A cataloger facing a
// rights complaint had no control to click.
func TestWorkDocCarriesTheCover(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedWorkGrain(t, bs)

	docCover := func() string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/v1/works/"+editWorkID+"/doc", nil)
		req.Header.Set("Authorization", "Bearer lib-token")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("doc = %d %s", rec.Code, rec.Body)
		}
		var out struct {
			Cover string `json:"cover"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Cover
	}

	if got := docCover(); got != "" {
		t.Fatalf("a work with no cover reports %q", got)
	}
	if rec := putCover(t, h, editWorkID, pngBytes, "image/png"); rec.Code != http.StatusOK {
		t.Fatalf("upload = %d", rec.Code)
	}
	if got, want := docCover(), "covers/"+editWorkID+".png"; got != want {
		t.Fatalf("doc cover = %q, want %q", got, want)
	}
	// And it disappears when the cover is removed, so the panel stops offering
	// Remove for a cover that is gone.
	req := httptest.NewRequest(http.MethodDelete, "/v1/works/"+editWorkID+"/cover", nil)
	req.Header.Set("Authorization", "Bearer lib-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rec.Code)
	}
	if got := docCover(); got != "" {
		t.Fatalf("doc still reports a removed cover: %q", got)
	}
}

// DELETE still clears every stored format (the report's V13, which must not
// regress now that the loop is shared with the PUT sweep).
func TestRemovingACoverClearsEveryFormat(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedWorkGrain(t, bs)
	// Store one of each behind the handler's back, as a pre-fix grain would have.
	for ext, body := range map[string][]byte{"jpg": jpegBytes, "png": pngBytes} {
		if _, err := bs.Put(t.Context(), bibframe.CoverBlobPath(editWorkID, ext), body, blob.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodDelete, "/v1/works/"+editWorkID+"/cover", nil)
	req.Header.Set("Authorization", "Bearer lib-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	for _, ext := range coverExts {
		if _, _, err := bs.Get(t.Context(), bibframe.CoverBlobPath(editWorkID, ext)); err == nil {
			t.Fatalf("%s survived the removal", ext)
		}
	}
}
