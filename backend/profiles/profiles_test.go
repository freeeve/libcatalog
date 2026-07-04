package profiles

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShippedDefaults(t *testing.T) {
	set, err := LoadDefaults()
	if err != nil {
		t.Fatalf("shipped defaults invalid: %v", err)
	}
	for _, id := range []string{"work-monograph", "instance-ebook", "fastadd", "authority-topic"} {
		if set[id] == nil {
			t.Fatalf("missing default profile %s", id)
		}
	}
	if got := set.ForResource(ResourceWork); len(got) != 2 || got[0].ID != "fastadd" {
		t.Fatalf("work profiles = %+v", got)
	}
}

func TestLoadDirOverrides(t *testing.T) {
	base, err := LoadDefaults()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// Override fastadd with a variant; add a new item profile.
	override := `{"id":"fastadd","label":"Fast add (local)","resourceType":"work",
	  "fields":[{"path":"title","predicates":["http://id.loc.gov/ontologies/bibframe/title","http://id.loc.gov/ontologies/bibframe/mainTitle"],
	    "label":"Title","min":1,"max":1,"valueSource":{"kind":"literal"}}]}`
	item := `{"id":"item-basic","label":"Item","resourceType":"item",
	  "fields":[{"path":"callNumber","predicates":["http://id.loc.gov/ontologies/bibframe/shelfMark"],
	    "label":"Call number","valueSource":{"kind":"literal"}}]}`
	_ = os.WriteFile(filepath.Join(dir, "fastadd.json"), []byte(override), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "item.json"), []byte(item), 0o644)
	set, err := LoadDir(base, dir)
	if err != nil {
		t.Fatal(err)
	}
	if set["fastadd"].Label != "Fast add (local)" {
		t.Fatalf("override not applied: %+v", set["fastadd"])
	}
	if set["item-basic"] == nil || set["work-monograph"] == nil {
		t.Fatal("addition or base profile missing")
	}
	// A broken override fails the whole load (framework-test behavior).
	_ = os.WriteFile(filepath.Join(dir, "broken.json"), []byte(`{"id":"broken","label":"B","resourceType":"work","fields":[]}`), 0o644)
	if _, err := LoadDir(base, dir); err == nil {
		t.Fatal("broken profile accepted")
	}
}

func TestValidateRejects(t *testing.T) {
	valid := func() Profile {
		return Profile{
			ID: "p", Label: "P", ResourceType: ResourceWork,
			Fields: []Field{{
				Path:        "x",
				Predicates:  []string{"http://id.loc.gov/ontologies/bibframe/title"},
				Label:       "X",
				ValueSource: ValueSource{Kind: KindLiteral},
			}},
		}
	}
	if err := (func() error { p := valid(); return p.Validate() })(); err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}
	cases := map[string]func(*Profile){
		"unknown vocabulary": func(p *Profile) { p.Fields[0].Predicates = []string{"http://evil.example/x"} },
		"chain too long": func(p *Profile) {
			p.Fields[0].Predicates = []string{
				"http://id.loc.gov/ontologies/bibframe/a",
				"http://id.loc.gov/ontologies/bibframe/b",
				"http://id.loc.gov/ontologies/bibframe/c",
				"http://id.loc.gov/ontologies/bibframe/d",
			}
		},
		"editable 3-chain": func(p *Profile) {
			p.Fields[0].Predicates = []string{
				"http://id.loc.gov/ontologies/bibframe/a",
				"http://id.loc.gov/ontologies/bibframe/b",
				"http://id.loc.gov/ontologies/bibframe/c",
			}
		},
		"unknown kind":         func(p *Profile) { p.Fields[0].ValueSource.Kind = "magic" },
		"enum without options": func(p *Profile) { p.Fields[0].ValueSource.Kind = KindEnum },
		"vocab without ref":    func(p *Profile) { p.Fields[0].ValueSource.Kind = KindVocab },
		"bad date default": func(p *Profile) {
			p.Fields[0].ValueSource.Kind = KindDate
			p.Fields[0].Default = "someday"
		},
		"enum default outside options": func(p *Profile) {
			p.Fields[0].ValueSource = ValueSource{Kind: KindEnum, Options: []string{"a", "b"}}
			p.Fields[0].Default = "c"
		},
		"min over max": func(p *Profile) { p.Fields[0].Min, p.Fields[0].Max = 2, 1 },
		"duplicate path": func(p *Profile) {
			p.Fields = append(p.Fields, p.Fields[0])
		},
		"bad resource type": func(p *Profile) { p.ResourceType = "shelf" },
		"annotation on direct field": func(p *Profile) {
			p.Fields[0].Annotation = []string{"http://id.loc.gov/ontologies/bibframe/source"}
		},
		"annotation outside known vocabularies": func(p *Profile) {
			p.Fields[0].Predicates = append(p.Fields[0].Predicates, "http://www.w3.org/2000/01/rdf-schema#label")
			p.Fields[0].Annotation = []string{"http://evil.example/x"}
		},
	}
	for name, mutate := range cases {
		p := valid()
		mutate(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Good defaults pass.
	p := valid()
	p.Fields[0].ValueSource = ValueSource{Kind: KindDate}
	p.Fields[0].Default = "2026-07-02"
	if err := p.Validate(); err != nil {
		t.Fatalf("date default rejected: %v", err)
	}
	// A read-only 3-chain (contribution -> agent -> label) is valid.
	p = valid()
	p.Fields[0].Predicates = []string{
		"http://id.loc.gov/ontologies/bibframe/contribution",
		"http://id.loc.gov/ontologies/bibframe/agent",
		"http://www.w3.org/2000/01/rdf-schema#label",
	}
	p.Fields[0].ReadOnly = true
	if err := p.Validate(); err != nil {
		t.Fatalf("read-only 3-chain rejected: %v", err)
	}
}
