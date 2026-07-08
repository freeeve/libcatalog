// Package marc is the MARC ingest provider (ARCHITECTURE §9): it reads an ISO 2709
// MARC stream and yields each record as a resolvable ingest.Record, so an existing
// ILS's MARC flows through the same two-tier identity + clustering pipeline as any
// other provider (ingest.Run). Each record is crosswalked to BIBFRAME by libcodex's
// FromRecord; identity keys are drawn from that BIBFRAME plus the MARC control
// number. This is the modern, clustered path -- distinct from the legacy per-record
// bibframe.BuildMARC (which keys grains on the MARC 001 with no minted identity).
package marc

import (
	"context"
	"fmt"
	"os"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/identity"
	"github.com/freeeve/libcat/ingest"
	codex "github.com/freeeve/libcodex"
	codexbf "github.com/freeeve/libcodex/bibframe"
)

// ProviderName is the MARC provider's registry key and default provenance feed
// graph (feed:marc). A deployment onboarding an ILS can override the feed to name
// the source (e.g. feed:sierra).
const ProviderName = "marc"

// Provider reads an ISO 2709 MARC file and yields its records for ingest.Run.
type Provider struct {
	feed string
	path string
}

// New is the ingest.Factory for MARC. It takes the .mrc file path from
// Config.Source and the provenance feed from Config.Feed (default feed:marc).
func New(cfg ingest.Config) (ingest.Provider, error) {
	if cfg.Source == "" {
		return nil, fmt.Errorf("marc: MARC file (Config.Source) is required")
	}
	feed := cfg.Feed
	if feed == "" {
		feed = ProviderName
	}
	return Provider{feed: feed, path: cfg.Source}, nil
}

// Name returns the provider's provenance feed graph name.
func (p Provider) Name() string { return p.feed }

// Role reports MARC as a bibliographic ingest source (feed:<name>).
func (p Provider) Role() ingest.Role { return ingest.RoleIngest }

