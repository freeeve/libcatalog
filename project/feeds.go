package project

import (
	"sort"
	"strings"

	"github.com/freeeve/libcodex/rdf"
)

// feedPrefix is the scheme bibframe.FeedGraph mints its named graphs under.
const feedPrefix = "feed:"

// Feeds lists the provider names that appear as feed graphs in a catalog.nq,
// sorted. It answers "what could this catalog be projected as?", which is the
// question a caller needs when a requested provider projects nothing: the
// difference between a catalog that is empty and a catalog whose feeds simply
// were not asked for.
func Feeds(catalogNQ []byte) ([]string, error) {
	ds, err := rdf.ParseNQuadsShared(catalogNQ)
	if err != nil {
		return nil, err
	}
	return FeedsDataset(ds), nil
}

// FeedsDataset is Feeds over an already-parsed dataset.
// preRenameNS is the RDF namespace this project used before its rename; a
// graph ingested back then carries adopter extras under these predicates,
// which the current projection cannot read.
const preRenameNS = "https://github.com/freeeve/libcatalog/ns#"

// PreRenameCount reports how many statements carry predicates under the
// pre-rename namespace. The projection reads extras only from the current
// namespace, so a non-zero count means adopter fields (covers, ratings, custom
// display keys) will silently vanish from the public catalog unless the graph
// is re-ingested or migrated; callers should surface it as a warning.
func PreRenameCount(ds *rdf.Dataset) int {
	n := 0
	for _, q := range ds.Quads {
		if strings.HasPrefix(q.P.Value, preRenameNS) {
			n++
		}
	}
	return n
}

func FeedsDataset(ds *rdf.Dataset) []string {
	seen := map[string]bool{}
	for _, q := range ds.Quads {
		if !q.G.IsIRI() {
			continue
		}
		if name, ok := strings.CutPrefix(q.G.Value, feedPrefix); ok && name != "" {
			seen[name] = true
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
