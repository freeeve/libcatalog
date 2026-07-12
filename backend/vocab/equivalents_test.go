package vocab

import (
	"strings"
	"testing"

	"github.com/freeeve/libcat/storage/blob"
)

// equivFixture loads a three-scheme bridge: a Homosaurus term H exact-links an
// LCSH term L (loaded), a FAST term F close-links the same L, and L
// exact-links an external URI X that no vocabulary loads.
func equivFixture(t *testing.T) *Index {
	t.Helper()
	const (
		H = "https://homosaurus.org/v5/homoit0009999"
		L = "http://id.loc.gov/authorities/subjects/sh99009999"
		F = "http://id.worldcat.org/fast/9999999"
		X = "https://www.wikidata.org/entity/Q9999999"
	)
	nq := strings.NewReplacer("{H}", H, "{L}", L, "{F}", F, "{X}", X).Replace(`
<{H}> <http://www.w3.org/2004/02/skos/core#prefLabel> "Sapphic poets"@en <authority:homosaurus> .
<{H}> <http://www.w3.org/2004/02/skos/core#exactMatch> <{L}> <authority:homosaurus> .
<{F}> <http://www.w3.org/2004/02/skos/core#prefLabel> "Lesbian poets"@en <authority:fast> .
<{F}> <http://www.w3.org/2004/02/skos/core#closeMatch> <{L}> <authority:fast> .
<{L}> <http://www.w3.org/2004/02/skos/core#prefLabel> "Lesbian poets (LCSH)"@en <authority:lcsh> .
<{L}> <http://www.w3.org/2004/02/skos/core#exactMatch> <{X}> <authority:lcsh> .
`)
	st := blob.NewMem()
	if _, err := st.Put(t.Context(), "data/authorities/eq/vocab.nq", []byte(nq), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	ix, err := Load(t.Context(), st, "data/authorities/", nil)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

// eq finds an equivalent by URI in a result set.
func eq(list []Equivalent, id string) *Equivalent {
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}

// TestEquivalentsBridges is the seam: outbound, inbound, and all
// three pivot shapes, each labeled with the weakest hop's strength.
func TestEquivalentsBridges(t *testing.T) {
	ix := equivFixture(t)
	const (
		H = "https://homosaurus.org/v5/homoit0009999"
		L = "http://id.loc.gov/authorities/subjects/sh99009999"
		F = "http://id.worldcat.org/fast/9999999"
		X = "https://www.wikidata.org/entity/Q9999999"
	)

	// From the Homosaurus term: L direct (outbound exact), F via the shared-L
	// pivot (exact+close -> pivot-close), X via L's onward link (exact+exact
	// -> pivot-exact).
	got, ok := ix.Equivalents(H)
	if !ok {
		t.Fatal("H should resolve")
	}
	if l := eq(got, L); l == nil || l.Strength != "exact" || !l.Known || l.Scheme != "lcsh" {
		t.Errorf("L = %+v, want direct exact, known, lcsh", l)
	}
	if f := eq(got, F); f == nil || f.Strength != "pivot-close" || f.Via != L {
		t.Errorf("F = %+v, want pivot-close via L", f)
	}
	if x := eq(got, X); x == nil || x.Strength != "pivot-exact" || x.Via != L || x.Known {
		t.Errorf("X = %+v, want pivot-exact via L, not Known", x)
	}
	if s := eq(got, H); s != nil {
		t.Errorf("the source term must be excluded, got %+v", s)
	}

	// From the LCSH term: H and F are INBOUND directs (the load-bearing
	// direction: community vocabularies link TO LCSH), X outbound.
	got, ok = ix.Equivalents(L)
	if !ok {
		t.Fatal("L should resolve")
	}
	if h := eq(got, H); h == nil || h.Strength != "exact" || h.Scheme != "homosaurus" {
		t.Errorf("H = %+v, want inbound exact", h)
	}
	if f := eq(got, F); f == nil || f.Strength != "close" {
		t.Errorf("F = %+v, want inbound close", f)
	}

	// From the FAST term: L direct close; H via shared-L pivot; sorted with
	// the strongest first.
	got, ok = ix.Equivalents(F)
	if !ok {
		t.Fatal("F should resolve")
	}
	if l := eq(got, L); l == nil || l.Strength != "close" {
		t.Errorf("L = %+v, want direct close", l)
	}
	if h := eq(got, H); h == nil || h.Strength != "pivot-close" || h.Via != L {
		t.Errorf("H = %+v, want pivot-close via L", h)
	}
	if len(got) > 1 && strengthRank(got[0].Strength) < strengthRank(got[len(got)-1].Strength) {
		t.Errorf("results not strength-sorted: %+v", got)
	}

	// An unknown URI is not a term: no equivalents, not an empty success.
	if _, ok := ix.Equivalents("https://example.org/nope"); ok {
		t.Error("unknown URI should not resolve")
	}
}

// TestEquivalentsSidecarInbound: a sidecar-backed scheme contributes inbound
// equivalents through its reverse-match artifact, and an artifact set without
// one (pre-artifact builds) still arms -- minus inbound.
func TestEquivalentsSidecarInbound(t *testing.T) {
	const (
		H = "https://homosaurus.org/v5/homoit0008888"
		L = "http://id.loc.gov/authorities/subjects/sh88008888"
	)
	hs := strings.NewReplacer("{H}", H, "{L}", L).Replace(`
<{H}> <http://www.w3.org/2004/02/skos/core#prefLabel> "Queer zines"@en <authority:homosaurus> .
<{H}> <http://www.w3.org/2004/02/skos/core#exactMatch> <{L}> <authority:homosaurus> .
`)
	lc := strings.NewReplacer("{L}", L).Replace(`
<{L}> <http://www.w3.org/2004/02/skos/core#prefLabel> "Zines"@en <authority:lcsh> .
`)
	st := blob.NewMem()
	hsSource := "data/authorities/vocab/hs.nq"
	if _, err := st.Put(t.Context(), hsSource, []byte(hs), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Put(t.Context(), "data/authorities/vocab/lcsh.nq", []byte(lc), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildSidecar(t.Context(), st, "data/authorities/", "homosaurus", hsSource); err != nil {
		t.Fatal(err)
	}
	ix, err := Load(t.Context(), st, "data/authorities/", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Sanity: homosaurus must actually be sidecar-served (its own armed source).
	if _, ok := ix.load().sidecar["homosaurus"]; !ok {
		t.Fatal("homosaurus is not sidecar-armed; the test would prove nothing")
	}

	// Inbound: the map-backed LCSH term surfaces its sidecar-backed linker.
	got, ok := ix.Equivalents(L)
	if !ok {
		t.Fatal("L should resolve")
	}
	h := eq(got, H)
	if h == nil || h.Strength != "exact" || h.Scheme != "homosaurus" || !h.Known {
		t.Fatalf("sidecar inbound = %+v, want exact/homosaurus/known", h)
	}

	// Pre-artifact set: drop the reverse artifact, reload -- the scheme still
	// arms, equivalents just lose the sidecar inbound.
	if err := st.Delete(t.Context(), "data/authorities/sidecar/homosaurus.rev.json.gz"); err != nil {
		t.Fatal(err)
	}
	ix2, err := Load(t.Context(), st, "data/authorities/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ix2.load().sidecar["homosaurus"]; !ok {
		t.Fatal("scheme must still arm without the reverse artifact")
	}
	got2, _ := ix2.Equivalents(L)
	if eq(got2, H) != nil {
		t.Errorf("without the artifact, sidecar inbound should be absent: %+v", got2)
	}
	// Outbound from the sidecar term is unaffected either way.
	out2, ok := ix2.Equivalents(H)
	if !ok || eq(out2, L) == nil || eq(out2, L).Strength != "exact" {
		t.Errorf("sidecar outbound degraded: %+v", out2)
	}
}

// pivotGuardFixture builds the task-420 over-reach corpus: FAST terms
// linking broad LCSH nodes that homosaurus terms of varying specificity
// also link. LCSH itself is NOT loaded -- the nodes are bare pivot URIs.
func pivotGuardFixture(t *testing.T) *Index {
	t.Helper()
	const nq = `
<urn:fast:women> <http://www.w3.org/2004/02/skos/core#prefLabel> "Women"@en <authority:fast> .
<urn:fast:women> <http://www.w3.org/2004/02/skos/core#exactMatch> <urn:lcsh:women> <authority:fast> .
<urn:homo:women> <http://www.w3.org/2004/02/skos/core#prefLabel> "Women"@en <authority:homosaurus> .
<urn:homo:women> <http://www.w3.org/2004/02/skos/core#exactMatch> <urn:lcsh:women> <authority:homosaurus> .
<urn:homo:womyn> <http://www.w3.org/2004/02/skos/core#prefLabel> "Womyn"@en <authority:homosaurus> .
<urn:homo:womyn> <http://www.w3.org/2004/02/skos/core#exactMatch> <urn:lcsh:women> <authority:homosaurus> .
<urn:homo:womyn> <http://www.w3.org/2004/02/skos/core#broader> <urn:homo:women> <authority:homosaurus> .
<urn:fast:minorities> <http://www.w3.org/2004/02/skos/core#prefLabel> "Minorities"@en <authority:fast> .
<urn:fast:minorities> <http://www.w3.org/2004/02/skos/core#exactMatch> <urn:lcsh:minorities> <authority:fast> .
<urn:homo:sexmin> <http://www.w3.org/2004/02/skos/core#prefLabel> "Sexual minorities"@en <authority:homosaurus> .
<urn:homo:sexmin> <http://www.w3.org/2004/02/skos/core#exactMatch> <urn:lcsh:minorities> <authority:homosaurus> .
<urn:homo:lgbtq> <http://www.w3.org/2004/02/skos/core#prefLabel> "LGBTQ+ people"@en <authority:homosaurus> .
<urn:homo:lgbtq> <http://www.w3.org/2004/02/skos/core#exactMatch> <urn:lcsh:minorities> <authority:homosaurus> .
<urn:fast:ssm> <http://www.w3.org/2004/02/skos/core#prefLabel> "Same-sex marriage"@en <authority:fast> .
<urn:fast:ssm> <http://www.w3.org/2004/02/skos/core#exactMatch> <urn:lcsh:ssm> <authority:fast> .
<urn:homo:ssm> <http://www.w3.org/2004/02/skos/core#prefLabel> "Same-sex marriage"@en <authority:homosaurus> .
<urn:homo:ssm> <http://www.w3.org/2004/02/skos/core#exactMatch> <urn:lcsh:ssm> <authority:homosaurus> .
<urn:homo:lescouples> <http://www.w3.org/2004/02/skos/core#prefLabel> "Lesbian couples"@en <authority:homosaurus> .
<urn:homo:lescouples> <http://www.w3.org/2004/02/skos/core#exactMatch> <urn:lcsh:ssm> <authority:homosaurus> .
<urn:gnd:minderheiten> <http://www.w3.org/2004/02/skos/core#prefLabel> "Minderheiten"@de <authority:gnd> .
<urn:gnd:minderheiten> <http://www.w3.org/2004/02/skos/core#exactMatch> <urn:lcsh:minorities> <authority:gnd> .
<urn:fast:girls> <http://www.w3.org/2004/02/skos/core#prefLabel> "Girls"@en <authority:fast> .
<urn:fast:girls> <http://www.w3.org/2004/02/skos/core#exactMatch> <urn:lcsh:girls> <authority:fast> .
<urn:homo:grrls> <http://www.w3.org/2004/02/skos/core#prefLabel> "Grrls"@en <authority:homosaurus> .
<urn:homo:grrls> <http://www.w3.org/2004/02/skos/core#exactMatch> <urn:lcsh:girls> <authority:homosaurus> .
<urn:homo:girls> <http://www.w3.org/2004/02/skos/core#prefLabel> "Girls"@en <authority:homosaurus> .
<urn:homo:girls> <http://www.w3.org/2004/02/skos/core#exactMatch> <urn:lcsh:girls-elsewhere> <authority:homosaurus> .
<urn:fast:masculinity> <http://www.w3.org/2004/02/skos/core#prefLabel> "Masculinity"@en <authority:fast> .
<urn:fast:masculinity> <http://www.w3.org/2004/02/skos/core#exactMatch> <urn:lcsh:masc> <authority:fast> .
<urn:homo:masculinities> <http://www.w3.org/2004/02/skos/core#prefLabel> "Masculinities"@en <authority:homosaurus> .
<urn:homo:masculinities> <http://www.w3.org/2004/02/skos/core#exactMatch> <urn:lcsh:masc> <authority:homosaurus> .
`
	st := blob.NewMem()
	if _, err := st.Put(t.Context(), "data/authorities/pg/vocab.nq", []byte(nq), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	ix, err := Load(t.Context(), st, "data/authorities/", nil)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

// strengthOf finds an equivalent's strength, "" when absent.
func strengthOf(eqs []Equivalent, id string) string {
	for _, e := range eqs {
		if e.ID == id {
			return e.Strength
		}
	}
	return ""
}

// TestPivotGuards pins task 420's precision rules on its reported cases:
// the subtree drop (Women never suggests Womyn), the hub demotion
// (Minorities' claimants fall to pivot-close), the label-match exemption
// (Same-sex marriage keeps its counterpart at full strength while the
// sibling demotes), and the plural-tolerant keeper (Masculinity ->
// Masculinities stays pivot-exact).
func TestPivotGuards(t *testing.T) {
	ix := pivotGuardFixture(t)

	women, ok := ix.Equivalents("urn:fast:women")
	if !ok {
		t.Fatal("fast Women unresolved")
	}
	if got := strengthOf(women, "urn:homo:women"); got != "pivot-exact" {
		t.Errorf("Women -> Women = %q, want pivot-exact (label-matched counterpart)", got)
	}
	if got := strengthOf(women, "urn:homo:womyn"); got != "" {
		t.Errorf("Women -> Womyn = %q, want dropped (its group sibling is its ancestor)", got)
	}

	minorities, _ := ix.Equivalents("urn:fast:minorities")
	for _, id := range []string{"urn:homo:sexmin", "urn:homo:lgbtq"} {
		if got := strengthOf(minorities, id); got != "pivot-close" {
			t.Errorf("Minorities -> %s = %q, want demoted pivot-close (hub node)", id, got)
		}
	}

	ssm, _ := ix.Equivalents("urn:fast:ssm")
	if got := strengthOf(ssm, "urn:homo:ssm"); got != "pivot-exact" {
		t.Errorf("Same-sex marriage counterpart = %q, want pivot-exact", got)
	}
	if got := strengthOf(ssm, "urn:homo:lescouples"); got != "pivot-close" {
		t.Errorf("Lesbian couples = %q, want demoted pivot-close (kept, lower)", got)
	}

	masc, _ := ix.Equivalents("urn:fast:masculinity")
	if got := strengthOf(masc, "urn:homo:masculinities"); got != "pivot-exact" {
		t.Errorf("Masculinity -> Masculinities = %q, want pivot-exact (plural-tolerant match, sole claimant)", got)
	}
}

// TestPivotGuardsReverseDirection pins task 423: with the NARROW term as
// the source, its exactMatch onto a broad hub must not equate it to the
// hub's counterparts. Homosaurus "Womyn" asserts exactMatch on LCSH
// "Women" -- a two-hop exactMatch chain that passed 420's target-side
// guard -- and a high-fan-in node demotes non-matching pivots even when
// each scheme has only one claimant.
func TestPivotGuardsReverseDirection(t *testing.T) {
	ix := pivotGuardFixture(t)

	// Source-narrow: Womyn's own broader ("Women") claims the same node,
	// so the node equates to the ancestor, not to Womyn -- no pivot
	// suggests plain "Women" (in any scheme) as Womyn's equivalent.
	womyn, ok := ix.Equivalents("urn:homo:womyn")
	if !ok {
		t.Fatal("homo Womyn unresolved")
	}
	if got := strengthOf(womyn, "urn:fast:women"); got != "" {
		t.Errorf("Womyn -> FAST Women = %q, want dropped (source is the narrow end)", got)
	}
	if got := strengthOf(womyn, "urn:homo:women"); got != "" {
		t.Errorf("Womyn -> homosaurus Women = %q, want dropped (its own broader is not its equivalent)", got)
	}

	// Cross-scheme fan-in: LCSH "Minorities" carries three claimants across
	// schemes, so GND's single non-matching claimant demotes even though
	// its scheme has no sibling there.
	minorities, _ := ix.Equivalents("urn:fast:minorities")
	if got := strengthOf(minorities, "urn:gnd:minderheiten"); got != "pivot-close" {
		t.Errorf("Minorities -> Minderheiten = %q, want demoted pivot-close (hub fan-in)", got)
	}

	// The clean 1:1 pivot is untouched by the fan-in rule (two terms on the
	// node, counting the source).
	masc, _ := ix.Equivalents("urn:fast:masculinity")
	if got := strengthOf(masc, "urn:homo:masculinities"); got != "pivot-exact" {
		t.Errorf("Masculinity -> Masculinities = %q, want pivot-exact still", got)
	}
}

// TestPivotGuardLabelCounterpart pins task 425: a two-scheme load starves
// fan-in and sibling evidence, but when the candidate's own scheme holds a
// label counterpart for the source that does NOT claim the pivot node, the
// lone divergent claimant drops -- the target vocabulary itself declines
// the equivalence the pivot asserts. (The queerbooks shape: FAST "Women"
// -> bare LCSH node <- sole claimant Homosaurus "Womyn", while Homosaurus
// "Women" links elsewhere; here as Girls/Grrls so labels stay distinct
// from the 420 fixtures.)
func TestPivotGuardLabelCounterpart(t *testing.T) {
	ix := pivotGuardFixture(t)

	girls, ok := ix.Equivalents("urn:fast:girls")
	if !ok {
		t.Fatal("fast Girls unresolved")
	}
	// Grrls is the node's SOLE claimant (no fan-in, no sibling, no
	// ancestry), yet homosaurus holds "Girls" linking elsewhere: dropped.
	if got := strengthOf(girls, "urn:homo:grrls"); got != "" {
		t.Errorf("Girls -> Grrls = %q, want dropped (the scheme's counterpart declines this node)", got)
	}

	// The counterpart CO-CLAIMING the node keeps adjacent concepts
	// reviewable one tier down (Lesbian couples, unchanged from 420).
	ssm, _ := ix.Equivalents("urn:fast:ssm")
	if got := strengthOf(ssm, "urn:homo:lescouples"); got != "pivot-close" {
		t.Errorf("Lesbian couples = %q, want demoted pivot-close still", got)
	}

	// No counterpart in the scheme -> the rule stays silent: inflection
	// counterparts must not fire it (exact-normalized lookup only)...
	masc, _ := ix.Equivalents("urn:fast:masculinity")
	if got := strengthOf(masc, "urn:homo:masculinities"); got != "pivot-exact" {
		t.Errorf("Masculinity -> Masculinities = %q, want pivot-exact untouched", got)
	}
	// ...and a genuinely counterpart-less bridge keeps its 420-demoted
	// tier rather than dropping (Minorities' claimants: reviewable noise,
	// not silently killed -- the documented residual).
	minorities, _ := ix.Equivalents("urn:fast:minorities")
	if got := strengthOf(minorities, "urn:homo:sexmin"); got != "pivot-close" {
		t.Errorf("Minorities -> Sexual minorities = %q, want pivot-close still", got)
	}
}
