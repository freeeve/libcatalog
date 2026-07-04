package editor

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/freeeve/libcatalog/bibframe"
	"github.com/freeeve/libcatalog/identity"
	"github.com/freeeve/libcatalog/ingest"
	"github.com/freeeve/libcatalog/ingest/marc"

	"github.com/freeeve/libcatalog/backend/profiles"
)

var marcSamples = []string{
	"../../ingest/overdrive/testdata/marc-express/od-sample-ebook.mrc",
	"../../ingest/overdrive/testdata/marc-express/od-sample-audiobook.mrc",
}

// realGrains ingests the vendored MARC Express samples and returns each
// grain's bytes keyed by Work id -- the golden corpus for round-trips.
func realGrains(t *testing.T) map[string][]byte {
	t.Helper()
	grains := map[string][]byte{}
	for _, sample := range marcSamples {
		dir := t.TempDir()
		prov, err := marc.New(ingest.Config{Source: sample})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ingest.Run(prov, dir); err != nil {
			t.Fatal(err)
		}
		err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".nq") || d.Name() == "catalog.nq" {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			gi, err := identity.ScanGrain(data)
			if err != nil {
				return err
			}
			for _, w := range gi.Works {
				grains[w.WorkID] = data
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(grains) == 0 {
		t.Fatal("no grains")
	}
	return grains
}

func newMapper(t *testing.T) *Mapper {
	t.Helper()
	set, err := profiles.LoadDefaults()
	if err != nil {
		t.Fatal(err)
	}
	return &Mapper{WorkProfile: set["work-monograph"], InstanceProfile: set["instance-ebook"]}
}

// TestGoldenRoundTrip proves the decomposition is lossless over every real
// grain: grain -> doc -> grain is byte-identical when nothing is edited.
func TestGoldenRoundTrip(t *testing.T) {
	m := newMapper(t)
	for workID, grain := range realGrains(t) {
		doc, err := m.ToDoc(grain, workID)
		if err != nil {
			t.Fatalf("%s: ToDoc: %v", workID, err)
		}
		back, err := m.ToGrain(doc)
		if err != nil {
			t.Fatalf("%s: ToGrain: %v", workID, err)
		}
		if !bytes.Equal(back, grain) {
			t.Fatalf("%s: round-trip diverged\n--- original\n%s\n--- rebuilt\n%s", workID, grain, back)
		}
	}
}

func TestFieldExtraction(t *testing.T) {
	m := newMapper(t)
	grains := realGrains(t)
	var checked int
	for workID, grain := range grains {
		doc, err := m.ToDoc(grain, workID)
		if err != nil {
			t.Fatal(err)
		}
		titles := doc.Work.Fields["title"]
		if len(titles) == 0 {
			continue
		}
		checked++
		if titles[0].V == "" || titles[0].Prov != "feed:marc" || titles[0].IRI {
			t.Fatalf("%s: title = %+v", workID, titles[0])
		}
		// The title link (work -> title node) is structure, preserved in
		// passthrough, not claimed.
		var linkPreserved bool
		for _, line := range doc.Passthrough {
			if strings.Contains(line, "bibframe/title") && strings.Contains(line, titles[0].Node) {
				linkPreserved = true
			}
		}
		if !linkPreserved {
			t.Fatalf("%s: title link quad missing from passthrough", workID)
		}
		// Instances carry identifier values.
		if len(doc.Instances) == 0 {
			t.Fatalf("%s: no instances", workID)
		}
	}
	if checked == 0 {
		t.Fatal("no grains had titles")
	}
}

// TestStructuredFieldsClaimed proves the tasks/083 additions surface values
// living inside blank structures: the 3-hop contributor chain and the
// 2-hop label chains (subject headings, notes, extent, publication) that
// used to hide in passthrough.
func TestStructuredFieldsClaimed(t *testing.T) {
	m := newMapper(t)
	found := map[string]bool{}
	for workID, grain := range realGrains(t) {
		doc, err := m.ToDoc(grain, workID)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"contributors", "subjectLabels"} {
			for _, v := range doc.Work.Fields[path] {
				if v.V != "" && strings.HasPrefix(v.Prov, "feed:") {
					found[path] = true
				}
			}
		}
		for _, inst := range doc.Instances {
			for _, path := range []string{"links", "notes", "extent", "publisher"} {
				for _, v := range inst.Fields[path] {
					if v.V != "" {
						found[path] = true
					}
				}
			}
		}
	}
	for _, path := range []string{"contributors", "subjectLabels", "links", "notes", "extent", "publisher"} {
		if !found[path] {
			t.Errorf("no grain surfaced %q", path)
		}
	}
}

// TestAnnotationResolved proves a field's annotation chain (the heading's
// bf:source label, MARC $2) rides along on each value, display-only.
func TestAnnotationResolved(t *testing.T) {
	m := newMapper(t)
	var found bool
	for workID, grain := range realGrains(t) {
		doc, err := m.ToDoc(grain, workID)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range doc.Work.Fields["subjectLabels"] {
			if v.Annotation == "OverDrive" {
				found = true
			}
		}
		// Display-only: the annotation's quads stay in passthrough and the
		// round trip stays byte-identical (TestGoldenRoundTrip); here just
		// confirm the source label is still a passthrough statement.
		if found {
			var inPassthrough bool
			for _, line := range doc.Passthrough {
				if strings.Contains(line, `"OverDrive"`) {
					inPassthrough = true
				}
			}
			if !inPassthrough {
				t.Fatal("annotation source quads were claimed out of passthrough")
			}
			return
		}
	}
	t.Fatal("no subject heading carried the OverDrive source annotation")
}

// TestEditedValueLandsOnNode proves a doc edit renders back onto the right
// node: changing a title changes exactly that literal in the grain.
func TestEditedValueLandsOnNode(t *testing.T) {
	m := newMapper(t)
	for workID, grain := range realGrains(t) {
		doc, err := m.ToDoc(grain, workID)
		if err != nil {
			t.Fatal(err)
		}
		titles := doc.Work.Fields["title"]
		if len(titles) == 0 {
			continue
		}
		original := titles[0].V
		titles[0].V = "Edited Title For Test"
		doc.Work.Fields["title"] = titles
		back, err := m.ToGrain(doc)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(back), "Edited Title For Test") {
			t.Fatal("edit missing from rebuilt grain")
		}
		// The edit is scoped to the Work's title field: re-materializing
		// shows the new value and not the old one there. (The old string
		// legitimately survives elsewhere in the grain -- the Instance's
		// own title node and the crosswalk's rdfs:label mirror are
		// unclaimed structure; syncing such paired display quads on edit
		// is the write path's job, tasks/045.)
		doc2, err := m.ToDoc(back, workID)
		if err != nil {
			t.Fatal(err)
		}
		var haveEdited, haveOriginal bool
		for _, v := range doc2.Work.Fields["title"] {
			haveEdited = haveEdited || v.V == "Edited Title For Test"
			haveOriginal = haveOriginal || v.V == original
		}
		if !haveEdited || haveOriginal {
			t.Fatalf("re-materialized title = %+v", doc2.Work.Fields["title"])
		}
		return // one grain suffices
	}
	t.Fatal("no grain with a title")
}

