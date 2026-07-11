// Package hardcover is a first-party ingest provider for a Hardcover
// (hardcover.app) "Read" shelf. It pulls a user's read books over Hardcover's
// GraphQL API and yields them as resolvable records for the shared ingest.Run
// pipeline (ARCHITECTURE §9), so a Hardcover shelf flows through the same
// identity/clustering and `lcat project` path as any other source -- no bespoke
// clustering, facet counting, or subject control outside the framework.
//
// Each read book is exploded into one record per collapsed edition format
// (physical / audiobook / ebook): the records share the book's Work-clustering key
// (author, title, language) so they cluster into a single Work, while each carries a
// distinct per-format instance key, so a mixed-format read becomes one Work with an
// Instance per format -- exactly how OverDrive editions cluster.
package hardcover

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// SourceHardcover is the bf:source scheme of a Hardcover instance's provenance id
// (the book/edition id), tagged so it is recoverable from a grain for back-links
// (mirrors OverDrive's source-tagged identifiers; ARCHITECTURE §9).
const SourceHardcover = "hardcover"

// maxGenres caps the genres carried per book (most-voted first), matching the demo's
// pipeline so the projected tag set stays focused.
const maxGenres = 8

// userBook is one row of the Hardcover `user_books` read shelf: the reading-log
// fields (rating, read dates) plus the nested bibliographic `book`.
type userBook struct {
	ID            int      `json:"id"`
	Rating        *float64 `json:"rating"`
	LastReadDate  string   `json:"last_read_date"`
	FirstReadDate string   `json:"first_read_date"`
	Book          book     `json:"book"`
}

// book is the bibliographic core of a user_book: title, agents, genre tags, cover,
// and its editions (collapsed to one Instance per format downstream).
type book struct {
	ID            int             `json:"id"`
	Slug          string          `json:"slug"`
	Title         string          `json:"title"`
	Subtitle      string          `json:"subtitle"`
	Description   string          `json:"description"`
	Image         *image          `json:"image"`
	Contributions []contribution  `json:"contributions"`
	CachedTags    json.RawMessage `json:"cached_tags"`
	Editions      []edition       `json:"editions"`
}

type image struct {
	URL string `json:"url"`
}

// contribution is one agent's role on a book; author.name is the transcribed name.
type contribution struct {
	Contribution string  `json:"contribution"`
	Author       *author `json:"author"`
}

type author struct {
	Name string `json:"name"`
}

// edition is one published edition; reading_format_id (or the format text) maps it to
// a discovery format, and it carries ISBNs and an optional cover image.
type edition struct {
	ID              int    `json:"id"`
	ISBN13          string `json:"isbn_13"`
	ISBN10          string `json:"isbn_10"`
	ReadingFormatID *int   `json:"reading_format_id"`
	EditionFormat   string `json:"edition_format"`
	PhysicalFormat  string `json:"physical_format"`
	Image           *image `json:"image"`
}

// tagCount is one genre tag and its vote count. Hardcover serializes an element as
// either an object {tag,count} or a bare string; both decode here.
type tagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// UnmarshalJSON accepts a genre element as a {tag,count} object or a bare string.
func (t *tagCount) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		t.Tag, t.Count = s, 0
		return nil
	}
	type raw tagCount
	var r raw
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}
	*t = tagCount(r)
	return nil
}

// formatInstance is one collapsed edition format for a book: the discovery format and
// the ISBNs of the edition chosen to represent it.
type formatInstance struct {
	format string
	isbns  []string
}

// collapseFormats reduces a book's editions to one entry per discovery format, keeping
// the first edition seen for a format but upgrading to a later ISBN-bearing edition
// when the incumbent has none. Editions with no derivable format are skipped. The
// result is ordered by format for deterministic instance emission.
func collapseFormats(editions []edition) []formatInstance {
	byFormat := map[string]*formatInstance{}
	for _, e := range editions {
		f := formatOf(e)
		if f == "" {
			continue
		}
		isbns := editionISBNs(e)
		if cur, ok := byFormat[f]; ok {
			if len(isbns) > 0 && len(cur.isbns) == 0 {
				cur.isbns = isbns
			}
			continue
		}
		byFormat[f] = &formatInstance{format: f, isbns: isbns}
	}
	out := make([]formatInstance, 0, len(byFormat))
	for _, fi := range byFormat {
		out = append(out, *fi)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].format < out[j].format })
	return out
}

