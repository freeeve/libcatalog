package overdrive

import (
	"strings"

	"github.com/freeeve/libcatalog/identity"
	codexbf "github.com/freeeve/libcodex/bibframe"
)

// BIBFRAME crosswalks one OverDrive item directly to a libcodex BIBFRAME
// Work/Instance pair -- the OverDrive reference provider's real path
// (ARCHITECTURE §9), mapping the Thunder JSON feed straight to BIBFRAME with no
// MARC intermediate. This keeps data the MARC detour drops (notably BISAC
// classification) and models subjects as bf:Topic without MARC's 6xx/653
// constraints. The result is serialized by BIBFRAME.Graph, so it takes the same
// graph shape as a record-derived BIBFRAME.
func (it Item) BIBFRAME() *codexbf.BIBFRAME {
	bib := &codexbf.BIBFRAME{}

	title := codexbf.Title{MainTitle: it.Title, Subtitle: it.Subtitle}

	// Work: intellectual content -- content class, preferred title, agents,
	// topical subjects, languages, and BISAC classification.
	bib.Work.Class = workClass(it.Type.ID)
	if title.MainTitle != "" {
		bib.Work.Titles = append(bib.Work.Titles, title)
	}
	bib.Work.Contributions = it.bibContributions()
	for _, s := range it.Subjects {
		if s.Name != "" {
			bib.Work.Subjects = append(bib.Work.Subjects, codexbf.Subject{Class: "Topic", Label: s.Name})
		}
	}
	for _, l := range it.Languages {
		if code := iso639_2(l.ID); code != "" {
			bib.Work.Languages = append(bib.Work.Languages, code)
		}
	}
	// BISAC is a controlled classification. libcodex's Classification carries a
	// class + value but no scheme, so the "bisacsh" source is not yet expressed
	// (see tasks/008); the code is retained -- which the MARC path dropped.
	for _, b := range it.BISAC {
		if b.Code != "" {
			bib.Work.Classifications = append(bib.Work.Classifications,
				codexbf.Classification{Class: "Classification", Value: b.Code})
		}
	}

	// Instance: this publication -- transcribed title, edition, provision, and
	// identifiers (ISBNs, the OverDrive title id, and the Reserve ID).
	if title.MainTitle != "" {
		bib.Instance.Titles = append(bib.Instance.Titles, title)
	}
	bib.Instance.EditionStatement = it.Edition
	if p := it.provisionBF(); p != nil {
		bib.Instance.Provision = p
	}
	for _, isbn := range it.ISBNs() {
		bib.Instance.Identifiers = append(bib.Instance.Identifiers,
			codexbf.Identifier{Class: "Isbn", Value: isbn})
	}
	// The OverDrive title id and Reserve ID are local identifiers. They land as
	// bf:Identifier; the scheme that distinguishes them (overdrive vs the Thunder
	// availability key) awaits libcodex Identifier source support (tasks/008).
	if it.ID != "" {
		bib.Instance.Identifiers = append(bib.Instance.Identifiers,
			codexbf.Identifier{Class: "Identifier", Value: it.ID})
	}
	if it.ReserveID != "" {
		bib.Instance.Identifiers = append(bib.Instance.Identifiers,
			codexbf.Identifier{Class: "Identifier", Value: it.ReserveID})
	}
	return bib
}

// WorkID returns a stable, IRI- and filesystem-safe id for the item, taken from
// the OverDrive title id (falling back to the Reserve ID). It names the grain
// file and the #<id>Work / #<id>Instance node IRIs. Phase 0 only: ARCHITECTURE
// §4's identity model replaces this with a minted, provider-independent id.
func (it Item) WorkID() string {
	if id := sanitizeID(it.ID); id != "" {
		return id
	}
	return sanitizeID(it.ReserveID)
}

// Identity returns the record's resolution keys and clustering fields for
// identity.Resolver to assign stable Work/Instance ids (ARCHITECTURE §4). The
// keys are the OverDrive title id, each ISBN, and the Reserve ID, namespaced by
// scheme -- ordered so the most specific (the title id) resolves first and ISBN
// serves as the cross-provider merge key. The clustering fields are the primary
// author, the main title, and the original language.
func (it Item) Identity() identity.Record {
	rec := identity.Record{
		Author: it.primaryAuthor(),
		Title:  it.Title,
		Lang:   it.primaryLang(),
	}
	if it.ID != "" {
		rec.ProviderKeys = append(rec.ProviderKeys, identity.ProviderKey(identity.SchemeID, it.ID))
	}
	for _, isbn := range it.ISBNs() {
		rec.ProviderKeys = append(rec.ProviderKeys, identity.ProviderKey(identity.SchemeISBN, isbn))
	}
	if it.ReserveID != "" {
		rec.ProviderKeys = append(rec.ProviderKeys, identity.ProviderKey(identity.SchemeID, it.ReserveID))
	}
	return rec
}

// primaryAuthor returns the transcribed name of the item's first author, or "".
func (it Item) primaryAuthor() string {
	authors, _ := it.contributors()
	if len(authors) > 0 {
		return authors[0].name
	}
	return ""
}

// primaryLang returns the ISO 639-2 code of the item's first mappable language,
// or "".
func (it Item) primaryLang() string {
	for _, l := range it.Languages {
		if code := iso639_2(l.ID); code != "" {
			return code
		}
	}
	return ""
}

// bibContributions maps the item's creators to BIBFRAME contributions, marking
// the first author as the primary contribution (as a MARC 1xx would be) and the
// rest -- extra authors, narrators, illustrators -- as added contributions.
func (it Item) bibContributions() []codexbf.Contribution {
	authors, others := it.contributors()
	out := make([]codexbf.Contribution, 0, len(authors)+len(others))
	for i, c := range authors {
		out = append(out, codexbf.Contribution{
			Primary: i == 0, Class: "Person", Label: c.name, Role: c.role,
		})
	}
	for _, c := range others {
		out = append(out, codexbf.Contribution{
			Primary: false, Class: "Person", Label: c.name, Role: c.role,
		})
	}
	return out
}

// provisionBF builds the publication provision (publisher, year) or nil when the
// item names neither.
func (it Item) provisionBF() *codexbf.Provision {
	p := &codexbf.Provision{}
	if it.Publisher != nil {
		p.Publisher = it.Publisher.Name
	}
	if len(it.PublishDate) >= 4 {
		p.Date = it.PublishDate[:4]
	}
	if p.Publisher == "" && p.Date == "" {
		return nil
	}
	return p
}

// workClass maps an OverDrive media type to the BIBFRAME class refining bf:Work:
// nonmusical audio for an audiobook, text otherwise. It mirrors libcodex's
// leader-byte crosswalk ('i' -> Audio, 'a' -> Text).
func workClass(typeID string) string {
	if typeID == "audiobook" {
		return "Audio"
	}
	return "Text"
}

// sanitizeID keeps the characters valid in an IRI fragment and a filename,
// dropping the rest.
func sanitizeID(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '.', c == '-', c == '_':
			b.WriteByte(c)
		}
	}
	return b.String()
}
