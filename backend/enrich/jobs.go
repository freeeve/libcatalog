package enrich

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/freeeve/libcat/ingest"

	"github.com/freeeve/libcat/backend/store"
)

// JobStatus is the async-run lifecycle.
type JobStatus string

const (
	JobQueued  JobStatus = "QUEUED"
	JobRunning JobStatus = "RUNNING"
	JobDone    JobStatus = "DONE"
	JobFailed  JobStatus = "FAILED"
)

// jobTTL bounds how long finished jobs stay listable.
const jobTTL = 7 * 24 * time.Hour

// statsInterval is how often a running job's record refreshes with the
// enricher's live counters.
const statsInterval = 3 * time.Second

// staleAfter is how long a RUNNING record may go without a heartbeat before
// the drain declares its worker dead -- twenty missed statsInterval beats.
// A claim persists RUNNING, so a process that dies mid-run (restart, deploy,
// crash) leaves a record no drain would ever touch: not QUEUED, never
// finished, badged RUNNING until the TTL.
const staleAfter = 60 * time.Second

// Job is one asynchronous enrichment run: kicked with a source and scope,
// drained by the worker, its record carrying live batch counters while it
// runs so a poller can show progress on an hours-long corpus pass.
type Job struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	// Filters are the run's [key, value] scope terms (ingest.MatchExtras
	// semantics -- the same scoping the synchronous run and the audit use).
	Filters [][2]string `json:"filters,omitempty"`
	// Hosts is the per-job peer-host override, for sources that take one
	// (the bibliocommons harvest): sweep a different peer list without a
	// restart. Empty keeps the source's configured hosts.
	Hosts []string `json:"hosts,omitempty"`
	// Target names what this run talks to (the source's descriptor at
	// creation, host overrides applied) -- stamped on the record so a
	// finished job still says what it pulled after the config changes.
	Target    string    `json:"target,omitempty"`
	Requester string    `json:"requester"`
	Status    JobStatus `json:"status"`
	// Stats is the live progress while RUNNING (updated per statsInterval
	// when the source reports counters) and the final tallies after.
	Stats *ingest.EnrichStats `json:"stats,omitempty"`
	// Result is the completed run's summary (DONE only).
	Result *Result `json:"result,omitempty"`
	// Error is the failure, classified the same way the synchronous
	// endpoint classifies it (FAILED only; generic, detail in the log).
	Error      string    `json:"error,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	StartedAt  time.Time `json:"startedAt,omitzero"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
	// HeartbeatAt is the worker's liveness signal, rewritten on every stats
	// tick while the run is in flight; a RUNNING record whose heartbeat goes
	// stale is an orphan (its process died mid-run) and the next drain
	// fails it rather than leaving it RUNNING forever.
	HeartbeatAt time.Time `json:"heartbeatAt,omitzero"`
}

// ErrJobNotFound reports an unknown job id.
var ErrJobNotFound = errors.New("enrichment job not found")

// errJobClaimed reports a job another worker already picked up.
var errJobClaimed = errors.New("enrichment job already claimed")

func jobKey(id string) store.Key { return store.Key{PK: "JOB#ENRICH", SK: id} }

// CreateJob queues an asynchronous run. The source must exist (the caller's
// mistake surfaces at kick time, not first drain); execution happens on the
// worker via RunQueuedJobs.
func (s *Service) CreateJob(ctx context.Context, requester, source string, filters [][2]string, hosts []string) (Job, error) {
	if s.DB == nil {
		return Job{}, fmt.Errorf("%w: async jobs need the record store", ErrMisconfigured)
	}
	src, ok := s.Sources[source]
	if !ok {
		return Job{}, fmt.Errorf("%w: %q", ErrUnknownSource, source)
	}
	// Host overrides fail at kick time, not first drain: an unknown
	// capability or a URL-shaped host is the caller's mistake to see now.
	if len(hosts) > 0 {
		if _, ok := src.Enricher.(HostScoped); !ok {
			return Job{}, fmt.Errorf("%w: source %q does not take hosts", ErrValidation, source)
		}
		for _, h := range hosts {
			if h == "" || strings.ContainsAny(h, "./: ") {
				return Job{}, fmt.Errorf("%w: host %q must be a bare subdomain (e.g. seattle)", ErrValidation, h)
			}
		}
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return Job{}, err
	}
	job := Job{
		ID: hex.EncodeToString(suffix), Source: source, Filters: filters, Hosts: hosts,
		Requester: requester, Status: JobQueued, CreatedAt: s.jobNow().UTC(),
	}
	// Stamp what the run will talk to, as configured right now.
	if scoped, err := s.scopedEnricher(src, source, hosts); err == nil {
		if d, ok := scoped.(Describer); ok {
			job.Target = d.Describe()
		}
	}
	if err := s.putJob(ctx, job, store.CondIfAbsent); err != nil {
		return Job{}, err
	}
	return job, nil
}

