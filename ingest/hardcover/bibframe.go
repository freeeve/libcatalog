package hardcover

import (
	"strconv"
	"strings"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/identity"
	codexbf "github.com/freeeve/libcodex/bibframe"
)

// record is one Hardcover read book at one edition format: an ingest.Record realizing
// a single Instance of the book's Work. A book's per-format records share Work-level
// data (so they cluster into one Work) and differ only in the Instance and instance
// identity keys. fi.format is "" for a book with no derivable edition format.
type record struct {
	ub userBook
	fi formatInstance
}

// Work returns the book's Work-level BIBFRAME: content class, title, agents, genre
// topics (uncontrolled tags until a later controlled-subject pass promotes them), and
// language. It is identical across a book's per-format records, so the shared pipeline's
// first-record-wins Work grouping is well defined.
func (r record) Work() codexbf.Work {
	b := r.ub.Book
	w := codexbf.Work{Class: "Text"}
	if t := r.title(); t.MainTitle != "" {
		w.Titles = append(w.Titles, t)
	}
	w.Contributions = b.contributions()
	// Genres that map to a controlled authority become controlled subjects (see
	// ControlledSubjects); the rest stay uncontrolled tags, so both dimensions coexist
	// without duplicating a genre.
	for _, g := range b.tags() {
		w.Subjects = append(w.Subjects, codexbf.Subject{Class: "Topic", Label: g})
	}
	w.Languages = []string{"eng"}
	if desc := strings.TrimSpace(b.Description); desc != "" {
		w.Summary = []string{desc}
	}
	return w
}

// ControlledSubjects promotes the book's mapped genres to controlled-vocabulary subjects
// (authority URI + localized labels + skos:broader) via the shipped table, so the shared
// pipeline emits them into the graph as first-class subjects (ingest.SubjectEnricher).
func (r record) ControlledSubjects() []bibframe.AuthoritySubject {
	return r.ub.Book.controlledSubjects()
}

// Instance returns this edition format's Instance-level BIBFRAME: transcribed title,
// the RDA media type that carries the discovery format, the edition's
// ISBNs, and a source-tagged Hardcover provenance id for back-links.
func (r record) Instance() codexbf.Instance {
	var inst codexbf.Instance
	if t := r.title(); t.MainTitle != "" {
		inst.Titles = append(inst.Titles, t)
	}
	if m, ok := rdaMediaTerm(r.fi.format); ok {
		inst.Media = []codexbf.RDATerm{m}
	}
	for _, isbn := range r.fi.isbns {
		inst.Identifiers = append(inst.Identifiers, codexbf.Identifier{Class: "Isbn", Value: isbn})
	}
	inst.Identifiers = append(inst.Identifiers,
		codexbf.Identifier{Class: "Identifier", Value: r.provenanceID(), Source: SourceHardcover})
	return inst
}

// Identity returns the record's resolution keys and clustering fields. The instance key
// is the Hardcover book id scoped by format (so a book's formats resolve to distinct
// Instances), plus each ISBN as a cross-provider merge key; the clustering fields are
// the primary author, the title, and the language, so a book's format records cluster
// into one Work.
func (r record) Identity() identity.Record {
	rec := identity.Record{Author: r.primaryAuthor(), Title: r.ub.Book.Title, Lang: "eng"}
	key := "hardcover:" + strconv.Itoa(r.ub.Book.ID)
	if r.fi.format != "" {
		key += ":" + r.fi.format
	}
	rec.ProviderKeys = append(rec.ProviderKeys, identity.ProviderKey(identity.SchemeID, key))
	for _, isbn := range r.fi.isbns {
		rec.ProviderKeys = append(rec.ProviderKeys, identity.ProviderKey(identity.SchemeISBN, isbn))
	}
	return rec
}

// Extras returns the book's non-BIBFRAME display fields carried through to
// catalog.json's `extra` object via the feed provenance graph: cover URL,
// personal rating, and read date. The description is NOT an extra: it is core
// bibliographic data, emitted as bf:summary on the Work. Identical across
// a book's format records (first wins). Nil when the book supplies none.
func (r record) Extras() map[string]string {
	m := map[string]string{}
	if c := r.ub.Book.cover(); c != "" {
		m["cover"] = c
	}
	if r.ub.Rating != nil {
		m["rating"] = formatRating(*r.ub.Rating)
	}
	if d := r.dateRead(); d != "" {
		m["dateRead"] = d
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// title is the book's transcribed title, shared by its Work and each Instance.
func (r record) title() codexbf.Title {
	return codexbf.Title{MainTitle: r.ub.Book.Title, Subtitle: r.ub.Book.Subtitle}
}

// dateRead prefers the last read date, falling back to the first.
func (r record) dateRead() string {
	if r.ub.LastReadDate != "" {
		return r.ub.LastReadDate
	}
	return r.ub.FirstReadDate
}

// provenanceID is the Hardcover id backing this Instance's source-tagged identifier
// (the book id, scoped by format so a book's formats carry distinct provenance).
func (r record) provenanceID() string {
	id := strconv.Itoa(r.ub.Book.ID)
	if r.fi.format != "" {
		id += ":" + r.fi.format
	}
	return id
}

// primaryAuthor returns the normalized name of the book's first credited author, or "".
func (r record) primaryAuthor() string {
	for _, c := range r.ub.Book.Contributions {
		if c.Author != nil && c.Author.Name != "" {
			return lastFirst(c.Author.Name)
		}
	}
	return ""
}

// contributions maps a book's Hardcover contributions to BIBFRAME contributions: the
// first credited agent is the primary contribution (as a MARC 1xx would lead), names
// normalized to "Last, First", roles lowercased (default "author"), deduped by the raw
// name and role.
func (b book) contributions() []codexbf.Contribution {
	seen := map[string]bool{}
	var out []codexbf.Contribution
	for _, c := range b.Contributions {
		if c.Author == nil || c.Author.Name == "" {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(c.Contribution))
		if role == "" {
			role = "author"
		}
		key := c.Author.Name + "|" + role
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, codexbf.Contribution{
			Primary: len(out) == 0,
			Class:   "Person",
			Label:   lastFirst(c.Author.Name),
			Roles:   []codexbf.Role{{Term: role}},
		})
	}
	return out
}

// rdaMediaTerm maps a discovery format to its RDA media type (337 -> bf:media), which
// the projector reads back into the format facet: audio for an audiobook,
// computer for an ebook, unmediated for a physical book (which the projector renders as
// "print"). ok is false for a formatless record, so no media statement is emitted.
func rdaMediaTerm(format string) (codexbf.RDATerm, bool) {
	switch format {
	case "audiobook":
		return codexbf.RDATerm{Code: "s", Label: "audio"}, true
	case "ebook":
		return codexbf.RDATerm{Code: "c", Label: "computer"}, true
	case "physical":
		return codexbf.RDATerm{Code: "n", Label: "unmediated"}, true
	default:
		return codexbf.RDATerm{}, false
	}
}
