package overdrive

import (
	"strings"

	"github.com/freeeve/libcatalog/identity"
	codexbf "github.com/freeeve/libcodex/bibframe"
)

// bf:source scheme codes for OverDrive's identifiers and classification, tagged so
// each is unambiguously recoverable from a grain (ARCHITECTURE §9, tasks/008). They
// are exported so downstream consumers -- notably the runtime availability adapter
// (tasks/004), which keys on the Reserve ID -- select the right node by scheme.
const (
	// SourceBISAC is the bf:source of a BISAC subject-category code.
	SourceBISAC = "bisacsh"
	// SourceOverDrive is the bf:source of the OverDrive title id.
	SourceOverDrive = "overdrive"
	// SourceReserveID is the bf:source of the OverDrive Reserve ID -- the stable
	// per-edition key the availability adapter queries at view time.
	SourceReserveID = "overdrive-reserve"
)

// BIBFRAME crosswalks one OverDrive item directly to a libcodex BIBFRAME
// Work/Instance pair -- the OverDrive reference provider's real path
// (ARCHITECTURE §9), mapping the Thunder JSON feed straight to BIBFRAME with no
// MARC intermediate. This keeps data the MARC detour drops (notably BISAC
// classification) and models subjects as bf:Topic without MARC's 6xx/653
// constraints. The result is serialized by BIBFRAME.Graph, so it takes the same
// graph shape as a record-derived BIBFRAME.
func (it Item) BIBFRAME() *codexbf.BIBFRAME {
	return &codexbf.BIBFRAME{Work: it.Work(), Instance: it.Instance()}
}

// Work returns the item's Work-level BIBFRAME (intellectual content): content
// class, preferred title, agents, topical subjects, languages, and BISAC
// classification. When items cluster into one Work, this is the shared node.
func (it Item) Work() codexbf.Work {
	w := codexbf.Work{Class: workClass(it.Type.ID)}
	if title := it.title(); title.MainTitle != "" {
		w.Titles = append(w.Titles, title)
	}
	w.Contributions = it.bibContributions()
	for _, s := range it.Subjects {
		if s.Name != "" {
			w.Subjects = append(w.Subjects, codexbf.Subject{Class: "Topic", Label: s.Name})
		}
	}
	for _, l := range it.Languages {
		if code := iso639_2(l.ID); code != "" {
			w.Languages = append(w.Languages, code)
		}
	}
	// BISAC is a controlled classification: the code carries bf:source "bisacsh" so
	// the scheme is explicit (tasks/008). The MARC detour dropped these entirely.
	for _, b := range it.BISAC {
		if b.Code != "" {
			w.Classifications = append(w.Classifications,
				codexbf.Classification{Class: "Classification", Value: b.Code, Source: SourceBISAC})
		}
	}
	return w
}

// Instance returns the item's Instance-level BIBFRAME (this publication):
// transcribed title, edition, provision, and identifiers (ISBNs, the OverDrive
// title id, and the Reserve ID). Each clustered item contributes one Instance.
func (it Item) Instance() codexbf.Instance {
	var inst codexbf.Instance
	if title := it.title(); title.MainTitle != "" {
		inst.Titles = append(inst.Titles, title)
	}
	inst.EditionStatement = it.Edition
	if p := it.provisionBF(); p != nil {
		inst.Provision = p
	}
	for _, isbn := range it.ISBNs() {
		inst.Identifiers = append(inst.Identifiers, codexbf.Identifier{Class: "Isbn", Value: isbn})
	}
	// The OverDrive title id and Reserve ID are local identifiers, distinguished by
	// bf:source (tasks/008): the title id carries "overdrive", the Reserve ID
	// "overdrive-reserve" so the availability adapter can recover it unambiguously.
	// The Reserve ID is a stable per-edition key (not volatile availability data),
	// so it stays in the feed grain per the ARCHITECTURE §5 provenance model.
	if it.ID != "" {
		inst.Identifiers = append(inst.Identifiers, codexbf.Identifier{Class: "Identifier", Value: it.ID, Source: SourceOverDrive})
	}
	if it.ReserveID != "" {
		inst.Identifiers = append(inst.Identifiers, codexbf.Identifier{Class: "Identifier", Value: it.ReserveID, Source: SourceReserveID})
	}
	return inst
}

// title is the item's transcribed title, shared by the Work and each Instance.
func (it Item) title() codexbf.Title {
	return codexbf.Title{MainTitle: it.Title, Subtitle: it.Subtitle}
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
