package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/vocab"
	"github.com/freeeve/libcat/storage/blob"
)

const transURI = "https://homosaurus.org/v4/homoit0001235"

// newSuggestAPI builds a handler with the suggestion surface over MemStore
// and the vocab fixture, with a controllable clock for challenge aging.
func newSuggestAPI(t *testing.T) (http.Handler, *suggest.Abuse, func(time.Duration)) {
	t.Helper()
	data, err := os.ReadFile("../vocab/testdata/authorities.nq")
	if err != nil {
		t.Fatal(err)
	}
	bs := blob.NewMem()
	if _, err := bs.Put(t.Context(), "a/x.nq", data, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	ix, err := vocab.Load(t.Context(), bs, "a/", nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := suggest.New(store.NewMem(), ix, suggest.Caps{})
	// Open the patron intake (tasks/263); default is off. Disabled/allowlist
	// behavior is covered in suggestpolicy_handlers_test.go.
	if _, err := svc.PutPolicy(t.Context(), suggest.Policy{Enabled: true, FreeText: suggest.FreeTextAny}); err != nil {
		t.Fatal(err)
	}
	abuse, err := suggest.NewAbuse([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	abuse.SetClock(func() time.Time { return now })
	advance := func(d time.Duration) { now = now.Add(d) }
	return New(Deps{Suggest: svc, Abuse: abuse, Vocab: ix}), abuse, advance
}

func validBody(challenge string) map[string]any {
	return map[string]any{
		"workId":    "wabc123def456",
		"term":      map[string]string{"scheme": "homosaurus", "id": transURI},
		"type":      "ADD",
		"challenge": challenge,
		"website":   "",
	}
}

func TestSuggestionFlow(t *testing.T) {
	h, abuse, advance := newSuggestAPI(t)

	// Fetch a challenge, age it past the bot floor.
	rec := doJSON(t, h, http.MethodGet, "/v1/challenge", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("challenge: %d", rec.Code)
	}
	var ch struct{ Challenge string }
	_ = json.Unmarshal(rec.Body.Bytes(), &ch)
	advance(10 * time.Second)

	// Submit.
	rec = doJSON(t, h, http.MethodPost, "/v1/suggestions", "", validBody(ch.Challenge))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("submit: %d %s", rec.Code, rec.Body)
	}

	// Public counts.
	rec = doJSON(t, h, http.MethodGet, "/v1/works/wabc123def456/suggestions", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("counts: %d", rec.Code)
	}
	var out struct {
		Suggestions []struct {
			Term           vocab.TermRef `json:"term"`
			SupporterCount int           `json:"supporterCount"`
		} `json:"suggestions"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Suggestions) != 1 || out.Suggestions[0].SupporterCount != 1 || out.Suggestions[0].Term.ID != transURI {
		t.Fatalf("public view = %s", rec.Body)
	}
	// No supporter hashes or reviewer fields leak.
	if body := rec.Body.String(); containsAny(body, "supporterHash", "reviewedBy", "hash") {
		t.Fatalf("leaky public payload: %s", body)
	}

	// Instant (bot-speed) challenge rejected.
	rec = doJSON(t, h, http.MethodGet, "/v1/challenge", "", nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &ch)
	rec = doJSON(t, h, http.MethodPost, "/v1/suggestions", "", validBody(ch.Challenge))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("instant challenge: %d", rec.Code)
	}

	_ = abuse
}

func TestSuggestionHoneypot(t *testing.T) {
	h, _, advance := newSuggestAPI(t)
	rec := doJSON(t, h, http.MethodGet, "/v1/challenge", "", nil)
	var ch struct{ Challenge string }
	_ = json.Unmarshal(rec.Body.Bytes(), &ch)
	advance(10 * time.Second)
	body := validBody(ch.Challenge)
	body["website"] = "https://spam.example"
	rec = doJSON(t, h, http.MethodPost, "/v1/suggestions", "", body)
	// Indistinguishable success...
	if rec.Code != http.StatusAccepted {
		t.Fatalf("honeypot: %d", rec.Code)
	}
	// ...but nothing stored.
	rec = doJSON(t, h, http.MethodGet, "/v1/works/wabc123def456/suggestions", "", nil)
	var out struct {
		Suggestions []any `json:"suggestions"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Suggestions) != 0 {
		t.Fatalf("honeypot wrote: %s", rec.Body)
	}
}

func TestSuggestionFieldGating(t *testing.T) {
	h, _, advance := newSuggestAPI(t)
	rec := doJSON(t, h, http.MethodGet, "/v1/challenge", "", nil)
	var ch struct{ Challenge string }
	_ = json.Unmarshal(rec.Body.Bytes(), &ch)
	advance(10 * time.Second)

	cases := map[string]func(map[string]any){
		"bad work id":    func(b map[string]any) { b["workId"] = "../../etc" },
		"bad scheme":     func(b map[string]any) { b["term"] = map[string]string{"scheme": "NOT VALID", "id": "x"} },
		"unknown term":   func(b map[string]any) { b["term"] = map[string]string{"scheme": "homosaurus", "id": "https://nope"} },
		"bad sourceRef":  func(b map[string]any) { b["sourceRef"] = "javascript:alert(1)" },
		"bad type":       func(b map[string]any) { b["type"] = "UPSERT" },
		"missing reason": func(b map[string]any) { b["type"] = "REMOVE" },
	}
	for name, mutate := range cases {
		body := validBody(ch.Challenge)
		mutate(body)
		rec := doJSON(t, h, http.MethodPost, "/v1/suggestions", "", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}
	// Bad work id on the public read too.
	rec = doJSON(t, h, http.MethodGet, "/v1/works/not-a-work/suggestions", "", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id read: %d", rec.Code)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
