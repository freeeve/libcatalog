package httpapi

import (
	"archive/zip"
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strings"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/workindex"
)

// coverBatchMaxBytes bounds the whole uploaded zip; entries stay under the
// single-cover cap.
const coverBatchMaxBytes = 64 << 20

// coverBatchResult reports one zip entry's fate.
//
// Skipped and Failed are not the same thing, and folding them together made the
// batch report lie (tasks/268). Skipped means the entry was rejected before
// either store was touched: nothing happened, and the operator can ignore it or
// fix the name. Failed means the stores were asked to do the work and did not:
// the entry is worth retrying. Changed marks the one case an operator has to
// repair by hand -- the cover statement was written and could not be undone, so
// the record claims an image whose bytes are not stored.
type coverBatchResult struct {
	File    string `json:"file"`
	WorkID  string `json:"workId,omitempty"`
	Cover   string `json:"cover,omitempty"`
	Skipped string `json:"skipped,omitempty"`
	Failed  string `json:"failed,omitempty"`
	Changed bool   `json:"changed,omitempty"`
}

// registerCoverBatch mounts the zip batch cover upload (tasks/220, 058 item
// 2 remainder): each entry is <workId>.<ext> or <isbn>.<ext>; ISBNs resolve
// through the work index. Every applied cover goes through the same
// grain-first SetCover path as the single PUT, so a bad name never strands
// bytes -- and, since tasks/268, a failed byte write never strands a statement:
// the danger of grain-first is the statement, not the bytes.
//
// The response counts applied, skipped and failed separately. A batch report is
// the only account of the run a librarian ever sees, so an entry that changed a
// record must never be filed under a word that promises nothing happened.
func registerCoverBatch(mux *http.ServeMux, bs blob.Store, ix *workindex.Index, queue *suggest.Service, verifier auth.TokenVerifier, logger *slog.Logger) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	mux.Handle("POST /v1/covers/batch", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, coverBatchMaxBytes))
		if err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "zip too large (64MB cap)")
			return
		}
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			writeError(w, http.StatusBadRequest, "not a zip archive")
			return
		}
		byISBN, err := isbnIndex(r, ix)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "work index unavailable")
			return
		}
		var results []coverBatchResult
		applied, skipped, failed := 0, 0, 0
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			name := path.Base(f.Name)
			if strings.HasPrefix(name, ".") || strings.HasPrefix(f.Name, "__MACOSX/") {
				continue
			}
			res := applyBatchCover(r, bs, ix, byISBN, f, name)
			switch {
			case res.Failed != "":
				failed++
				logger.Error("batch cover entry failed", "file", res.File, "workId", res.WorkID,
					"actor", id.Email, "err", res.Failed, "recordChanged", res.Changed)
			case res.Skipped != "":
				skipped++
			default:
				applied++
			}
			// Audit every entry that left a cover statement on a record -- the
			// applied ones, and the one whose statement could not be rolled
			// back. Otherwise the single work that needs repair by hand is the
			// single work nothing in the audit log names (tasks/268). A
			// compensated entry changed nothing and is not audited, as in 249.
			if queue != nil && res.Cover != "" {
				note := res.Cover + " (batch)"
				if res.Changed {
					note = res.Cover + " (batch; bytes were not stored)"
				}
				queue.WriteAudit(r.Context(), suggest.AuditEntry{
					WorkID: res.WorkID, Action: "COVER_SET", Actor: id.Email, Note: note,
				})
			}
			results = append(results, res)
		}
		// A failed entry makes this a partial success, and a 200 would say the
		// whole batch did what it was asked. 207 says some entries did not.
		status := http.StatusOK
		if failed > 0 {
			status = http.StatusMultiStatus
		}
		writeJSON(w, status, map[string]any{
			"applied": applied, "skipped": skipped, "failed": failed, "results": results,
		})
	})))
}

// isbnIndex maps each normalized ISBN to its work id; ISBNs claimed by more
// than one work map to "" so the entry skips as ambiguous instead of
// guessing.
func isbnIndex(r *http.Request, ix *workindex.Index) (map[string]string, error) {
	summaries, err := ix.Summaries(r.Context())
	if err != nil {
		return nil, err
	}
	byISBN := map[string]string{}
	for _, s := range summaries {
		for _, raw := range s.ISBNs {
			isbn := normalizeISBN(raw)
			if isbn == "" {
				continue
			}
			if prev, ok := byISBN[isbn]; ok && prev != s.WorkID {
				byISBN[isbn] = ""
				continue
			}
			byISBN[isbn] = s.WorkID
		}
	}
	return byISBN, nil
}

