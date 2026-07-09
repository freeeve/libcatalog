package httpapi

import (
	"archive/zip"
	"bytes"
	"io"
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
type coverBatchResult struct {
	File    string `json:"file"`
	WorkID  string `json:"workId,omitempty"`
	Cover   string `json:"cover,omitempty"`
	Skipped string `json:"skipped,omitempty"`
}

// registerCoverBatch mounts the zip batch cover upload (tasks/220, 058 item
// 2 remainder): each entry is <workId>.<ext> or <isbn>.<ext>; ISBNs resolve
// through the work index. Every applied cover goes through the same
// grain-first SetCover path as the single PUT, so a bad name never strands
// bytes.
func registerCoverBatch(mux *http.ServeMux, bs blob.Store, ix *workindex.Index, queue *suggest.Service, verifier auth.TokenVerifier) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)

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
		applied := 0
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			name := path.Base(f.Name)
			if strings.HasPrefix(name, ".") || strings.HasPrefix(f.Name, "__MACOSX/") {
				continue
			}
			res := applyBatchCover(r, bs, ix, byISBN, f, name)
			if res.Skipped == "" {
				applied++
				if queue != nil {
					queue.WriteAudit(r.Context(), suggest.AuditEntry{
						WorkID: res.WorkID, Action: "COVER_SET", Actor: id.Email, Note: res.Cover + " (batch)",
					})
				}
			}
			results = append(results, res)
		}
		writeJSON(w, http.StatusOK, map[string]any{"applied": applied, "results": results})
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
	url := "covers/" + workID + "." + ext
	if _, err := mutateWorkGrain(r, bs, ix, workID, func(g []byte) ([]byte, error) {
		return bibframe.SetCover(g, workID, url)
	}); err != nil {
		res.Skipped = mutateSkipReason(err)
		return res
	}
	if _, err := bs.Put(r.Context(), bibframe.CoverBlobPath(workID, ext), img, blob.PutOptions{}); err != nil {
		res.Skipped = "cover store failed"
		return res
	}
	res.Cover = url
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
