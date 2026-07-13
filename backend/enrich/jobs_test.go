package enrich

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/freeeve/libcat/ingest"

	"github.com/freeeve/libcat/backend/store"
)

// statsEnricher enriches nothing but reports counters, so a job's live
// stats mirror has something to snapshot.
type statsEnricher struct{ st ingest.EnrichStats }

func (e statsEnricher) Name() string { return "stats" }
func (e statsEnricher) Enrich(ctx context.Context, works []ingest.WorkSummary) ([]ingest.Enrichment, error) {
	return nil, nil
}
func (e statsEnricher) RunStats() ingest.EnrichStats { return e.st }

// failingEnricher fails with the configured error.
type failingEnricher struct{ err error }

func (e failingEnricher) Name() string { return "fail" }
func (e failingEnricher) Enrich(ctx context.Context, works []ingest.WorkSummary) ([]ingest.Enrichment, error) {
	return nil, e.err
}

func jobService(t *testing.T) *Service {
	t.Helper()
	return &Service{
		Blob: fixtureStore(t), DB: store.NewMem(), GrainPrefix: "data/works/",
		Sources: map[string]Source{
			"stub":  {Enricher: stubEnricher{}, Mode: ModeDirect},
			"stats": {Enricher: statsEnricher{st: ingest.EnrichStats{Batches: 7, ResolvedCreators: 3}}, Mode: ModeDirect},
			"fail":  {Enricher: failingEnricher{err: errors.New("dial upstream.internal: refused")}, Mode: ModeDirect},
		},
	}
}

