package enrich

import (
	"context"
	"errors"
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
