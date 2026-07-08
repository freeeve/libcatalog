package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"
)

func TestBulkAddItems(t *testing.T) {
	h, bs := newRecordsAPI(t)
	ctx := t.Context()
	workID := "wbulkadd123"
	instanceID := workID + "i"
	if _, err := bs.Put(ctx, bibframe.GrainPath(workID),
		identityGrain(workID, "A Wizard of Earthsea", "Le Guin, Ursula K.", "9780547773742"), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	type item struct {
		CallNumber string `json:"callNumber"`
		Location   string `json:"location"`
		Barcode    string `json:"barcode"`
	}
	var preview struct {
		Items []item `json:"items"`
	}

	// Preview: barcodes generate without writing.
	rec := request(t, h, "POST", "/v1/works/"+workID+"/items/bulk", "lib-token", "", map[string]any{
		"instanceId": instanceID, "count": 3, "barcodePrefix": "B-", "callNumber": "FIC LEG", "dryRun": true,
	})
	if rec.Code != 200 {
		t.Fatalf("dry run: %d %s", rec.Code, rec.Body)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &preview)
	if len(preview.Items) != 3 || preview.Items[0].Barcode != "B-0001" || preview.Items[2].Barcode != "B-0003" {
		t.Fatalf("preview = %+v", preview.Items)
	}
	grain, _, _ := bs.Get(ctx, bibframe.GrainPath(workID))
	if items, _ := bibframe.ItemsOf(grain, instanceID); len(items) != 0 {
		t.Fatalf("dry run wrote items: %+v", items)
	}

	// Real add: 12 copies, sequential, persisted.
	rec = request(t, h, "POST", "/v1/works/"+workID+"/items/bulk", "lib-token", "", map[string]any{
		"instanceId": instanceID, "count": 12, "barcodePrefix": "B-", "callNumber": "FIC LEG", "location": "Main",
	})
	if rec.Code != 200 {
		t.Fatalf("bulk add: %d %s", rec.Code, rec.Body)
	}
	grain, _, _ = bs.Get(ctx, bibframe.GrainPath(workID))
	items, err := bibframe.ItemsOf(grain, instanceID)
	if err != nil || len(items) != 12 {
		t.Fatalf("items = %d, %v", len(items), err)
	}
	seen := map[string]bool{}
	for _, it := range items {
		if seen[it.Barcode] {
			t.Fatalf("duplicate barcode %s", it.Barcode)
		}
		seen[it.Barcode] = true
		if it.CallNumber != "FIC LEG" || it.Location != "Main" {
			t.Fatalf("item fields lost: %+v", it)
		}
	}
	if !seen["B-0001"] || !seen["B-0012"] {
		t.Fatalf("barcodes not sequential: %v", seen)
	}

	// A second bulk add continues past the existing numbering.
	rec = request(t, h, "POST", "/v1/works/"+workID+"/items/bulk", "lib-token", "", map[string]any{
		"instanceId": instanceID, "count": 2, "barcodePrefix": "B-", "dryRun": true,
	})
	if rec.Code != 200 {
		t.Fatalf("second preview: %d %s", rec.Code, rec.Body)
	}
	preview.Items = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &preview)
	if len(preview.Items) != 2 || preview.Items[0].Barcode != "B-0013" {
		t.Fatalf("continuation = %+v", preview.Items)
	}

	// Validation: count bounds and required prefix.
	for _, body := range []map[string]any{
		{"instanceId": instanceID, "count": 0, "barcodePrefix": "B-"},
		{"instanceId": instanceID, "count": 101, "barcodePrefix": "B-"},
		{"instanceId": instanceID, "count": 2},
	} {
		if rec := request(t, h, "POST", "/v1/works/"+workID+"/items/bulk", "lib-token", "", body); rec.Code != 400 {
			t.Fatalf("want 400 for %+v, got %d", body, rec.Code)
		}
	}
}
