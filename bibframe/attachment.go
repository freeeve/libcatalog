package bibframe

import (
	"fmt"
	"regexp"
	"sort"

	"github.com/freeeve/libcodex/rdf"
)

// PredAttachment carries one staff attachment's filename as a plain literal
// on the Work (tasks/229, 058 item 2): scans, correspondence, acquisition
// paperwork -- cataloging working material, stored in the blob store beside
// the grain and NEVER projected to the public catalog. Being an lcat:
// statement it survives re-ingest like every curation decision, and clones
// drop it with the rest of the lcat markers (the bytes stay with the
// source).
const PredAttachment = LcatNS + "attachment"

// attachmentName is the safe-filename shape: leading alphanumeric (which
// excludes dotfiles and ".."), then alphanumerics, dot, underscore, hyphen,
// bounded well under filesystem limits.
var attachmentName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// ValidAttachmentName reports whether name is safe to use as a blob path
// segment and statement value.
func ValidAttachmentName(name string) bool {
	return attachmentName.MatchString(name)
}

// AttachmentBlobPath is where an attachment's bytes live, sharded like
// grains and covers.
func AttachmentBlobPath(workID, name string) string {
	return "data/attachments/" + workID[:min(2, len(workID))] + "/" + workID + "/" + name
}

// AttachmentsOf lists the work's editorial attachment filenames, sorted.
func AttachmentsOf(grainNQ []byte, workID string) ([]string, error) {
	ds, err := rdf.ParseNQuads(grainNQ)
	if err != nil {
		return nil, err
	}
	work, ed := WorkIRI(workID), EditorialGraph()
	var out []string
	for i := range ds.Quads {
		q := &ds.Quads[i]
		if q.G == ed && q.S.IsIRI() && q.S.Value == work && q.P.Value == PredAttachment && q.O.IsLiteral() {
			out = append(out, q.O.Value)
		}
	}
	sort.Strings(out)
	return out, nil
}

// SetAttachment records (add=true) or retracts (add=false) one attachment
// statement, guarded on the grain describing the work (the 202/211/214
// invariant: a typo'd id must not assert statements into a foreign grain)
// and on the filename shape. Adds are idempotent through canonicalization.
func SetAttachment(grainNQ []byte, workID, name string, add bool) ([]byte, error) {
	if !ValidAttachmentName(name) {
		return nil, fmt.Errorf("bibframe: bad attachment name %q", name)
	}
	if !grainDescribesWork(grainNQ, workID) {
		return nil, fmt.Errorf("bibframe: grain does not describe work %s", workID)
	}
	q := rdf.Quad{S: rdf.NewIRI(WorkIRI(workID)), P: rdf.NewIRI(PredAttachment), O: rdf.NewLiteral(name, "", "")}
	patch := Patch{Add: []rdf.Quad{q}}
	if !add {
		patch = Patch{Remove: []rdf.Quad{q}}
	}
	return ApplyEditorialPatch(grainNQ, patch)
}