// GetJob returns one job. The surface is admin-gated, so there is no
// requester scoping.
func (s *Service) GetJob(ctx context.Context, id string) (Job, error) {
	if s.DB == nil {
		return Job{}, ErrJobNotFound
	}
	rec, err := s.DB.Get(ctx, jobKey(id))
	if errors.Is(err, store.ErrNotFound) {
		return Job{}, ErrJobNotFound
	}
	if err != nil {
		return Job{}, err
	}
	var job Job
	if err := json.Unmarshal(rec.Data, &job); err != nil {
		return Job{}, err
	}
	return job, nil
}

// ListJobs returns every live job, newest first.
func (s *Service) ListJobs(ctx context.Context) ([]Job, error) {
	jobs := []Job{}
	if s.DB == nil {
		return jobs, nil
	}
	for rec, err := range s.DB.Query(ctx, "JOB#ENRICH", "", store.QueryOpt{Limit: 200}) {
		if err != nil {
			return nil, err
		}
		var job Job
		if json.Unmarshal(rec.Data, &job) == nil {
			jobs = append(jobs, job)
		}
	}
	// Keys are random ids, so store order is meaningless; newest first.
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].CreatedAt.After(jobs[j].CreatedAt) })
	return jobs, nil
}

// RunQueuedJobs drains QUEUED jobs once -- the worker-loop body for container
// deployments (a ticker) and scheduled serverless drains alike. Job failures
// land in the job record; the returned error is store trouble only.
//
// Jobs for DISTINCT sources run concurrently: the sources hit independent
// external services with independent rate limits, so a whole-catalog Vega
// crawl need not block a BiblioCommons consensus run queued behind it. Jobs
// for the SAME source stay serial -- two runs sharing the caller IP would
// trip a peer's per-IP limiter -- enforced two ways: a source with a live
// RUNNING record is skipped this tick, and only its oldest QUEUED job is
// dispatched. MaxParallel optionally caps the concurrency across sources.
//
// The drain also reaps orphans: a RUNNING record whose heartbeat has gone
// stale belongs to a process that died between claim and completion, and
// nothing else would ever finish it.
func (s *Service) RunQueuedJobs(ctx context.Context) (int, error) {
	if s.DB == nil {
		return 0, nil
	}
	busy := map[string]bool{}     // sources with a live RUNNING job
	oldest := map[string]string{} // source -> oldest QUEUED job id
	oldestAt := map[string]time.Time{}
	for rec, err := range s.DB.Query(ctx, "JOB#ENRICH", "", store.QueryOpt{}) {
		if err != nil {
			return 0, err
		}
		var job Job
		if json.Unmarshal(rec.Data, &job) != nil {
			continue
		}
		switch {
		case job.Status == JobRunning && s.staleRunning(job):
			if err := s.reapJob(ctx, rec, job); err != nil {
				return 0, err
			}
		case job.Status == JobRunning:
			busy[job.Source] = true
		case job.Status == JobQueued:
			if at, ok := oldestAt[job.Source]; !ok || job.CreatedAt.Before(at) {
				oldest[job.Source] = job.ID
				oldestAt[job.Source] = job.CreatedAt
			}
		}
	}

	var toRun []string
	for src, id := range oldest {
		if !busy[src] {
			toRun = append(toRun, id)
		}
	}
	if len(toRun) == 0 {
		return 0, nil
	}
	sort.Strings(toRun) // deterministic dispatch order

	var sem chan struct{}
	if s.MaxParallel > 0 {
		sem = make(chan struct{}, s.MaxParallel)
	}
	var (
		mu       sync.Mutex
		ran      int
		firstErr error
		wg       sync.WaitGroup
	)
	for _, id := range toRun {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if sem != nil {
				sem <- struct{}{}
				defer func() { <-sem }()
			}
			err := s.runJob(ctx, id)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case errors.Is(err, errJobClaimed):
			case err != nil:
				if firstErr == nil {
					firstErr = err
				}
			default:
				ran++
			}
		}(id)
	}
	wg.Wait()
	return ran, firstErr
}

// ReapStaleJobs fails every RUNNING record whose heartbeat has gone stale
// and returns how many it reaped. It runs on a cadence INDEPENDENT of
// RunQueuedJobs: the parallel drain joins its launched jobs, so a legit
// long run (a multi-hour TLC crawl) keeps that call from returning and the
// drain's own inline reap never fires again -- leaving a hung sibling
// orphaned until a full process restart. A standalone reaper on its own
// ticker recovers such orphans while the drain is still joined.
func (s *Service) ReapStaleJobs(ctx context.Context) (int, error) {
	if s.DB == nil {
		return 0, nil
	}
	reaped := 0
	for rec, err := range s.DB.Query(ctx, "JOB#ENRICH", "", store.QueryOpt{}) {
		if err != nil {
			return reaped, err
		}
		var job Job
		if json.Unmarshal(rec.Data, &job) != nil {
			continue
		}
		if job.Status == JobRunning && s.staleRunning(job) {
			if err := s.reapJob(ctx, rec, job); err != nil {
				return reaped, err
			}
			reaped++
		}
	}
	return reaped, nil
}

