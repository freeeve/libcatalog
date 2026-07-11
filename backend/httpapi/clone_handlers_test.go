package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/freeeve/libcat/bibframe"
)

// TestCloneWork covers (058 item 4): POST clones a work into a
// fresh suppressed editorial-only grain the index knows about; phantom ids
// and anonymous callers refuse.
func TestCloneWork(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedWorkGrain(t, bs)

	if rec := request(t, h, http.MethodPost, "/v1/works/wzzzz00phantom/clone", "lib-token", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("phantom clone = %d %s", rec.Code, rec.Body)
	}
	if rec := request(t, h, http.MethodPost, "/v1/works/"+editWorkID+"/clone", "", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon clone = %d", rec.Code)
	}

	rec := request(t, h, http.MethodPost, "/v1/works/"+editWorkID+"/clone", "lib-token", "", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("clone = %d %s", rec.Code, rec.Body)
	}
	var res struct{ WorkID, From, ETag string }
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.From != editWorkID || res.WorkID == "" || res.WorkID == editWorkID {
		t.Fatalf("clone response = %+v", res)
	}

	// The clone's grain exists, is editorial-only, and is suppressed.
	grain, _, err := bs.Get(t.Context(), bibframe.GrainPath(res.WorkID))
	if err != nil {
		t.Fatalf("clone grain: %v", err)
	}
	if strings.Contains(string(grain), "feed:") {
		t.Fatalf("clone carries feed statements:\n%s", grain)
	}
	vis, err := bibframe.Visibility(grain, res.WorkID)
	if err != nil || !vis.Suppressed {
		t.Fatalf("clone visibility = %+v, %v", vis, err)
	}

	// The editor can open it immediately (read-your-writes via ix.Apply).
	if rec := request(t, h, http.MethodGet, "/v1/works/"+res.WorkID+"/doc", "lib-token", "", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "A Book") {
		t.Fatalf("clone doc = %d %s", rec.Code, rec.Body)
	}
}
