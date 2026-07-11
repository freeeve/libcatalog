package suggest

import (
	"errors"
	"os"
	"testing"

	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/vocab"
	"github.com/freeeve/libcat/storage/blob"
)

// newRawService builds a service with the default (off) policy -- unlike
// newService, which opens the intake for the Submit-exercising tests.
func newRawService(t *testing.T) *Service {
	t.Helper()
	data, err := os.ReadFile("../vocab/testdata/authorities.nq")
	if err != nil {
		t.Fatal(err)
	}
	bs := blob.NewMem()
	if _, err := bs.Put(t.Context(), "data/authorities/x.nq", data, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	ix, err := vocab.Load(t.Context(), bs, "data/authorities/", nil)
	if err != nil {
		t.Fatal(err)
	}
	return New(store.NewMem(), ix, Caps{})
}

func submitErr(t *testing.T, svc *Service, term vocab.TermRef) error {
	t.Helper()
	_, err := svc.Submit(t.Context(), SubmitInput{
		WorkID: "wabc123def456", Term: term, Type: TypeAdd,
		SupporterHash: "h" + term.ID, WorkTitle: "A Book",
	})
	return err
}

// TestDefaultPolicyRefusesEverything pins the opt-in default: a fresh store
// accepts no patron suggestion, controlled or folk, until the policy enables it.
func TestDefaultPolicyRefusesEverything(t *testing.T) {
	svc := newRawService(t)
	if got := svc.GetPolicyOrZero(t); got.Enabled {
		t.Fatalf("fresh policy enabled = true, want false")
	}
	if err := submitErr(t, svc, controlled(transURI)); !errors.Is(err, ErrSuggestionsOff) {
		t.Errorf("controlled on default policy: %v, want ErrSuggestionsOff", err)
	}
	if err := submitErr(t, svc, folk("cozy fantasy")); !errors.Is(err, ErrSuggestionsOff) {
		t.Errorf("folk on default policy: %v, want ErrSuggestionsOff", err)
	}
}

// TestSchemeAllowlist: an empty allowlist admits every loaded scheme, a
// non-empty one refuses schemes outside it while still accepting members. The
// "fast" scheme is not loaded, so under an empty allowlist it passes the policy
// and is rejected by the vocabulary (ErrBadTerm) -- proving the allowlist let it
// through -- while a homosaurus-only allowlist refuses it earlier
// (ErrSchemeNotAllowed), before the vocabulary is consulted.
func TestSchemeAllowlist(t *testing.T) {
	fast := vocab.TermRef{Scheme: "fast", ID: sciFiURI}

	empty := newRawService(t)
	mustPutPolicy(t, empty, Policy{Enabled: true, FreeText: FreeTextOff})
	if err := submitErr(t, empty, controlled(transURI)); err != nil {
		t.Errorf("homosaurus with empty allowlist: %v, want accepted", err)
	}
	if err := submitErr(t, empty, fast); !errors.Is(err, ErrBadTerm) {
		t.Errorf("fast with empty allowlist: %v, want ErrBadTerm (allowlist admitted the scheme)", err)
	}

	restricted := newRawService(t)
	mustPutPolicy(t, restricted, Policy{Enabled: true, Schemes: []string{"homosaurus"}, FreeText: FreeTextOff})
	if err := submitErr(t, restricted, controlled(transURI)); err != nil {
		t.Errorf("homosaurus in allowlist: %v, want accepted", err)
	}
	if err := submitErr(t, restricted, fast); !errors.Is(err, ErrSchemeNotAllowed) {
		t.Errorf("fast outside allowlist: %v, want ErrSchemeNotAllowed", err)
	}
}

// TestFreeTextModes: off refuses folk entirely, existing refuses a novel tag
// but keeps one already in use, any accepts both.
func TestFreeTextModes(t *testing.T) {
	off := newRawService(t)
	mustPutPolicy(t, off, Policy{Enabled: true, FreeText: FreeTextOff})
	if err := submitErr(t, off, folk("cozy fantasy")); !errors.Is(err, ErrFreeTextOff) {
		t.Errorf("folk with free-text off: %v, want ErrFreeTextOff", err)
	}

	any := newRawService(t)
	mustPutPolicy(t, any, Policy{Enabled: true, FreeText: FreeTextAny})
	if err := submitErr(t, any, folk("cozy fantasy")); err != nil {
		t.Errorf("novel folk with free-text any: %v, want accepted", err)
	}

	existing := newRawService(t)
	// Create the folk term first under an open policy, then narrow to existing.
	mustPutPolicy(t, existing, Policy{Enabled: true, FreeText: FreeTextAny})
	if err := submitErr(t, existing, folk("cozy fantasy")); err != nil {
		t.Fatalf("seed existing folk: %v", err)
	}
	mustPutPolicy(t, existing, Policy{Enabled: true, FreeText: FreeTextExisting})
	if err := submitErr(t, existing, folk("cozy fantasy")); err != nil {
		t.Errorf("existing folk with free-text existing: %v, want accepted", err)
	}
	if err := submitErr(t, existing, folk("brand new tag")); !errors.Is(err, ErrNovelTagOff) {
		t.Errorf("novel folk with free-text existing: %v, want ErrNovelTagOff", err)
	}
}

// TestManualTermNotGated: the librarian path adds any term even when the patron
// policy is off -- the cataloger is the authority (tasks/263).
func TestManualTermNotGated(t *testing.T) {
	svc := newRawService(t)
	// Default (off) policy: patrons are refused, but ManualTerm must succeed.
	if err := submitErr(t, svc, controlled(transURI)); !errors.Is(err, ErrSuggestionsOff) {
		t.Fatalf("precondition: patron submit not refused: %v", err)
	}
	if err := svc.ManualTerm(t.Context(), "wabc123def456", controlled(transURI), "A Book", "lib@example.org"); err != nil {
		t.Errorf("ManualTerm on default policy: %v, want accepted", err)
	}
	if err := svc.ManualTerm(t.Context(), "wabc123def456", folk("cataloger tag"), "A Book", "lib@example.org"); err != nil {
		t.Errorf("ManualTerm folk on default policy: %v, want accepted", err)
	}
}

// TestPutPolicyValidatesFreeText: an unknown free-text mode is rejected; an
// unset one defaults to off and empty scheme entries are dropped.
func TestPutPolicyValidatesFreeText(t *testing.T) {
	svc := newRawService(t)
	if _, err := svc.PutPolicy(t.Context(), Policy{Enabled: true, FreeText: "sometimes"}); !errors.Is(err, ErrBadPolicy) {
		t.Errorf("bad free-text mode: %v, want ErrBadPolicy", err)
	}
	saved, err := svc.PutPolicy(t.Context(), Policy{Enabled: true, Schemes: []string{"homosaurus", "", "homosaurus"}})
	if err != nil {
		t.Fatalf("PutPolicy: %v", err)
	}
	if saved.FreeText != FreeTextOff {
		t.Errorf("unset free-text normalized to %q, want off", saved.FreeText)
	}
	if len(saved.Schemes) != 1 || saved.Schemes[0] != "homosaurus" {
		t.Errorf("schemes normalized to %v, want [homosaurus]", saved.Schemes)
	}
}

func mustPutPolicy(t *testing.T, svc *Service, p Policy) {
	t.Helper()
	if _, err := svc.PutPolicy(t.Context(), p); err != nil {
		t.Fatalf("PutPolicy: %v", err)
	}
}

// GetPolicyOrZero is a tiny test helper: the policy, or a zero value on error.
func (s *Service) GetPolicyOrZero(t *testing.T) Policy {
	t.Helper()
	p, err := s.GetPolicy(t.Context())
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	return p
}