// Records reads every record from the MARC file, crosswalks each to BIBFRAME, and
// returns them as ingest records. ctx is accepted for the Provider contract; the
// file read is local and does not observe cancellation.
func (p Provider) Records(_ context.Context) ([]ingest.Record, error) {
	f, err := os.Open(p.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	recs, err := bibframe.ReadMARC(f)
	if err != nil {
		return nil, err
	}
	return FromCodexRecords(recs), nil
}

// FromCodexRecords crosswalks already-parsed MARC records to ingest records
// -- the same shape file ingest produces, exposed for copy cataloging
// (tasks/050), where records arrive from Z39.50/SRU targets or staged
// uploads instead of a file.
func FromCodexRecords(recs []*codex.Record) []ingest.Record {
	out := make([]ingest.Record, 0, len(recs))
	for _, rec := range recs {
		bib := codexbf.FromRecord(rec)
		cleanFreeText(bib)
		out = append(out, record{
			bib:      bib,
			id:       recordIdentity(bib, rec.ControlField("001")),
			verbatim: bibframe.VerbatimFields(rec),
		})
	}
	return out
}

// cleanFreeText normalizes the crosswalk's free-text carriers before grains
// are built: vendor MARC (OverDrive Marc Express among them) embeds HTML
// character references and markup in 520/505/5xx prose (&#8212;, <b>...) and
// in transcribed titles (245/246), which would otherwise bake into bf:summary
// and bf:title literals verbatim (tasks/081, tasks/089). Identifier and heading
// fields stay untouched; the verbatim sidecar preserves the original field
// bytes for fidelity.
func cleanFreeText(bib *codexbf.BIBFRAME) {
	cleanTitles(bib.Work.Titles)
	cleanTitles(bib.Instance.Titles)
	cleanVariantTitles(bib.Work.VariantTitles)
	cleanVariantTitles(bib.Instance.VariantTitles)
	bib.Instance.ResponsibilityStatement = ingest.CleanText(bib.Instance.ResponsibilityStatement)
	for i, s := range bib.Work.Summary {
		bib.Work.Summary[i] = ingest.CleanText(s)
	}
	for i, s := range bib.Work.TableOfContents {
		bib.Work.TableOfContents[i] = ingest.CleanText(s)
	}
	for i := range bib.Work.Notes {
		bib.Work.Notes[i].Label = ingest.CleanText(bib.Work.Notes[i].Label)
	}
	for i := range bib.Instance.Notes {
		bib.Instance.Notes[i].Label = ingest.CleanText(bib.Instance.Notes[i].Label)
	}
}

// cleanTitles normalizes the transcribed parts of each title in place.
func cleanTitles(ts []codexbf.Title) {
	for i := range ts {
		ts[i].MainTitle = ingest.CleanText(ts[i].MainTitle)
		ts[i].Subtitle = ingest.CleanText(ts[i].Subtitle)
		ts[i].PartName = ingest.CleanText(ts[i].PartName)
	}
}

// cleanVariantTitles normalizes 246 variant/parallel titles in place.
func cleanVariantTitles(ts []codexbf.VariantTitle) {
	for i := range ts {
		ts[i].MainTitle = ingest.CleanText(ts[i].MainTitle)
		ts[i].Subtitle = ingest.CleanText(ts[i].Subtitle)
		ts[i].PartName = ingest.CleanText(ts[i].PartName)
	}
}

// Identity derives the resolution keys for one parsed MARC record -- what
// the copy-cataloging match banner runs through a dry-run resolver.
func Identity(rec *codex.Record) identity.Record {
	return recordIdentity(codexbf.FromRecord(rec), rec.ControlField("001"))
}

// record is one MARC record as an ingest.Record: its BIBFRAME (from FromRecord),
// the identity keys derived from it, and the crosswalk-lossy fields preserved
// verbatim for the sidecar (tasks/049). Identity is precomputed so the
// interface's three accessors do not re-crosswalk.
type record struct {
	bib      *codexbf.BIBFRAME
	id       identity.Record
	verbatim []string
}

func (r record) Identity() identity.Record  { return r.id }
func (r record) Work() codexbf.Work         { return r.bib.Work }
func (r record) Instance() codexbf.Instance { return r.bib.Instance }
func (r record) Verbatim() []string         { return r.verbatim }

// recordIdentity derives resolution keys and clustering fields from a record's
// BIBFRAME. The MARC control number (001) is the most specific provider-local key
// and resolves first; ISBN/ISSN are the cross-provider merge keys (ARCHITECTURE
// §4/§9). Clustering fields are the primary contributor, the main title, and the
// first language.
func recordIdentity(bib *codexbf.BIBFRAME, controlNumber string) identity.Record {
	rec := identity.Record{}
	if len(bib.Work.Titles) > 0 {
		rec.Title = bib.Work.Titles[0].MainTitle
	}
	for _, c := range bib.Work.Contributions {
		if c.Primary && c.Label != "" {
			rec.Author = c.Label
			break
		}
	}
	if rec.Author == "" {
		for _, c := range bib.Work.Contributions {
			if c.Label != "" {
				rec.Author = c.Label
				break
			}
		}
	}
	if len(bib.Work.Languages) > 0 {
		rec.Lang = bib.Work.Languages[0]
	}
	if controlNumber != "" {
		rec.ProviderKeys = append(rec.ProviderKeys, identity.ProviderKey(identity.SchemeID, controlNumber))
	}
	for _, id := range bib.Instance.Identifiers {
		switch id.Class {
		case "Isbn":
			if id.Value != "" {
				rec.ProviderKeys = append(rec.ProviderKeys, identity.ProviderKey(identity.SchemeISBN, id.Value))
			}
		case "Issn":
			if id.Value != "" {
				rec.ProviderKeys = append(rec.ProviderKeys, identity.ProviderKey(identity.SchemeISSN, id.Value))
			}
		}
	}
	return rec
}