// editionISBNs returns the edition's ISBNs, ISBN-13 first, dropping empties.
func editionISBNs(e edition) []string {
	var out []string
	if e.ISBN13 != "" {
		out = append(out, e.ISBN13)
	}
	if e.ISBN10 != "" {
		out = append(out, e.ISBN10)
	}
	return out
}

// formatOf maps an edition to a discovery format: first by reading_format_id
// (1 physical, 2 audiobook, 4 ebook), then by the edition/physical format text, and
// finally "" when neither identifies one.
func formatOf(e edition) string {
	if e.ReadingFormatID != nil {
		switch *e.ReadingFormatID {
		case 1:
			return "physical"
		case 2:
			return "audiobook"
		case 4:
			return "ebook"
		}
	}
	text := strings.ToLower(strings.TrimSpace(e.EditionFormat))
	if text == "" {
		text = strings.ToLower(strings.TrimSpace(e.PhysicalFormat))
	}
	switch {
	case text == "":
		return ""
	case strings.Contains(text, "audio") || strings.Contains(text, "audible"):
		return "audiobook"
	case strings.Contains(text, "ebook") || strings.Contains(text, "e-book") || strings.Contains(text, "kindle"):
		return "ebook"
	default:
		return "physical"
	}
}

// genres returns the book's genre tags, most-voted first, deduped and capped at
// maxGenres. cached_tags may arrive as a JSON object or a JSON string wrapping one,
// under key "Genre" (or "genre"); both are handled.
func (b book) genres() []string {
	tags := parseGenres(b.CachedTags)
	sort.SliceStable(tags, func(i, j int) bool { return tags[i].Count > tags[j].Count })
	seen := map[string]bool{}
	var out []string
	for _, t := range tags {
		if t.Tag == "" || seen[t.Tag] {
			continue
		}
		seen[t.Tag] = true
		out = append(out, t.Tag)
		if len(out) == maxGenres {
			break
		}
	}
	return out
}

// parseGenres extracts the Genre tag list from a cached_tags value that may be a JSON
// object or a JSON string wrapping one. It is best-effort: a shape it cannot read
// yields no genres rather than an error.
func parseGenres(raw json.RawMessage) []tagCount {
	data := []byte(raw)
	if len(data) == 0 {
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return nil
		}
		data = []byte(s)
	}
	var ct struct {
		Genre  []tagCount `json:"Genre"`
		GenreL []tagCount `json:"genre"`
	}
	if err := json.Unmarshal(data, &ct); err != nil {
		return nil
	}
	if len(ct.Genre) > 0 {
		return ct.Genre
	}
	return ct.GenreL
}

// lastFirst normalizes a transcribed personal name to "Last, First Middle" so agents
// sort and cluster consistently. A name that is empty, already comma-bearing, or a
// single token passes through unchanged (compound surnames are not inferred).
func lastFirst(name string) string {
	n := strings.TrimSpace(name)
	if n == "" || strings.Contains(n, ",") {
		return n
	}
	parts := strings.Fields(n)
	if len(parts) < 2 {
		return n
	}
	last := parts[len(parts)-1]
	rest := strings.Join(parts[:len(parts)-1], " ")
	return last + ", " + rest
}

// cover returns the book's cover image URL: the book image, else the first edition
// that carries one, else "".
func (b book) cover() string {
	if b.Image != nil && b.Image.URL != "" {
		return b.Image.URL
	}
	for _, e := range b.Editions {
		if e.Image != nil && e.Image.URL != "" {
			return e.Image.URL
		}
	}
	return ""
}

// formatRating renders a numeric rating without a trailing ".0", so a whole-star
// rating projects as "4" rather than "4.0".
func formatRating(r float64) string {
	return strconv.FormatFloat(r, 'f', -1, 64)
}