// TestOverriddenFlag proves feed values under an editorial lcat:overrides
// marker come back flagged for the hover-reveal / revert affordance.
func TestOverriddenFlag(t *testing.T) {
	m := newMapper(t)
	for workID, grain := range realGrains(t) {
		doc, err := m.ToDoc(grain, workID)
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Work.Fields["subjects"]) == 0 {
			continue
		}
		// Claim bf:subject editorially.
		patch := bibframe.OverridePatch(bibframe.WorkIRI(workID),
			"http://id.loc.gov/ontologies/bibframe/subject")
		patch.Add = append(patch.Add, bibframe.SubjectQuad(workID, "https://homosaurus.org/v4/x"))
		claimed, err := bibframe.ApplyEditorialPatch(grain, patch)
		if err != nil {
			t.Fatal(err)
		}
		doc2, err := m.ToDoc(claimed, workID)
		if err != nil {
			t.Fatal(err)
		}
		var feedFlagged, editorialFlagged bool
		for _, v := range doc2.Work.Fields["subjects"] {
			switch {
			case strings.HasPrefix(v.Prov, "feed:"):
				feedFlagged = feedFlagged || v.Overridden
				if !v.Overridden {
					t.Fatalf("feed subject not flagged: %+v", v)
				}
			case v.Prov == "editorial:":
				editorialFlagged = editorialFlagged || v.Overridden
			}
		}
		if !feedFlagged {
			t.Fatal("no feed subject flagged overridden")
		}
		if editorialFlagged {
			t.Fatal("editorial value flagged overridden")
		}
		return
	}
	t.Skip("no grain with feed subjects")
}
