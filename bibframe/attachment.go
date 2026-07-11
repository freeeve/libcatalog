package bibframe

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/freeeve/libcodex/rdf"
)

// PredAttachment carries one staff attachment's filename as a plain literal
// on the Work: scans, correspondence, acquisition
// paperwork -- cataloging working material, stored in the blob store beside
// the grain and NEVER projected to the public catalog. Being an lcat:
// statement it survives re-ingest like every curation decision, and clones
// drop it with the rest of the lcat markers (the bytes stay with the
// source).
//
// The literal is the filename the cataloger uploaded, in whatever script they
// work in. It is a *display name*, not a path: the bytes live at
// AttachmentBlobPath, under a segment derived from it. Conflating the two is
// what made the old client-side sanitizer lossy, collapsing every non-Latin
// filename onto its bare extension and letting the next upload overwrite it
// .
const PredAttachment = LcatNS + "attachment"

// maxAttachmentName bounds the display name in bytes. Each byte encodes to at
// most three segment characters, so a segment stays well under any path limit.
const maxAttachmentName = 100

// attachmentSegment is the safe blob-path shape AttachmentSegment produces:
// leading alphanumeric (which excludes dotfiles and ".."), then alphanumerics,
// dot, underscore, hyphen.
var attachmentSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,319}$`)

// legacyAttachmentName is the shape attachment names had to satisfy before
// when the display name *was* the path segment.
var legacyAttachmentName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// ValidAttachmentName reports whether name is usable as an attachment's
// display name: non-empty, valid UTF-8, no path separators, not "." or "..",
// no control characters, and short enough to encode. Every script is allowed;
// keeping the stored path safe is AttachmentSegment's job, not the name's.
func ValidAttachmentName(name string) bool {
	if name == "" || len(name) > maxAttachmentName || !utf8.ValidString(name) {
		return false
	}
	if name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// AttachmentSegment encodes a display name into a blob-path segment: a fixed
// "a" prefix, then every byte outside [A-Za-z0-9.-] escaped as "_XX" (hex).
//
// The mapping is injective, which is the whole point -- two different
// filenames must never address the same bytes. Escaping is prefix-free (a "_"
// always opens a three-character escape, and a literal "_" escapes to "_5F"),
// so distinct names give distinct bodies; the constant prefix preserves that
// and guarantees the leading alphanumeric the segment shape requires. A
// variable prefix would not: "文" and "x文" would both encode to
// "x_E6_96_87".
func AttachmentSegment(name string) (string, error) {
	if !ValidAttachmentName(name) {
		return "", fmt.Errorf("bibframe: bad attachment name %q", name)
	}
	var b strings.Builder
	b.WriteByte('a')
	for i := 0; i < len(name); i++ {
		switch c := name[i]; {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.', c == '-':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "_%02X", c)
		}
	}
	seg := b.String()
	if !attachmentSegment.MatchString(seg) {
		return "", fmt.Errorf("bibframe: attachment name %q does not encode to a safe segment", name)
	}
	return seg, nil
}

// AttachmentBlobPath is where an attachment's bytes live, sharded like grains
// and covers. name is the display name; the final segment is its encoding.
func AttachmentBlobPath(workID, name string) (string, error) {
	seg, err := AttachmentSegment(name)
	if err != nil {
		return "", err
	}
	return attachmentDir(workID) + seg, nil
}

// LegacyAttachmentBlobPath is where an attachment uploaded
// lives: the display name was the segment. Readers fall back to it so the
// rename of the encoding scheme does not orphan bytes already stored. Empty
// when name could never have been a pre-236 segment.
func LegacyAttachmentBlobPath(workID, name string) string {
	if !legacyAttachmentName.MatchString(name) {
		return ""
	}
	return attachmentDir(workID) + name
}

func attachmentDir(workID string) string {
	return "data/attachments/" + workID[:min(2, len(workID))] + "/" + workID + "/"
}

// AttachmentsOf lists the work's editorial attachment display names, sorted.
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
// and on the display name being usable. Adds are idempotent through
// canonicalization.
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
