package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/identity"
	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/workindex"
)

// registerMaintenance mounts the maintenance surfaces: the
// visibility stance (tombstone with optional redirect, suppress) and the
// duplicate-detection worklist over the shared work index.
func registerMaintenance(mux *http.ServeMux, bs blob.Store, ix *workindex.Index, queue *suggest.Service, verifier auth.TokenVerifier) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)
	registerItemsBulk(mux, bs, ix, queue, librarian)

	mux.Handle("GET /v1/works/{id}/visibility", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		grain, _, workID, ok := readWorkGrain(w, r, bs)
		if !ok {
			return
		}
		v, err := bibframe.Visibility(grain, workID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "grain parse failed")
			return
		}
		writeJSON(w, http.StatusOK, v)
	})))

	mux.Handle("POST /v1/works/{id}/visibility", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		workID := r.PathValue("id")
		if !workIDPattern.MatchString(workID) {
			writeError(w, http.StatusBadRequest, "bad work id")
			return
		}
		var req struct {
			Action     string `json:"action"` // tombstone | untombstone | suppress | unsuppress
			RedirectTo string `json:"redirectTo"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		if req.RedirectTo != "" && !workIDPattern.MatchString(req.RedirectTo) {
			writeError(w, http.StatusBadRequest, "bad redirect target")
			return
		}
		// A tombstone that redirects to the work being tombstoned is a permalink
		// that loops (the successor IS the retired page), so the republished
		// redirect dead-ends in ERR_TOO_MANY_REDIRECTS. Reject it, symmetric to
		// the relations (target != work) and merge (from != to) self-guards.
		if req.RedirectTo == workID {
			writeError(w, http.StatusBadRequest, "a tombstone cannot redirect to itself")
			return
		}
		mutate := map[string]func([]byte) ([]byte, error){
			"tombstone":   func(g []byte) ([]byte, error) { return bibframe.SetTombstone(g, workID, req.RedirectTo) },
			"untombstone": func(g []byte) ([]byte, error) { return bibframe.ClearTombstone(g, workID) },
			"suppress":    func(g []byte) ([]byte, error) { return bibframe.SetSuppressed(g, workID, true) },
			"unsuppress":  func(g []byte) ([]byte, error) { return bibframe.SetSuppressed(g, workID, false) },
		}[req.Action]
		if mutate == nil {
			writeError(w, http.StatusBadRequest, "action must be tombstone|untombstone|suppress|unsuppress")
			return
		}
		etag, err := mutateWorkGrain(r, bs, ix, workID, mutate)
		if err != nil {
			writeMutateError(w, err)
			return
		}
		if queue != nil {
			queue.WriteAudit(r.Context(), suggest.AuditEntry{
				WorkID: workID, Action: "VISIBILITY_" + req.Action, Actor: id.Email, ETag: etag,
				Note: req.RedirectTo,
			})
			// Retiring a work also retires its queue noise: open suggestion
			// rows close with a moderator-grade reject, audit-stamped,
			// instead of pointing at a dead id forever. Best effort -- the
			// tombstone itself already landed; a cleanup failure is
			// recorded in the trail rather than failing the response.
			if req.Action == "tombstone" {
				if _, err := queue.RejectOpenForWork(r.Context(), workID, id.Email, "work tombstoned"); err != nil {
					queue.WriteAudit(r.Context(), suggest.AuditEntry{
						WorkID: workID, Action: "WORK_TOMBSTONE_REJECT_FAILED", Actor: id.Email,
						Note: "open suggestions could not be closed; they remain reviewable",
					})
				}
			}
		}
		grain, _, _ := bs.Get(r.Context(), bibframe.GrainPath(workID))
		v, _ := bibframe.Visibility(grain, workID)
		w.Header().Set("ETag", etag)
		writeJSON(w, http.StatusOK, v)
	})))

	// Holdings: the minimal bf:Item model, read per work and
	// replaced per instance.
	mux.Handle("GET /v1/works/{id}/items", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		grain, etag, workID, ok := readWorkGrain(w, r, bs)
		if !ok {
			return
		}
		gi, err := identity.ScanGrain(grain)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "grain parse failed")
			return
		}
		items := map[string][]bibframe.Item{}
		for _, inst := range gi.Instances {
			list, err := bibframe.ItemsOf(grain, inst.InstanceID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "grain parse failed")
				return
			}
			items[inst.InstanceID] = list
		}
		w.Header().Set("ETag", etag)
		writeJSON(w, http.StatusOK, map[string]any{"workId": workID, "etag": etag, "items": items})
	})))

	// PUT replaces an instance's holdings wholesale under the client's If-Match
	// token, exactly as PUT /v1/works/{id} does. It is a client-token PUT: the
	// panel reads a list, a human edits it, and the list is written back, so the
	// write must be checked against the read it was computed from.
	//
	// mutateWorkGrain is deliberately not used here. Its retry-from-fresh loop
	// exists to carry server-initiated edits past a concurrent write, and it did
	// exactly that -- re-reading the grain and re-applying a list computed
	// against a grain that no longer existed. The second of two catalogers
	// deleted the first one's copy and was told 200. A barcode names
	// one physical copy, so the lost item is a shelf unlinked from the catalog.
	mux.Handle("PUT /v1/works/{id}/items", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		ifMatch, ok := requireIfMatch(w, r)
		if !ok {
			return
		}
		var req struct {
			InstanceID string          `json:"instanceId"`
			Items      []bibframe.Item `json:"items"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || req.InstanceID == "" {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		if len(req.Items) > 200 {
			writeError(w, http.StatusBadRequest, "at most 200 items per instance")
			return
		}
		grain, etag, workID, ok := readWorkGrain(w, r, bs)
		if !ok {
			return
		}
		// An early-out: it saves parsing a list that cannot be written, and it
		// answers from the grain already in hand. It is not what enforces the
		// precondition -- the Put below is, and it is given the *client's* token,
		// not the one read a microsecond ago. Passing the fresh read's etag would
		// make the store's check tautological and leave this comparison as the
		// only guard.
		if etag != ifMatch {
			writeGrainConflict(w, workID, etag, grain)
			return
		}
		updated, err := bibframe.SetItems(grain, req.InstanceID, req.Items)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		// Corpus-wide barcode uniqueness: reject a barcode already
		// held by a live item on a different instance -- a barcode names one
		// physical copy. The within-request duplicate check is SetItems' job
		//; this catches a collision with another record. The instance
		// being edited is excluded, so re-saving its own items is not a
		// self-collision. Best-effort against the index (not a transactional lock),
		// which is the corpus-wide primitive available here.
		for _, it := range req.Items {
			if it.Barcode == "" {
				continue
			}
			held, by, herr := ix.BarcodeHeldByOther(r.Context(), it.Barcode, workID, req.InstanceID)
			if herr != nil {
				writeError(w, http.StatusInternalServerError, "barcode check failed")
				return
			}
			if held {
				writeError(w, http.StatusConflict, fmt.Sprintf("barcode %q is already assigned to another item (work %s)", it.Barcode, by.WorkID))
				return
			}
		}
		path := bibframe.GrainPath(workID)
		newTag, err := bs.Put(r.Context(), path, updated, blob.PutOptions{
			IfMatch: ifMatch, ContentType: "application/n-quads",
		})
		// A writer that lands between the read above and this Put is caught here
		// and nowhere else.
		if errors.Is(err, blob.ErrPreconditionFailed) {
			fresh, freshTag, _, ok := readWorkGrain(w, r, bs)
			if !ok {
				return
			}
			writeGrainConflict(w, workID, freshTag, fresh)
			return
		}
		if err != nil {
			writeGrainWriteError(w, err)
			return
		}
		ix.Apply(path, newTag, updated)
		_ = ix.AppendFeed(r.Context(), path)
		if queue != nil {
			queue.WriteAudit(r.Context(), suggest.AuditEntry{
				WorkID: workID, Action: "ITEMS_EDIT", Actor: id.Email, ETag: newTag,
				Note: req.InstanceID,
			})
		}
		w.Header().Set("ETag", newTag)
		writeJSON(w, http.StatusOK, map[string]string{"workId": workID, "etag": newTag})
	})))

	// The withdrawal review queue: feed-only works the last
	// reconciliation flagged as gone from their feed, awaiting a curator's
	// suppress-or-keep call. Auto-suppressed rows are decided, so they stay
	// out of the queue.
	mux.Handle("GET /v1/withdrawn", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		summaries, err := ix.Summaries(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		queue := []ingest.WorkSummary{}
		for _, s := range summaries {
			if s.Withdrawn != "" && !s.Suppressed {
				queue = append(queue, s)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"works": queue})
	})))

	mux.Handle("POST /v1/works/{id}/withdrawn", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		workID := r.PathValue("id")
		if !workIDPattern.MatchString(workID) {
			writeError(w, http.StatusBadRequest, "bad work id")
			return
		}
		var req struct {
			Action string `json:"action"` // keep | suppress
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		mutate := map[string]func([]byte) ([]byte, error){
			// keep: the withdrawal is overruled -- clear the flag and record
			// the decision so reconciliation never re-flags this work.
			"keep": func(g []byte) ([]byte, error) {
				g, err := bibframe.ClearWithdrawn(g, workID)
				if err != nil {
					return nil, err
				}
				return bibframe.SetFeedKept(g, workID, true)
			},
			// suppress: hide from projection; the flag stays as the reason.
			"suppress": func(g []byte) ([]byte, error) { return bibframe.SetSuppressed(g, workID, true) },
		}[req.Action]
		if mutate == nil {
			writeError(w, http.StatusBadRequest, "action must be keep|suppress")
			return
		}
		etag, err := mutateWorkGrain(r, bs, ix, workID, mutate)
		if err != nil {
			writeMutateError(w, err)
			return
		}
		if queue != nil {
			queue.WriteAudit(r.Context(), suggest.AuditEntry{
				WorkID: workID, Action: "WITHDRAWN_" + req.Action, Actor: id.Email, ETag: etag,
			})
		}
		grain, _, _ := bs.Get(r.Context(), bibframe.GrainPath(workID))
		v, _ := bibframe.Visibility(grain, workID)
		w.Header().Set("ETag", etag)
		writeJSON(w, http.StatusOK, v)
	})))

	// The duplicate-detection worklist: Works sharing a clustering key
	// (author+title+language) that nonetheless hold separate ids -- the
	// candidates the merge tool resolves.
	// The barcode-duplicate report: a barcode names one physical
	// copy, so any held by more than one item is a data-quality defect. This is
	// the report an operator needs before uniqueness can be enforced on writes --
	// a constraint added over existing duplicates would fail writes to records
	// that were fine yesterday.
	mux.Handle("GET /v1/items/duplicate-barcodes", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dups, err := ix.DuplicateBarcodes(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		if dups == nil {
			dups = []workindex.DuplicateBarcode{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"duplicates": dups})
	})))

	mux.Handle("GET /v1/duplicates", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		type dupWork struct {
			WorkID string `json:"workId"`
			Title  string `json:"title,omitempty"`
		}
		type dupGroup struct {
			Key   string    `json:"key"`
			Works []dupWork `json:"works"`
		}
		byKey, err := ix.DuplicateGroups(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		titles := map[string]string{}
		if summaries, err := ix.Summaries(r.Context()); err == nil {
			for _, s := range summaries {
				titles[s.WorkID] = s.Title
			}
		}
		groups := []dupGroup{}
		for key, ids := range byKey {
			g := dupGroup{Key: key}
			for _, id := range ids {
				g.Works = append(g.Works, dupWork{WorkID: id, Title: titles[id]})
			}
			groups = append(groups, g)
		}
		sort.Slice(groups, func(i, j int) bool { return groups[i].Key < groups[j].Key })
		writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
	})))
}
