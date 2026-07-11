package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/identity"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/workindex"
)

const (
	bulkAddMax          = 100
	defaultBarcodeWidth = 4

	itemsPerInstanceMax = 200
	itemCapMessage      = "at most 200 items per instance"
)

// errItemCap and errBarcodeScan travel out of the grain-mutation closure, which
// can only return an error, and are mapped back to their status codes.
var (
	errItemCap     = errors.New("item cap")
	errBarcodeScan = errors.New("barcode scan failed")
)

// bulkItems pairs the request's shared item fields with the minted barcodes.
func bulkItems(callNumber, location, note string, barcodes []string) []bibframe.Item {
	out := make([]bibframe.Item, len(barcodes))
	for i, bc := range barcodes {
		out[i] = bibframe.Item{CallNumber: callNumber, Location: location, Note: note, Barcode: bc}
	}
	return out
}

// registerItemsBulk mounts bulk item creation: N copies in one
// action with an auto-incrementing barcode pattern. dryRun previews the
// generated list without writing.
//
// Barcodes are allocated inside the grain-mutation closure, under the index's
// process-wide allocation lock, and checked against the corpus set plus the
// items already on the fresh grain. Before they were chosen once from
// an index snapshot taken before the write and never revisited, so two
// simultaneous adds handed the same barcode to two items -- and across two works
// nothing could even detect it, there being no shared object to compare and
// swap on.
//
// The check is against every barcode the index knows about. It is not a
// uniqueness constraint: nothing rejects a duplicate typed by hand into the item
// editor.
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
		// The instance must belong to THIS work: an id typo or a
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
		if len(existing)+req.Count > itemsPerInstanceMax {
			writeError(w, http.StatusBadRequest, itemCapMessage)
			return
		}
		// The preview allocates without reserving: the barcodes it shows are the
		// ones a commit would pick right now, not a promise. Two catalogers who
		// preview the same prefix simultaneously see the same numbers, and the
		// commits are what arbitrate.
		if req.DryRun {
			taken, err := ix.Barcodes(r.Context())
			if err != nil {
				writeError(w, http.StatusInternalServerError, "barcode scan failed")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"workId": workID,
				"items":  bulkItems(req.CallNumber, req.Location, req.Note, nextBarcodes(taken, req.BarcodePrefix, width, req.Count)),
			})
			return
		}
		// generated is written by the closure below on whichever attempt lands,
		// so the response reports what was stored rather than what was first
		// chosen.
		var generated []bibframe.Item
		var etag string
		err = ix.AllocateBarcodes(func() error {
			var mErr error
			etag, mErr = mutateWorkGrain(r, bs, ix, workID, func(g []byte) ([]byte, error) {
				current, err := bibframe.ItemsOf(g, req.InstanceID)
				if err != nil {
					return nil, err
				}
				// Re-check inside the critical section. A concurrent write that
				// added items would otherwise carry this instance past the cap
				// on the strength of a count read before it landed.
				if len(current)+req.Count > itemsPerInstanceMax {
					return nil, errItemCap
				}
				// Re-allocate against the grain being appended to, on every
				// attempt. A CAS retry re-runs this closure precisely because
				// the grain moved, and barcodes chosen against the superseded
				// one would be laundered into the winner.
				taken, err := ix.Barcodes(r.Context())
				if err != nil {
					return nil, errBarcodeScan
				}
				for _, it := range current {
					if it.Barcode != "" {
						taken[it.Barcode] = true
					}
				}
				generated = bulkItems(req.CallNumber, req.Location, req.Note,
					nextBarcodes(taken, req.BarcodePrefix, width, req.Count))
				return bibframe.SetItems(g, req.InstanceID, append(current, generated...))
			})
			return mErr
		})
		if err != nil {
			if errors.Is(err, errItemCap) {
				writeError(w, http.StatusBadRequest, itemCapMessage)
				return
			}
			if errors.Is(err, errBarcodeScan) {
				writeError(w, http.StatusInternalServerError, "barcode scan failed")
				return
			}
			writeMutateError(w, err)
			return
		}
		if queue != nil {
			queue.WriteAudit(r.Context(), suggest.AuditEntry{
				WorkID: workID, Action: "ITEMS_BULK_ADD", Actor: id.Email, ETag: etag,
				Note: fmt.Sprintf("%s: %d items (%s...)", req.InstanceID, req.Count, generated[0].Barcode),
			})
		}
		w.Header().Set("ETag", etag)
		writeJSON(w, http.StatusOK, map[string]any{"workId": workID, "etag": etag, "items": generated})
	})))
}

// nextBarcodes generates count barcodes prefix+zero-padded counter, starting
// past the highest existing counter for the prefix and skipping collisions.
// taken is mutated: each minted barcode is marked, so one call never repeats
// itself. It must be a snapshot the caller owns.
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