// staleRunning reports whether a RUNNING job's worker is presumed dead.
// Records written before heartbeats existed fall back to StartedAt.
func (s *Service) staleRunning(job Job) bool {
	hb := job.HeartbeatAt
	if hb.IsZero() {
		hb = job.StartedAt
	}
	return !hb.IsZero() && s.jobNow().Sub(hb) > staleAfter
}

// reapJob fails an orphaned RUNNING record under its version, so a worker
// that is in fact alive (and about to heartbeat) wins the race instead.
func (s *Service) reapJob(ctx context.Context, rec store.Record, job Job) error {
	job.Status = JobFailed
	job.Error = "interrupted by a restart"
	job.FinishedAt = s.jobNow().UTC()
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	rec.Data = data
	rec.ExpireAt = s.jobNow().Add(jobTTL)
	if _, err := s.DB.Put(ctx, rec, store.CondIfVersion); err != nil && !errors.Is(err, store.ErrConditionFailed) {
		return err
	}
	return nil
}

// runJob claims one QUEUED job and executes it, refreshing the record with
// live enricher counters while the run is in flight.
func (s *Service) runJob(ctx context.Context, id string) error {
	job, err := s.claimJob(ctx, id)
	if err != nil {
		return err
	}
	var keep func(*ingest.WorkSummary) bool
	if len(job.Filters) > 0 {
		filters := job.Filters
		keep = func(sum *ingest.WorkSummary) bool { return ingest.MatchExtras(filters, sum.Extras) }
	}

	// While Run executes, mirror the source's live counters into the job
	// record so GET shows batches advancing. Best effort: a lost update is
	// the next tick's problem. The tick runs even for a source with no
	// counters to report: each write refreshes the heartbeat that keeps the
	// drain's orphan reaper off a live run. A host-scoped job reads the
	// scoped view's counters -- RunHosted builds the same view from the
	// same job fields.
	src := s.Sources[job.Source]
	scoped, scopeErr := s.scopedEnricher(src, job.Source, job.Hosts)
	if scopeErr != nil {
		job.FinishedAt = s.jobNow().UTC()
		job.Status = JobFailed
		job.Error = "enrichment run failed"
		return s.putJob(ctx, *job, store.CondNone)
	}
	reporter, reports := scoped.(ingest.StatsReporter)
	stopStats := make(chan struct{})
	statsDone := make(chan struct{})
	go func() {
		defer close(statsDone)
		ticker := time.NewTicker(statsInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopStats:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if reports {
					st := reporter.RunStats()
					job.Stats = &st
				}
				job.HeartbeatAt = s.jobNow().UTC()
				_ = s.putJob(ctx, *job, store.CondNone)
			}
		}
	}()

	result, runErr := s.RunHosted(ctx, job.Source, keep, job.Hosts)
	close(stopStats)
	<-statsDone

	job.FinishedAt = s.jobNow().UTC()
	if runErr != nil {
		job.Status = JobFailed
		job.Error = classifyJobError(runErr)
		if reports {
			st := reporter.RunStats()
			job.Stats = &st
		}
		return s.putJob(ctx, *job, store.CondNone)
	}
	job.Status = JobDone
	job.Result = &result
	job.Stats = result.Stats
	return s.putJob(ctx, *job, store.CondNone)
}

// classifyJobError mirrors the synchronous endpoint's client-facing
// classification: generic per class, raw detail stays server-side.
func classifyJobError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "enrichment upstream timed out"
	case errors.Is(err, ingest.ErrEnricher):
		return "enrichment upstream failed"
	default:
		return "enrichment run failed"
	}
}

// claimJob flips QUEUED -> RUNNING under the record's version so concurrent
// workers cannot double-run.
func (s *Service) claimJob(ctx context.Context, id string) (*Job, error) {
	rec, err := s.DB.Get(ctx, jobKey(id))
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrJobNotFound
	}
	if err != nil {
		return nil, err
	}
	var job Job
	if err := json.Unmarshal(rec.Data, &job); err != nil {
		return nil, err
	}
	if job.Status != JobQueued {
		return nil, errJobClaimed
	}
	job.Status = JobRunning
	job.StartedAt = s.jobNow().UTC()
	job.HeartbeatAt = job.StartedAt
	data, err := json.Marshal(job)
	if err != nil {
		return nil, err
	}
	rec.Data = data
	if _, err := s.DB.Put(ctx, rec, store.CondIfVersion); err != nil {
		if errors.Is(err, store.ErrConditionFailed) {
			return nil, errJobClaimed
		}
		return nil, err
	}
	return &job, nil
}

func (s *Service) putJob(ctx context.Context, job Job, cond store.Cond) error {
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	rec := store.Record{Key: jobKey(job.ID), Data: data, ExpireAt: s.jobNow().Add(jobTTL)}
	_, err = s.DB.Put(ctx, rec, cond)
	return err
}

// jobNow is the service clock (tests may override Now).
func (s *Service) jobNow() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
