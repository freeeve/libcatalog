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
// were not asked for (tasks/246).
func Feeds(catalogNQ []byte) ([]string, error) {
	ds, err := rdf.ParseNQuadsShared(catalogNQ)
	if err != nil {
		return nil, err
	}
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
	return out, nil
}
