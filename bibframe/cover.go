package bibframe

import (
	"fmt"

	"github.com/freeeve/libcodex/rdf"
)

// CoverExtraKey is the extras key the OPAC's cover slot reads (the tasks/022
// adapter passthrough); an uploaded cover writes the same key editorially,
// overlaying any feed-carried value (tasks/215).
const CoverExtraKey = "cover"

// CoverBlobPath is where a work's uploaded cover bytes live in the blob
// store. ext is the extension without the dot ("jpg", "png", "webp").
func CoverBlobPath(workID, ext string) string {
	return "data/covers/" + workID[:min(2, len(workID))] + "/" + workID + "." + ext
}

// CoverOf returns the work's effective cover URL, or "" when it has none.
//
// The cover is not a profile field, so it never appears in the editor doc's
// fields map -- the editor's Cover panel read it there and therefore reported
// "none" for every work it had not itself just uploaded to, leaving no way to
// take a published cover down (tasks/242). Readers ask for it by name instead.
//
// An editorial statement overlays a feed-carried one, matching SetCover, whose
// empty url removes the editorial layer and lets the feed value show again.
func CoverOf(grainNQ []byte, workID string) (string, error) {
	ds, err := rdf.ParseNQuads(grainNQ)
	if err != nil {
		return "", err
	}
	work := rdf.NewIRI(WorkIRI(workID))
	pred := rdf.NewIRI(ExtraPred + CoverExtraKey)
	ed := EditorialGraph()
	feed := ""
	for _, q := range ds.Quads {
		if q.S != work || q.P != pred || !q.O.IsLiteral() {
			continue
		}
		if q.G == ed {
			return q.O.Value, nil
		}
		if feed == "" {
			feed = q.O.Value
		}
	}
	return feed, nil
}

// SetCover records url as the work's editorial cover (lcat:extra/cover in
// the editorial graph), replacing any previous editorial cover statement,
// and returns the re-canonicalized grain. The work must exist in the grain
// (the tasks/202/211/214 invariant). An empty url removes the editorial
// cover, letting any feed-carried value show through again.
func SetCover(grainNQ []byte, workID, url string) ([]byte, error) {
	ds, err := rdf.ParseNQuads(grainNQ)
	if err != nil {
		return nil, err
	}
	work := rdf.NewIRI(WorkIRI(workID))
	described := false
	for i := range ds.Quads {
		if ds.Quads[i].S == work {
			described = true
			break
		}
	}
	if !described {
		return nil, fmt.Errorf("bibframe: grain does not describe work %s", workID)
	}
	ed := EditorialGraph()
	pred := rdf.NewIRI(ExtraPred + CoverExtraKey)
	keep := ds.Quads[:0]
	for _, q := range ds.Quads {
		if q.G == ed && q.S == work && q.P == pred {
			continue
		}
		keep = append(keep, q)
	}
	ds.Quads = keep
	stripped, err := ds.Canonical()
	if err != nil {
		return nil, err
	}
	if url == "" {
		return stripped, nil
	}
	return ApplyEditorialPatch(stripped, Patch{Add: []rdf.Quad{{
		S: work, P: pred, O: rdf.NewLiteral(url, "", ""),
	}}})
}
