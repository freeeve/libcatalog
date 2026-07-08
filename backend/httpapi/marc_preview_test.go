package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/marcview"
	"github.com/freeeve/libcat/backend/store"
)

// TestMARCPreviewAppliesStagedOps drives the tasks/070 live-preview route: a
// staged native work-title edit shows up in the MARC pane (as 240, the
// Work's title in the crosswalk; 245 stays the Instance's transcribed title)
// without anything being written, and an empty op list previews the saved
// state.
func TestMARCPreviewAppliesStagedOps(t *testing.T) {
	bs := blob.NewMem()
	const workID = "wmarc00000002"
	seedMARCGrain(t, bs, workID, "imarc00000002")
	verifier := staffVerifier{"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}}}
	h := New(Deps{Blob: bs, DB: store.NewMem(), Verifier: verifier})

	subfield := func(body []byte, tag string, code string) string {
		t.Helper()
		var view struct {
			Records []marcview.RecordDoc `json:"records"`
		}
		if err := json.Unmarshal(body, &view); err != nil || len(view.Records) == 0 {
			t.Fatalf("preview decode: %v (%d records)", err, len(view.Records))
		}
		for _, f := range view.Records[0].Fields {
			if f.Tag != tag {
				continue
			}
			for _, sf := range f.Subfields {
				if sf.Code == code {
					return sf.Value
				}
			}
		}
		return ""
	}

	// Empty ops preview the saved state.
	rec := request(t, h, http.MethodPost, "/v1/works/"+workID+"/marc/preview", "lib-token", "", map[string]any{"ops": []any{}})
	if rec.Code != http.StatusOK {
		t.Fatalf("saved preview = %d %s", rec.Code, rec.Body)
	}
	saved245 := subfield(rec.Body.Bytes(), "245", "a")
	saved240 := subfield(rec.Body.Bytes(), "240", "a")
	if saved245 == "" {
		t.Fatal("no 245$a in saved preview")
	}

	// A staged work-title op previews as a changed 240 (the Work's title);
	// the Instance's transcribed 245 is untouched.
	rec = request(t, h, http.MethodPost, "/v1/works/"+workID+"/marc/preview", "lib-token", "", map[string]any{
		"ops": []map[string]any{{
			"resource": "work", "path": "title", "action": "set",
			"values": []map[string]any{{"v": "A Previewed Title"}},
		}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("staged preview = %d %s", rec.Code, rec.Body)
	}
	if got := subfield(rec.Body.Bytes(), "240", "a"); got != "A Previewed Title" {
		t.Fatalf("previewed 240$a = %q, want the staged title", got)
	}
	if got := subfield(rec.Body.Bytes(), "245", "a"); got != saved245 {
		t.Fatalf("previewed 245$a = %q, want untouched %q", got, saved245)
	}

	// Nothing was written: the saved MARC still shows the original state.
	rec = request(t, h, http.MethodGet, "/v1/works/"+workID+"/marc", "lib-token", "", nil)
	if got := subfield(rec.Body.Bytes(), "240", "a"); got != saved240 {
		t.Fatalf("preview wrote through: 240$a = %q, want %q", got, saved240)
	}

	// A bad op surfaces as a 400, not a write.
	rec = request(t, h, http.MethodPost, "/v1/works/"+workID+"/marc/preview", "lib-token", "", map[string]any{
		"ops": []map[string]any{{"resource": "work", "path": "no-such-field", "action": "set"}},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad op = %d", rec.Code)
	}
}
