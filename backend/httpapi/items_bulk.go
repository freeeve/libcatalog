package httpapi

import (
	"encoding/json"
	"fmt"
	"github.com/freeeve/libcat/identity"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/workindex"
)

const (
	bulkAddMax          = 100
	defaultBarcodeWidth = 4
)

// registerItemsBulk mounts bulk item creation (tasks/069): N copies in one
// action with an auto-incrementing, collision-checked barcode pattern.
// dryRun previews the generated list without writing.
func registerItemsBulk(mux *http.ServeMux, bs blob.Store, ix *workindex.Index, queue *suggest.Service, librarian func(http.Handler) http.Handler) {
	mux.Handle("POST /v1/works/{id}/items/bulk", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		workID := r.PathValue("id")
		if !workIDPattern.MatchString(workID) {
			writeError(w, http.StatusBadRequest, "bad work id")
			return
		}
		var req struct {
			InstanceID    string `json:"instanceId"`
			Count         int    `json:"count"`
			CallNumber    string `json:"callNumber"`
			Location      string `json:"location"`
			Note          string `json:"note"`
			BarcodePrefix string `json:"barcodePrefix"`
			BarcodeWidth  int    `json:"barcodeWidth"`
			DryRun        bool   `json:"dryRun"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || req.InstanceID == "" {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		if req.Count < 1 || req.Count > bulkAddMax {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("count must be 1-%d", bulkAddMax))
			return
		}
		if req.BarcodePrefix == "" {
			writeError(w, http.StatusBadRequest, "bulk add needs a barcode prefix")
			return
		}
		width := req.BarcodeWidth
		if width <= 0 {
			width = defaultBarcodeWidth
		}
		if width > 12 {
			writeError(w, http.StatusBadRequest, "barcode width must be at most 12")
			return
		}
		grain, _, _, ok := readWorkGrain(w, r, bs)
		if !ok {
			return
		}
		// The instance must belong to THIS work (tasks/211): an id typo or a
		// copy-paste from another record used to graft holdings onto a
		// phantom IRI no reader enumerates -- consuming real barcodes.
		// Rejected on dryRun too, so the preview cannot promise barcodes an
		// instance cannot receive.
		gi, err := identity.ScanGrain(grain)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "grain parse failed")
			return
		}
		if !slices.ContainsFunc(gi.Instances, func(i identity.InstanceIdentity) bool { return i.InstanceID == req.InstanceID }) {
			writeError(w, http.StatusBadRequest, "no such instance on this work")
			return
		}
		existing, err := bibframe.ItemsOf(grain, req.InstanceID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "grain parse failed")
			return
		}
		if len(existing)+req.Count > 200 {
			writeError(w, http.StatusBadRequest, "at most 200 items per instance")
			return
		}
		taken, err := ix.Barcodes(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "barcode scan failed")
			return
		}
		barcodes := nextBarcodes(taken, req.BarcodePrefix, width, req.Count)
		generated := make([]bibframe.Item, req.Count)
		for i, bc := range barcodes {
			generated[i] = bibframe.Item{
				CallNumber: req.CallNumber, Location: req.Location,
				Note: req.Note, Barcode: bc,
			}
		}
		if req.DryRun {
			writeJSON(w, http.StatusOK, map[string]any{"workId": workID, "items": generated})
			return
		}
		etag, err := mutateWorkGrain(r, bs, ix, workID, func(g []byte) ([]byte, error) {
			current, err := bibframe.ItemsOf(g, req.InstanceID)
			if err != nil {
				return nil, err
			}
			return bibframe.SetItems(g, req.InstanceID, append(current, generated...))
		})
		if err != nil {
			writeMutateError(w, err)
			return
		}
		if queue != nil {
			queue.WriteAudit(r.Context(), suggest.AuditEntry{
				WorkID: workID, Action: "ITEMS_BULK_ADD", Actor: id.Email, ETag: etag,
				Note: fmt.Sprintf("%s: %d items (%s...)", req.InstanceID, req.Count, barcodes[0]),
			})
		}
		w.Header().Set("ETag", etag)
		writeJSON(w, http.StatusOK, map[string]any{"workId": workID, "etag": etag, "items": generated})
	})))
}

// nextBarcodes generates count barcodes prefix+zero-padded counter, starting
// past the highest existing counter for the prefix and skipping collisions.
func nextBarcodes(taken map[string]bool, prefix string, width, count int) []string {
	next := 1
	for bc := range taken {
		suffix, ok := strings.CutPrefix(bc, prefix)
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(suffix); err == nil && n >= next {
			next = n + 1
		}
	}
	out := make([]string, 0, count)
	for len(out) < count {
		bc := fmt.Sprintf("%s%0*d", prefix, width, next)
		next++
		if taken[bc] {
			continue
		}
		taken[bc] = true
		out = append(out, bc)
	}
	return out
}