// applyBatchCover resolves one zip entry to a work and applies its cover.
func applyBatchCover(r *http.Request, bs blob.Store, ix *workindex.Index, byISBN map[string]string, f *zip.File, name string) coverBatchResult {
	res := coverBatchResult{File: f.Name}
	dot := strings.LastIndexByte(name, '.')
	if dot <= 0 {
		res.Skipped = "no extension"
		return res
	}
	stem, ext := name[:dot], strings.ToLower(name[dot+1:])
	if ext == "jpeg" {
		ext = "jpg"
	}
	ct := ""
	for typ, e := range coverTypes {
		if e == ext {
			ct = typ
		}
	}
	if ct == "" {
		res.Skipped = "not jpg/png/webp"
		return res
	}
	workID := stem
	if !workIDPattern.MatchString(stem) {
		var ok bool
		workID, ok = byISBN[normalizeISBN(stem)]
		if !ok {
			res.Skipped = "not a work id or known isbn"
			return res
		}
		if workID == "" {
			res.Skipped = "isbn matches multiple works"
			return res
		}
	}
	res.WorkID = workID
	if f.UncompressedSize64 > coverMaxBytes {
		res.Skipped = "image too large (2MB cap)"
		return res
	}
	rc, err := f.Open()
	if err != nil {
		res.Skipped = "unreadable entry"
		return res
	}
	img, err := io.ReadAll(io.LimitReader(rc, coverMaxBytes+1))
	rc.Close()
	if err != nil || len(img) == 0 || len(img) > coverMaxBytes {
		res.Skipped = "unreadable entry"
		return res
	}
	// The zip entry's name claimed the format; the bytes have to agree.
	switch sniffed := sniffCover(img); {
	case sniffed == "":
		res.Skipped = "not a jpeg, png, or webp image"
		return res
	case coverTypes[sniffed] != ext:
		res.Skipped = "image is " + sniffed + ", not the ." + ext + " its name claims"
		return res
	}
	url := "covers/" + workID + "." + ext
	// Grain first, as the single PUT does. Every skip above returns before
	// either store is touched; from here on a failure has already written
	// something, so it is compensated and reported as failed, not skipped
	// (tasks/268). previous is the cover this entry replaces.
	var previous string
	if _, err := mutateWorkGrain(r, bs, ix, workID, func(g []byte) ([]byte, error) {
		cur, err := bibframe.CoverOf(g, workID)
		if err != nil {
			return nil, err
		}
		previous = cur
		return bibframe.SetCover(g, workID, url)
	}); err != nil {
		res.Skipped = mutateSkipReason(err)
		return res
	}
	if _, err := bs.Put(r.Context(), bibframe.CoverBlobPath(workID, ext), img, blob.PutOptions{}); err != nil {
		res.Failed = "cover store failed"
		// Restore the cover this entry replaced, not "": the previous cover's
		// bytes are still stored and still serving publicly.
		if rerr := restoreCover(r, bs, ix, workID, previous); rerr != nil {
			res.Cover, res.Changed = url, true
			res.Failed = "cover store failed, and the record could not be rolled back: it claims " + url
		}
		return res
	}
	res.Cover = url
	if err := sweepStaleCovers(r, bs, workID, ext); err != nil {
		// The cover applied and the record is right. What survives is the blob
		// it replaced, still serving from its own public URL, so the entry is
		// not a clean success -- but it is not a phantom either.
		res.Failed = "the cover applied, but the one it replaced could not be removed and is still being served"
	}
	return res
}

// mutateSkipReason folds a grain-mutation error into a per-entry skip note.
func mutateSkipReason(err error) string {
	if err == errWorkNotFound {
		return "no such work"
	}
	return err.Error()
}

// normalizeISBN strips separators and uppercases the check digit so zip
// entry names match the index's scanned identifier values regardless of
// hyphenation.
func normalizeISBN(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == 'x' || r == 'X':
			b.WriteRune('X')
		case r == '-' || r == ' ':
		default:
			return ""
		}
	}
	if n := b.Len(); n != 10 && n != 13 {
		return ""
	}
	return b.String()
}
