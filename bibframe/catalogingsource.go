package bibframe

import (
	codex "github.com/freeeve/libcodex"
)

// DecodeGrainMARCSource is DecodeGrainMARC plus the cataloging-
// source derivation: given the deployment's MARC organization code, each
// decoded record's 040 derives from graph facts AT DECODE TIME -- the field
// is never stored, so it cannot drift from the named-graph provenance that
// remains the statement-level source of truth. A record that arrived with a
// 040 gains the deployment as one trailing $d (modifying agency) when the
// grain carries editorial statements; a record with no 040 -- the born-
// digital feeds -- synthesizes 040 $a<org>$c<org> as its own cataloging
// source. An empty org code decodes unchanged.
func DecodeGrainMARCSource(grain []byte, org string) ([]*codex.Record, error) {
	recs, edited, err := decodeGrainMARC(grain)
	if err != nil || org == "" {
		return recs, err
	}
	for _, rec := range recs {
		applyCatalogingSource(rec, org, edited)
	}
	return recs, nil
}

// applyCatalogingSource derives one record's 040 from the deployment org
// code and whether the grain was locally edited.
func applyCatalogingSource(rec *codex.Record, org string, edited bool) {
	f, ok := rec.DataField("040")
	if !ok {
		rec.InsertField(codex.Field{
			Tag: "040", Ind1: ' ', Ind2: ' ',
			Subfields: []codex.Subfield{codex.NewSubfield('a', org), codex.NewSubfield('c', org)},
		})
		return
	}
	if !edited {
		return
	}
	if ds := f.SubfieldValues('d'); len(ds) > 0 && ds[len(ds)-1] == org {
		return
	}
	// $d slots into the field's canonical subfield order: after any
	// existing $a/$b/$c/$d run, before the $e description conventions.
	at := len(f.Subfields)
	for i, sf := range f.Subfields {
		if sf.Code == 'e' {
			at = i
			break
		}
	}
	sfs := append([]codex.Subfield(nil), f.Subfields[:at]...)
	sfs = append(sfs, codex.NewSubfield('d', org))
	sfs = append(sfs, f.Subfields[at:]...)
	f.Subfields = sfs
	rec.ReplaceField(f)
}
