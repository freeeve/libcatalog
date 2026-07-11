package batch_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/batch"
	"github.com/freeeve/libcat/backend/editor"
	"github.com/freeeve/libcat/backend/profiles"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/trigger"
)

type fakeNotifier struct{ events []trigger.Event }

func (f *fakeNotifier) Notify(_ context.Context, e trigger.Event) error {
	f.events = append(f.events, e)
	return nil
}

// seedWork writes a typed Work grain with a title and one feed tag.
func seedWork(t *testing.T, st blob.Store, workID, title, tag string) {
	t.Helper()
	const bfNS = "http://id.loc.gov/ontologies/bibframe/"
	ds := &rdf.Dataset{}
	feed := bibframe.FeedGraph("overdrive")
	work := rdf.NewIRI(bibframe.WorkIRI(workID))
	ds.Add(work, rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"), rdf.NewIRI(bfNS+"Work"), feed)
	tnode := rdf.NewBlank("t0")
	ds.Add(work, rdf.NewIRI(bfNS+"title"), tnode, feed)
	ds.Add(tnode, rdf.NewIRI(bfNS+"mainTitle"), rdf.NewLiteral(title, "", ""), feed)
	if tag != "" {
		snode := rdf.NewBlank("s0")
		ds.Add(work, rdf.NewIRI(bfNS+"subject"), snode, feed)
		ds.Add(snode, rdf.NewIRI("http://www.w3.org/2000/01/rdf-schema#label"), rdf.NewLiteral(tag, "", ""), feed)
	}
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Put(t.Context(), bibframe.GrainPath(workID), nq, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
}

func newService(t *testing.T) (*batch.Service, blob.Store, *suggest.Service, *fakeNotifier) {
	t.Helper()
	st := blob.NewMem()
	seedWork(t, st, "wbatch0000001", "Gideon the Ninth", "space opera")
	seedWork(t, st, "wbatch0000002", "Harrow the Ninth", "space opera")
	seedWork(t, st, "wbatch0000003", "The Hobbit", "cozy quest")
	set, err := profiles.LoadDefaults()
	if err != nil {
		t.Fatal(err)
	}
	queue := suggest.New(store.NewMem(), nil, suggest.Caps{})
	notifier := &fakeNotifier{}
	svc := &batch.Service{
		Blob: st, DB: store.NewMem(),
		Mapper: &editor.Mapper{WorkProfile: set["work-monograph"], InstanceProfile: set["instance-ebook"]},
		Queue:  queue, Trigger: notifier,
	}
	return svc, st, queue, notifier
}

// summarySetOps builds one summary set (the work-monograph profile's
// editable literal field) -- the op shape a macro or template carries.
func summarySetOps(text string) []editor.Op {
	return []editor.Op{{
		Resource: "work", Path: "summary", Action: "set",
		Values: []editor.OpValue{{V: text, Lang: "en"}},
	}}
}

// TestListQueriesSortsByLabel is the first test to read ListQueries back:
// it returned whatever the store's sort-key iterator yielded, and the key embeds a
// crypto/rand id, so the order was arbitrary and the query just saved did not land last.
// The labels are created in reverse-alphabetical order so that creation order, label
// order, and (random) id order are all distinct -- only a label sort produces a, b, c.
func TestListQueriesSortsByLabel(t *testing.T) {
	svc, _, _, _ := newService(t)
	ctx := t.Context()
	const owner = "lib@example.org"
	for _, label := range []string{"gamma", "beta", "alpha"} {
		if _, err := svc.CreateQuery(ctx, label, "q "+label, owner); err != nil {
			t.Fatal(err)
		}
	}
	got, err := svc.ListQueries(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	labels := make([]string, len(got))
	for i, sq := range got {
		labels[i] = sq.Label
	}
	if want := []string{"alpha", "beta", "gamma"}; !reflect.DeepEqual(labels, want) {
		t.Errorf("ListQueries order = %v, want %v (sorted by label, not creation or id order)", labels, want)
	}
	// One owner's queries do not leak into another's list.
	if other, err := svc.ListQueries(ctx, "someone@else.org"); err != nil || len(other) != 0 {
		t.Errorf("foreign owner list = %v, %v, want empty", other, err)
	}
}

func TestResolveKinds(t *testing.T) {
	svc, _, _, _ := newService(t)
	ctx := t.Context()

	targets, err := svc.Resolve(ctx, batch.Selection{Kind: batch.KindSearch, Query: "ninth"}, "lib@example.org")
	if err != nil || len(targets) != 2 {
		t.Fatalf("search = %v, %v", targets, err)
	}
	if targets[0].Title == "" {
		t.Fatalf("search targets carry titles: %+v", targets[0])
	}

	targets, err = svc.Resolve(ctx, batch.Selection{Kind: batch.KindAll}, "lib@example.org")
	if err != nil || len(targets) != 3 {
		t.Fatalf("all = %v, %v", targets, err)
	}

	targets, err = svc.Resolve(ctx, batch.Selection{Kind: batch.KindIDs, IDs: []string{"wbatch0000003", "wbatch0000003"}}, "lib@example.org")
	if err != nil || len(targets) != 1 || targets[0].WorkID != "wbatch0000003" {
		t.Fatalf("ids (deduped) = %v, %v", targets, err)
	}

	// Saved query round-trips through the datastore and resolves like search.
	sq, err := svc.CreateQuery(ctx, "The Ninth series", "ninth", "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	targets, err = svc.Resolve(ctx, batch.Selection{Kind: batch.KindSavedQuery, SavedQueryID: sq.ID}, "lib@example.org")
	if err != nil || len(targets) != 2 {
		t.Fatalf("savedQuery = %v, %v", targets, err)
	}
	// Somebody else's saved query does not resolve.
	if _, err := svc.Resolve(ctx, batch.Selection{Kind: batch.KindSavedQuery, SavedQueryID: sq.ID}, "other@example.org"); !errors.Is(err, batch.ErrNotFound) {
		t.Fatalf("foreign savedQuery err = %v", err)
	}

	if _, err := svc.Resolve(ctx, batch.Selection{Kind: batch.KindImportBatch}, "x"); !errors.Is(err, batch.ErrValidation) {
		t.Fatalf("importBatch err = %v", err)
	}
	if _, err := svc.Resolve(ctx, batch.Selection{Kind: "nope"}, "x"); !errors.Is(err, batch.ErrValidation) {
		t.Fatalf("unknown kind err = %v", err)
	}
	if _, err := svc.Resolve(ctx, batch.Selection{Kind: batch.KindIDs, IDs: []string{"not-a-work"}}, "x"); !errors.Is(err, batch.ErrValidation) {
		t.Fatalf("bad id err = %v", err)
	}
}

func TestRunDryRunAndExecute(t *testing.T) {
	svc, st, _, notifier := newService(t)
	ctx := t.Context()
	sel := batch.Selection{Kind: batch.KindSearch, Query: "ninth"}
	ops := summarySetOps("A necromantic space opera.")

	// Dry run: exact per-work deltas, aggregate counts, nothing written.
	dry, err := svc.Run(ctx, sel, ops, true, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if !dry.DryRun || dry.Matched != 2 || dry.Applied != 2 || dry.Failed != 0 {
		t.Fatalf("dry = %+v", dry)
	}
	if dry.Added == 0 || dry.Results[0].Diff == nil || len(dry.Results[0].Diff.Added) == 0 {
		t.Fatalf("dry diffs = %+v", dry.Results)
	}
	grain, _, err := st.Get(ctx, bibframe.GrainPath("wbatch0000001"))
	if err != nil || strings.Contains(string(grain), "necromantic") {
		t.Fatalf("dry run wrote: %v\n%s", err, grain)
	}
	if len(notifier.events) != 0 {
		t.Fatalf("dry run notified: %+v", notifier.events)
	}

	// Execute: per-record etags, grains rewritten, one audit + one trigger.
	run, err := svc.Run(ctx, sel, ops, false, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if run.Applied != 2 || run.Failed != 0 || run.Results[0].ETag == "" {
		t.Fatalf("run = %+v", run)
	}
	for _, id := range []string{"wbatch0000001", "wbatch0000002"} {
		grain, _, err := st.Get(ctx, bibframe.GrainPath(id))
		if err != nil || !strings.Contains(string(grain), "A necromantic space opera.") {
			t.Fatalf("%s not rewritten: %v\n%s", id, err, grain)
		}
	}
	grain, _, _ = st.Get(ctx, bibframe.GrainPath("wbatch0000003"))
	if strings.Contains(string(grain), "necromantic") {
		t.Fatal("unselected work rewritten")
	}
	if len(notifier.events) != 1 || len(notifier.events[0].Paths) != 2 {
		t.Fatalf("events = %+v", notifier.events)
	}

	// Per-record failure reporting: a missing work fails alone.
	mixed, err := svc.Run(ctx, batch.Selection{Kind: batch.KindIDs, IDs: []string{"wbatch0000003", "wmissing00001"}},
		ops, false, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if mixed.Applied != 1 || mixed.Failed != 1 {
		t.Fatalf("mixed = %+v", mixed)
	}

	// Instance-targeted ops are refused in batch context.
	bad := []editor.Op{{Resource: "i123", Path: "isbn", Action: "clear"}}
	if _, err := svc.Run(ctx, sel, bad, true, "x"); !errors.Is(err, batch.ErrValidation) {
		t.Fatalf("instance op err = %v", err)
	}
}

func TestRunCap(t *testing.T) {
	svc, _, _, _ := newService(t)
	svc.MaxWorks = 2
	if _, err := svc.Run(t.Context(), batch.Selection{Kind: batch.KindAll}, summarySetOps("x"), true, "lib@example.org"); !errors.Is(err, batch.ErrValidation) {
		t.Fatalf("cap err = %v", err)
	}
}

func TestMacroCRUDAndParams(t *testing.T) {
	svc, _, _, _ := newService(t)
	ctx := t.Context()

	m, err := svc.CreateMacro(ctx, batch.Macro{
		OwnedMeta: batch.OwnedMeta{Label: "Stamp summary"},
		Keys:      "4", // "1" is the editor's Native-tab chord
		Ops: []editor.Op{{
			Resource: "work", Path: "summary", Action: "set",
			Values: []editor.OpValue{{V: "${text} (stamped)", Lang: "en"}},
		}},
		Params: []batch.Param{{Name: "text", Label: "Summary text", Default: "No summary"}},
	}, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if m.ID == "" || m.Owner != "lib@example.org" || m.Shared {
		t.Fatalf("macro = %+v", m)
	}

	// Parameter substitution, defaults, and the fail-closed missing case.
	ops, err := batch.ApplyParams(m, map[string]string{"text": "A space opera"})
	if err != nil || ops[0].Values[0].V != "A space opera (stamped)" {
		t.Fatalf("params = %+v, %v", ops, err)
	}
	ops, err = batch.ApplyParams(m, nil)
	if err != nil || ops[0].Values[0].V != "No summary (stamped)" {
		t.Fatalf("default = %+v, %v", ops, err)
	}
	// A blank value means "use the default", same as omitted -- the client's
	// applyParams already reads it that way, and the parameter field
	// advertises the default as its placeholder (the ui
	// macros.test.ts table carries this same fixture).
	ops, err = batch.ApplyParams(m, map[string]string{"text": ""})
	if err != nil || ops[0].Values[0].V != "No summary (stamped)" {
		t.Fatalf("blank param = %+v, %v", ops, err)
	}
	// Blank with no default fails closed, naming the parameter.
	noDefault := m
	noDefault.Params = []batch.Param{{Name: "text", Label: "Summary text"}}
	if _, err := batch.ApplyParams(noDefault, map[string]string{"text": ""}); !errors.Is(err, batch.ErrValidation) || !strings.Contains(err.Error(), `parameter "text"`) {
		t.Fatalf("blank without default err = %v", err)
	}
	orphan := m
	orphan.Params = nil
	if _, err := batch.ApplyParams(orphan, nil); !errors.Is(err, batch.ErrValidation) {
		t.Fatalf("unresolved param err = %v", err)
	}
	// Substitution never mutates the stored macro's ops.
	if m.Ops[0].Values[0].V != "${text} (stamped)" {
		t.Fatalf("macro ops mutated: %+v", m.Ops)
	}

	// Personal macros are invisible to others; shared ones list for everyone.
	if _, err := svc.GetMacro(ctx, "other@example.org", m.ID); !errors.Is(err, batch.ErrNotFound) {
		t.Fatalf("foreign personal get err = %v", err)
	}
	m.Shared = true
	updated, err := svc.UpdateMacro(ctx, m.ID, m, "lib@example.org", false)
	if err != nil || !updated.Shared {
		t.Fatalf("share = %+v, %v", updated, err)
	}
	got, err := svc.GetMacro(ctx, "other@example.org", m.ID)
	if err != nil || got.Label != "Stamp summary" {
		t.Fatalf("shared get = %+v, %v", got, err)
	}
	list, err := svc.ListMacros(ctx, "other@example.org")
	if err != nil || len(list) != 1 {
		t.Fatalf("shared list = %+v, %v", list, err)
	}
	// The personal copy is gone after the share flip (no duplicates).
	mine, err := svc.ListMacros(ctx, "lib@example.org")
	if err != nil || len(mine) != 1 {
		t.Fatalf("own list after share = %+v, %v", mine, err)
	}

	// A non-owner, non-admin librarian may not update or delete it.
	if _, err := svc.UpdateMacro(ctx, m.ID, got, "other@example.org", false); !errors.Is(err, batch.ErrForbidden) {
		t.Fatalf("foreign update err = %v", err)
	}
	if err := svc.DeleteMacro(ctx, "other@example.org", m.ID, false); !errors.Is(err, batch.ErrForbidden) {
		t.Fatalf("foreign delete err = %v", err)
	}
	// An admin is the shared macro's custodian: it may relabel it (staying
	// shared, owner unchanged) and delete it -- the recovery path when the
	// owner's account is gone.
	relabel := got
	relabel.Label = "Stamp summary (curated)"
	curated, err := svc.UpdateMacro(ctx, m.ID, relabel, "boss@example.org", true)
	if err != nil || curated.Label != "Stamp summary (curated)" || !curated.Shared || curated.Owner != "lib@example.org" {
		t.Fatalf("admin relabel = %+v, %v", curated, err)
	}
	if err := svc.DeleteMacro(ctx, "boss@example.org", m.ID, true); err != nil {
		t.Fatalf("admin delete of shared macro: %v", err)
	}
	if _, err := svc.GetMacro(ctx, "lib@example.org", m.ID); !errors.Is(err, batch.ErrNotFound) {
		t.Fatalf("get after delete err = %v", err)
	}

	// Validation floor.
	if _, err := svc.CreateMacro(ctx, batch.Macro{Ops: summarySetOps("x")}, "x"); !errors.Is(err, batch.ErrValidation) {
		t.Fatalf("no label err = %v", err)
	}
	if _, err := svc.CreateMacro(ctx, batch.Macro{OwnedMeta: batch.OwnedMeta{Label: "empty"}}, "x"); !errors.Is(err, batch.ErrValidation) {
		t.Fatalf("no ops err = %v", err)
	}
}

// TestSharedMacroOverSelection is the acceptance shape: a shared macro run
// over a search selection rewrites every matching record.
func TestSharedMacroOverSelection(t *testing.T) {
	svc, st, _, _ := newService(t)
	ctx := t.Context()
	m, err := svc.CreateMacro(ctx, batch.Macro{
		OwnedMeta: batch.OwnedMeta{Label: "Series summary", Shared: true},
		Ops: []editor.Op{{
			Resource: "work", Path: "summary", Action: "set",
			Values: []editor.OpValue{{V: "${series} book.", Lang: "en"}},
		}},
		Params: []batch.Param{{Name: "series", Label: "Series name"}},
	}, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	// A different librarian runs it, as a template.
	loaded, err := svc.GetMacro(ctx, "other@example.org", m.ID)
	if err != nil {
		t.Fatal(err)
	}
	ops, err := batch.ApplyParams(loaded, map[string]string{"series": "Locked Tomb"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.Run(ctx, batch.Selection{Kind: batch.KindSearch, Query: "ninth"}, ops, false, "other@example.org")
	if err != nil || run.Applied != 2 {
		t.Fatalf("run = %+v, %v", run, err)
	}
	grain, _, err := st.Get(ctx, bibframe.GrainPath("wbatch0000002"))
	if err != nil || !strings.Contains(string(grain), "Locked Tomb book.") {
		t.Fatalf("template not applied: %v\n%s", err, grain)
	}
}

// fakeIndex records the read-your-writes calls Run must make.
type fakeIndex struct {
	applied map[string]string // grain path -> etag
	grains  map[string]int    // grain path -> written bytes
	feeds   [][]string
}

func (f *fakeIndex) Apply(path, etag string, grain []byte) {
	if f.applied == nil {
		f.applied = map[string]string{}
		f.grains = map[string]int{}
	}
	f.applied[path] = etag
	f.grains[path] = len(grain)
}

func (f *fakeIndex) AppendFeed(_ context.Context, paths ...string) error {
	f.feeds = append(f.feeds, paths)
	return nil
}

// TestRunUpdatesIndex covers an executed batch keeps the shared
// work index exact for its own writes -- Apply per written grain with the
// reported etag, one AppendFeed over the changed paths -- while a dry run
// touches nothing. Before this, batch edits stayed invisible to work search
// for up to the 30s refresh TTL.
func TestRunUpdatesIndex(t *testing.T) {
	svc, _, _, _ := newService(t)
	ix := &fakeIndex{}
	svc.Index = ix
	ctx := t.Context()
	sel := batch.Selection{Kind: batch.KindSearch, Query: "ninth"}
	ops := summarySetOps("Indexed immediately.")

	if _, err := svc.Run(ctx, sel, ops, true, "lib@example.org"); err != nil {
		t.Fatal(err)
	}
	if len(ix.applied) != 0 || len(ix.feeds) != 0 {
		t.Fatalf("dry run touched the index: %+v %+v", ix.applied, ix.feeds)
	}

	run, err := svc.Run(ctx, sel, ops, false, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if run.Applied != 2 {
		t.Fatalf("run = %+v", run)
	}
	for _, item := range run.Results {
		path := bibframe.GrainPath(item.WorkID)
		if ix.applied[path] != item.ETag {
			t.Fatalf("%s: index etag %q, run etag %q", item.WorkID, ix.applied[path], item.ETag)
		}
		if ix.grains[path] == 0 {
			t.Fatalf("%s: Apply saw no grain bytes", item.WorkID)
		}
	}
	if len(ix.feeds) != 1 || len(ix.feeds[0]) != 2 {
		t.Fatalf("feed appends = %+v, want one call with both paths", ix.feeds)
	}

	// A failed record never reaches the index.
	before := len(ix.applied)
	mixed, err := svc.Run(ctx, batch.Selection{Kind: batch.KindIDs, IDs: []string{"wmissing00001"}}, ops, false, "x")
	if err != nil {
		t.Fatal(err)
	}
	if mixed.Failed != 1 || len(ix.applied) != before {
		t.Fatalf("failed record indexed: %+v (applied %d -> %d)", mixed, before, len(ix.applied))
	}
}

// TestWhitespaceQueriesRejected covers a query that normalizes
// to nothing is refused everywhere -- resolve, run, saved-query creation,
// and legacy saved queries at resolution -- so only KindAll can select the
// whole catalog.
func TestWhitespaceQueriesRejected(t *testing.T) {
	svc, _, _, _ := newService(t)
	ctx := t.Context()
	blanks := []string{"", " ", "  ", "\t", "\n", " \t\n "}

	for _, q := range blanks {
		if _, err := svc.Resolve(ctx, batch.Selection{Kind: batch.KindSearch, Query: q}, "x"); !errors.Is(err, batch.ErrValidation) {
			t.Errorf("Resolve(%q) err = %v, want ErrValidation", q, err)
		}
		if _, err := svc.Run(ctx, batch.Selection{Kind: batch.KindSearch, Query: q}, summarySetOps("nope"), true, "x"); !errors.Is(err, batch.ErrValidation) {
			t.Errorf("Run(%q) err = %v, want ErrValidation", q, err)
		}
		if _, err := svc.CreateQuery(ctx, "zz-ws", q, "x"); !errors.Is(err, batch.ErrValidation) {
			t.Errorf("CreateQuery(%q) err = %v, want ErrValidation", q, err)
		}
	}
	if _, err := svc.CreateQuery(ctx, "   ", "real query", "x"); !errors.Is(err, batch.ErrValidation) {
		t.Errorf("whitespace label accepted: %v", err)
	}

	// KindAll remains the explicit whole-catalog path.
	all, err := svc.Resolve(ctx, batch.Selection{Kind: batch.KindAll}, "x")
	if err != nil || len(all) == 0 {
		t.Fatalf("KindAll = %d targets, err %v", len(all), err)
	}
	// A real search still scopes.
	scoped, err := svc.Resolve(ctx, batch.Selection{Kind: batch.KindSearch, Query: " Ninth "}, "x")
	if err != nil || len(scoped) == 0 || len(scoped) >= len(all) {
		t.Fatalf("scoped search = %d of %d, err %v", len(scoped), len(all), err)
	}
}