// TestJobLifecycle covers kick -> drain -> done: the job record ends DONE
// with the run's result and final stats, and a second drain finds nothing
// queued.
func TestJobLifecycle(t *testing.T) {
	svc := jobService(t)
	job, err := svc.CreateJob(t.Context(), "admin@example.org", "stats", [][2]string{}, nil)
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if job.Status != JobQueued || job.ID == "" {
		t.Fatalf("created = %+v, want QUEUED with an id", job)
	}

	ran, err := svc.RunQueuedJobs(t.Context())
	if err != nil || ran != 1 {
		t.Fatalf("RunQueuedJobs = %d, %v; want 1 run", ran, err)
	}
	got, err := svc.GetJob(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != JobDone || got.Result == nil || got.FinishedAt.IsZero() || got.StartedAt.IsZero() {
		t.Fatalf("after drain = %+v, want DONE with result and timestamps", got)
	}
	if got.Stats == nil || got.Stats.Batches != 7 {
		t.Fatalf("stats = %+v, want the enricher's reported counters", got.Stats)
	}

	if ran, _ := svc.RunQueuedJobs(t.Context()); ran != 0 {
		t.Fatalf("second drain ran %d, want 0", ran)
	}
}

// TestOrphanedRunningJobIsReaped pins the restart-orphan fix (task 440): a
// claim persists RUNNING, so a process that dies mid-run leaves a record no
// drain would touch -- not QUEUED, never finished, badged RUNNING until the
// TTL. The drain now fails a RUNNING record whose heartbeat has gone stale,
// and leaves one with a fresh heartbeat (a live worker) alone.
func TestOrphanedRunningJobIsReaped(t *testing.T) {
	svc := jobService(t)
	now := time.Now().UTC()
	svc.Now = func() time.Time { return now }

	orphan, err := svc.CreateJob(t.Context(), "admin@example.org", "stats", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.claimJob(t.Context(), orphan.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	live, err := svc.CreateJob(t.Context(), "admin@example.org", "stub", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.claimJob(t.Context(), live.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// The orphan's worker died 2 minutes ago; the live one heartbeat since.
	now = now.Add(2 * time.Minute)
	got, err := svc.GetJob(t.Context(), live.ID)
	if err != nil {
		t.Fatal(err)
	}
	got.HeartbeatAt = now
	if err := svc.putJob(t.Context(), got, store.CondNone); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.RunQueuedJobs(t.Context()); err != nil {
		t.Fatalf("drain: %v", err)
	}

	reaped, err := svc.GetJob(t.Context(), orphan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reaped.Status != JobFailed || reaped.Error != "interrupted by a restart" || reaped.FinishedAt.IsZero() {
		t.Fatalf("orphan after drain = %+v, want FAILED interrupted-by-restart with finishedAt", reaped)
	}
	alive, err := svc.GetJob(t.Context(), live.ID)
	if err != nil {
		t.Fatal(err)
	}
	if alive.Status != JobRunning {
		t.Fatalf("live job after drain = %+v, want still RUNNING (fresh heartbeat)", alive)
	}
}

// TestReapStaleJobsStandalone pins the independent reaper (task 464): a
// stale-heartbeat RUNNING orphan is failed by ReapStaleJobs on its own --
// no drain, no dispatch -- while a fresh-heartbeat run is left alone. This
// is the path that recovers a hung sibling while a long legit drain is
// still joined and RunQueuedJobs cannot return.
func TestReapStaleJobsStandalone(t *testing.T) {
	svc := jobService(t)
	now := time.Now().UTC()
	svc.Now = func() time.Time { return now }

	orphan, _ := svc.CreateJob(t.Context(), "a", "stats", nil, nil)
	if _, err := svc.claimJob(t.Context(), orphan.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	live, _ := svc.CreateJob(t.Context(), "a", "stub", nil, nil)
	if _, err := svc.claimJob(t.Context(), live.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// The orphan's heartbeat froze; the live one beat just now.
	now = now.Add(2 * time.Minute)
	got, _ := svc.GetJob(t.Context(), live.ID)
	got.HeartbeatAt = now
	if err := svc.putJob(t.Context(), got, store.CondNone); err != nil {
		t.Fatal(err)
	}

	reaped, err := svc.ReapStaleJobs(t.Context())
	if err != nil || reaped != 1 {
		t.Fatalf("ReapStaleJobs = %d, %v; want exactly the orphan reaped", reaped, err)
	}
	o, _ := svc.GetJob(t.Context(), orphan.ID)
	if o.Status != JobFailed || o.Error != "interrupted by a restart" {
		t.Fatalf("orphan = %+v, want FAILED interrupted-by-restart", o)
	}
	l, _ := svc.GetJob(t.Context(), live.ID)
	if l.Status != JobRunning {
		t.Fatalf("live job = %s, want still RUNNING (fresh heartbeat)", l.Status)
	}
}

// TestJobFailureClassified proves a failed run lands FAILED with the same
// generic client-facing classification the synchronous endpoint uses -- no
// raw upstream detail in the record.
func TestJobFailureClassified(t *testing.T) {
	svc := jobService(t)
	job, err := svc.CreateJob(t.Context(), "admin@example.org", "fail", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunQueuedJobs(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.GetJob(t.Context(), job.ID)
	if got.Status != JobFailed {
		t.Fatalf("status = %s, want FAILED", got.Status)
	}
	if got.Error != "enrichment upstream failed" {
		t.Fatalf("error = %q, want the generic upstream classification", got.Error)
	}
}

// TestJobValidation: unknown sources refuse at kick time, jobs need the
// record store, and unknown ids are ErrJobNotFound.
func TestJobValidation(t *testing.T) {
	svc := jobService(t)
	if _, err := svc.CreateJob(t.Context(), "a", "zz-nope", nil, nil); !errors.Is(err, ErrUnknownSource) {
		t.Fatalf("unknown source err = %v", err)
	}
	if _, err := svc.GetJob(t.Context(), "deadbeef"); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("unknown id err = %v", err)
	}
	noDB := &Service{Sources: map[string]Source{"stub": {Enricher: stubEnricher{}, Mode: ModeDirect}}}
	if _, err := noDB.CreateJob(t.Context(), "a", "stub", nil, nil); !errors.Is(err, ErrMisconfigured) {
		t.Fatalf("no-DB err = %v", err)
	}
}

// TestJobClaimContention: a job already RUNNING is not double-run.
func TestJobClaimContention(t *testing.T) {
	svc := jobService(t)
	job, err := svc.CreateJob(t.Context(), "a", "stub", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.claimJob(t.Context(), job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.claimJob(t.Context(), job.ID); !errors.Is(err, errJobClaimed) {
		t.Fatalf("second claim err = %v, want claimed", err)
	}
	if ran, err := svc.RunQueuedJobs(t.Context()); err != nil || ran != 0 {
		t.Fatalf("drain over a RUNNING job = %d, %v; want 0 runs", ran, err)
	}
}

// TestJobList returns newest first with a deterministic clock.
func TestJobList(t *testing.T) {
	svc := jobService(t)
	base := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	tick := 0
	svc.Now = func() time.Time { tick++; return base.Add(time.Duration(tick) * time.Minute) }
	first, _ := svc.CreateJob(t.Context(), "a", "stub", nil, nil)
	second, _ := svc.CreateJob(t.Context(), "a", "stats", nil, nil)
	jobs, err := svc.ListJobs(t.Context())
	if err != nil || len(jobs) != 2 {
		t.Fatalf("ListJobs = %d, %v", len(jobs), err)
	}
	if jobs[0].CreatedAt.Before(jobs[1].CreatedAt) {
		t.Fatalf("list order = %s then %s, want newest first", jobs[0].ID, jobs[1].ID)
	}
	_ = first
	_ = second
}

// hostedEnricher records which hosts a per-run view was scoped to.
type hostedEnricher struct {
	hosts []string
}

func (e *hostedEnricher) Name() string { return "hosted" }
func (e *hostedEnricher) Enrich(ctx context.Context, works []ingest.WorkSummary) ([]ingest.Enrichment, error) {
	return nil, nil
}
func (e *hostedEnricher) ForHosts(hosts []string) ingest.Enricher {
	return &hostedEnricher{hosts: hosts}
}

func (e *hostedEnricher) Describe() string {
	if len(e.hosts) == 0 {
		return "configured-peer"
	}
	return strings.Join(e.hosts, ", ")
}

// TestJobHostOverride pins the per-job peer-host seam (task 446): hosts on
// a HostScoped source validate at kick time and reach the run; hosts on any
// other source, or URL-shaped hosts, refuse with ErrValidation.
func TestJobHostOverride(t *testing.T) {
	svc := jobService(t)
	svc.Sources["hosted"] = Source{Enricher: &hostedEnricher{}, Mode: ModeDirect}

	job, err := svc.CreateJob(t.Context(), "a", "hosted", nil, []string{"seattle", "sfpl"})
	if err != nil {
		t.Fatalf("CreateJob with hosts: %v", err)
	}
	if len(job.Hosts) != 2 {
		t.Fatalf("job = %+v, want the hosts recorded", job)
	}
	// The record says what the run talks to, host override applied
	// (task 450) -- and without one, the configured descriptor.
	if job.Target != "seattle, sfpl" {
		t.Fatalf("target = %q, want the override hosts", job.Target)
	}
	plain, err := svc.CreateJob(t.Context(), "a", "hosted", nil, nil)
	if err != nil || plain.Target != "configured-peer" {
		t.Fatalf("plain job target = %q, %v; want the configured descriptor", plain.Target, err)
	}
	if _, err := svc.RunQueuedJobs(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.GetJob(t.Context(), job.ID)
	if got.Status != JobDone {
		t.Fatalf("hosted job = %+v, want DONE", got)
	}

	if _, err := svc.CreateJob(t.Context(), "a", "stub", nil, []string{"seattle"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("hosts on a non-hosted source err = %v, want ErrValidation", err)
	}
	if _, err := svc.CreateJob(t.Context(), "a", "hosted", nil, []string{"https://seattle.example"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("URL-shaped host err = %v, want ErrValidation", err)
	}
}

// gatedEnricher signals once when its run reaches Enrich, then blocks until
// released -- so a test can prove two runs are in flight at the same moment.
type gatedEnricher struct {
	name    string
	started chan string
	release chan struct{}
	once    sync.Once
}

func (g *gatedEnricher) Name() string { return g.name }
func (g *gatedEnricher) Enrich(ctx context.Context, works []ingest.WorkSummary) ([]ingest.Enrichment, error) {
	g.once.Do(func() { g.started <- g.name })
	select {
	case <-g.release:
	case <-ctx.Done():
	}
	return nil, nil
}

func gatedService(t *testing.T, started chan string, release chan struct{}, names ...string) *Service {
	t.Helper()
	sources := map[string]Source{}
	for _, n := range names {
		sources[n] = Source{Enricher: &gatedEnricher{name: n, started: started, release: release}, Mode: ModeDirect}
	}
	return &Service{Blob: fixtureStore(t), DB: store.NewMem(), GrainPrefix: "data/works/", Sources: sources}
}

// TestDrainRunsDistinctSourcesConcurrently pins the per-source parallel
// drain (task 461): jobs for different sources are in flight at the same
// time -- both reach Enrich before either is released -- so an independent
// peer's crawl does not block behind another's.
func TestDrainRunsDistinctSourcesConcurrently(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	svc := gatedService(t, started, release, "vega", "tlc")
	if _, err := svc.CreateJob(t.Context(), "admin", "vega", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateJob(t.Context(), "admin", "tlc", nil, nil); err != nil {
		t.Fatal(err)
	}

	done := make(chan int, 1)
	go func() {
		n, err := svc.RunQueuedJobs(t.Context())
		if err != nil {
			t.Errorf("drain: %v", err)
		}
		done <- n
	}()

	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case n := <-started:
			got[n] = true
		case <-time.After(3 * time.Second):
			t.Fatalf("only %v reached Enrich; distinct sources did not run concurrently", got)
		}
	}
	close(release)
	if n := <-done; n != 2 {
		t.Fatalf("ran %d, want 2", n)
	}
}

// TestDrainKeepsSameSourceSerial pins the one constraint: at most one job per
// source per drain (its oldest QUEUED), and a source with a live RUNNING job
// is skipped entirely -- two runs sharing the caller IP would trip a peer's
// per-IP limiter.
func TestDrainKeepsSameSourceSerial(t *testing.T) {
	svc := jobService(t)
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	tick := 0
	svc.Now = func() time.Time { tick++; return base.Add(time.Duration(tick) * time.Minute) }

	older, _ := svc.CreateJob(t.Context(), "a", "stub", nil, nil)
	newer, _ := svc.CreateJob(t.Context(), "a", "stub", nil, nil)

	// One drain runs exactly one of the two same-source jobs (the older).
	ran, err := svc.RunQueuedJobs(t.Context())
	if err != nil || ran != 1 {
		t.Fatalf("first drain = %d, %v; want exactly 1 (same source serial)", ran, err)
	}
	go1, _ := svc.GetJob(t.Context(), older.ID)
	gn, _ := svc.GetJob(t.Context(), newer.ID)
	if go1.Status != JobDone {
		t.Fatalf("older job = %s, want DONE (dispatched first)", go1.Status)
	}
	if gn.Status != JobQueued {
		t.Fatalf("newer job = %s, want still QUEUED (held behind same source)", gn.Status)
	}

	// The second drain picks up the one left behind.
	ran, err = svc.RunQueuedJobs(t.Context())
	if err != nil || ran != 1 {
		t.Fatalf("second drain = %d, %v; want the held job now run", ran, err)
	}
	gn, _ = svc.GetJob(t.Context(), newer.ID)
	if gn.Status != JobDone {
		t.Fatalf("newer job after second drain = %s, want DONE", gn.Status)
	}
}

// TestDrainSourceBusyIsSkipped: a source with a live (fresh-heartbeat)
// RUNNING record has its QUEUED job left untouched this tick.
func TestDrainSourceBusyIsSkipped(t *testing.T) {
	svc := jobService(t)
	now := time.Now().UTC()
	svc.Now = func() time.Time { return now }

	running, _ := svc.CreateJob(t.Context(), "a", "stub", nil, nil)
	if _, err := svc.claimJob(t.Context(), running.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	queued, _ := svc.CreateJob(t.Context(), "a", "stub", nil, nil)

	ran, err := svc.RunQueuedJobs(t.Context())
	if err != nil || ran != 0 {
		t.Fatalf("drain = %d, %v; want 0 (source busy)", ran, err)
	}
	gq, _ := svc.GetJob(t.Context(), queued.ID)
	if gq.Status != JobQueued {
		t.Fatalf("queued job = %s, want still QUEUED behind the RUNNING one", gq.Status)
	}
}

// TestDrainMaxParallelCaps: MaxParallel bounds how many distinct-source jobs
// are in flight at once.
func TestDrainMaxParallelCaps(t *testing.T) {
	started := make(chan string, 3)
	release := make(chan struct{})
	svc := gatedService(t, started, release, "vega", "tlc", "biblio")
	svc.MaxParallel = 1
	for _, src := range []string{"vega", "tlc", "biblio"} {
		if _, err := svc.CreateJob(t.Context(), "admin", src, nil, nil); err != nil {
			t.Fatal(err)
		}
	}

	done := make(chan int, 1)
	go func() { n, _ := svc.RunQueuedJobs(t.Context()); done <- n }()

	// With the cap at 1, exactly one job reaches Enrich; a second must not
	// start until the first is released.
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("no job started")
	}
	select {
	case n := <-started:
		t.Fatalf("a second job (%s) started while the cap is 1", n)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if n := <-done; n != 3 {
		t.Fatalf("ran %d, want all 3 eventually", n)
	}
}
