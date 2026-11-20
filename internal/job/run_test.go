package job_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/job"
)

// syncBuffer is a bytes.Buffer that is safe for concurrent use, so a logger
// can write to it from the goroutines Run starts.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// fakeQueue hands out the jobs it holds, one claim at a time, and then
// reports that none is left. claimErrs are returned by the first claims,
// before any job is handed out.
//
// extendErrs are returned by the first renewals, and extendErr by the rest.
// releaseErr is what ReleaseJob returns, and onRelease, when set, runs with
// the context of each ReleaseJob call.
type fakeQueue struct {
	extendErr  error
	extended   chan struct{}
	releaseErr error
	onRelease  func(ctx context.Context)

	mu         sync.Mutex
	jobs       []job.Job
	claimErrs  []error
	claims     int
	extendErrs []error
	releases   []int64
}

func newFakeQueue(jobs ...job.Job) *fakeQueue {
	return &fakeQueue{jobs: jobs, extended: make(chan struct{}, 100)}
}

func (q *fakeQueue) ClaimJob(_ context.Context, _ string, _ time.Duration) (job.Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.claims++
	if len(q.claimErrs) > 0 {
		err := q.claimErrs[0]
		q.claimErrs = q.claimErrs[1:]
		return job.Job{}, err
	}
	if len(q.jobs) == 0 {
		return job.Job{}, job.ErrNoJob
	}
	j := q.jobs[0]
	q.jobs = q.jobs[1:]
	return j, nil
}

func (q *fakeQueue) ExtendJob(_ context.Context, _ int64, _ string, _ time.Duration) error {
	select {
	case q.extended <- struct{}{}:
	default:
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.extendErrs) > 0 {
		err := q.extendErrs[0]
		q.extendErrs = q.extendErrs[1:]
		return err
	}
	return q.extendErr
}

func (q *fakeQueue) ReleaseJob(ctx context.Context, jobID int64, _ string) error {
	if q.onRelease != nil {
		q.onRelease(ctx)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.releases = append(q.releases, jobID)
	return q.releaseErr
}

// released returns the IDs of the jobs ReleaseJob was called for.
func (q *fakeQueue) released() []int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Clone(q.releases)
}

// runInBackground starts job.Run and returns a function that cancels it and
// returns its result. The test also waits for Run when it ends.
func runInBackground(t *testing.T, q job.Queue, h job.Handler, logger *slog.Logger) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	opts := job.Options{WorkerID: "fake-worker", Poll: time.Millisecond, Lease: time.Minute, Heartbeat: time.Millisecond}
	done := make(chan error, 1)
	go func() { done <- job.Run(ctx, logger, q, h, opts) }()
	var once sync.Once
	var result error
	stop = func() error {
		once.Do(func() {
			cancel()
			result = <-done
		})
		return result
	}
	t.Cleanup(func() { _ = stop() }) // the result is checked by the test that calls stop
	return stop
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// assertJobAttrs fails the test unless the JSON log holds a record with
// message msg whose attributes include want.
func assertJobAttrs(t *testing.T, log, msg string, want map[string]any) {
	t.Helper()
	for line := range strings.Lines(log) {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("json.Unmarshal(%q) error = %v", line, err)
		}
		if rec["msg"] != msg {
			continue
		}
		got := map[string]any{}
		for k := range want {
			got[k] = rec[k]
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("Run() logged %q with job attributes (-want +got):\n%s", msg, diff)
		}
		return
	}
	t.Errorf("Run() did not log %q; log:\n%s", msg, log)
}

func TestDispatch(t *testing.T) {
	kinds := []job.Kind{job.KindScan, job.KindApplyCheck, job.KindApply}
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			calls := map[job.Kind]int{}
			handlers := map[job.Kind]job.Handler{}
			for _, k := range kinds {
				handlers[k] = func(_ context.Context, j job.Job) error {
					if j.ID != 5 {
						t.Errorf("Dispatch() passed job %d to the %s handler, want 5", j.ID, k)
					}
					calls[k]++
					return nil
				}
			}
			if err := job.Dispatch(handlers)(t.Context(), job.Job{ID: 5, Kind: kind}); err != nil {
				t.Fatalf("Dispatch()(%s job) error = %v, want nil", kind, err)
			}
			if want := map[job.Kind]int{kind: 1}; !maps.Equal(calls, want) {
				t.Errorf("Dispatch()(%s job) handler calls = %v, want %v", kind, calls, want)
			}
		})
	}
	t.Run("unknown kind", func(t *testing.T) {
		called := false
		h := job.Dispatch(map[job.Kind]job.Handler{job.KindScan: func(context.Context, job.Job) error {
			called = true
			return nil
		}})
		for _, kind := range []job.Kind{"", job.KindApply, "fake-kind"} {
			if err := h(t.Context(), job.Job{ID: 5, Kind: kind}); err == nil {
				t.Errorf("Dispatch()(%q job) error = nil, want an error", kind)
			}
		}
		if called {
			t.Errorf("Dispatch() ran the scan handler for a job of another kind")
		}
	})
	t.Run("handler error", func(t *testing.T) {
		fail := errors.New("fake handler failure")
		h := job.Dispatch(map[job.Kind]job.Handler{job.KindScan: func(context.Context, job.Job) error { return fail }})
		if err := h(t.Context(), job.Job{ID: 5, Kind: job.KindScan}); !errors.Is(err, fail) {
			t.Errorf("Dispatch()(scan job) error = %v, want the handler's error", err)
		}
	})
}

func TestRun_HandlesJob(t *testing.T) {
	q := newFakeQueue(job.Job{ID: 7, ScanID: 9, Attempts: 1})
	got := make(chan job.Job, 1)
	stop := runInBackground(t, q, func(_ context.Context, j job.Job) error {
		got <- j
		return nil
	}, discardLogger())

	want := job.Job{ID: 7, ScanID: 9, Attempts: 1}
	if j := <-got; j != want {
		t.Errorf("Run() passed %+v to the handler, want %+v", j, want)
	}
	if err := stop(); err != nil {
		t.Errorf("Run() after cancel = %v, want nil", err)
	}
}

func TestRun_ExtendsLease(t *testing.T) {
	q := newFakeQueue(job.Job{ID: 1, ScanID: 1, Attempts: 1})
	release := make(chan struct{})
	stop := runInBackground(t, q, func(ctx context.Context, _ job.Job) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}, discardLogger())

	<-q.extended // the handler is still running, so this is a renewal during the job
	<-q.extended
	close(release)
	if err := stop(); err != nil {
		t.Errorf("Run() after cancel = %v, want nil", err)
	}
}

func TestRun_CancelsHandlerWhenLeaseLost(t *testing.T) {
	q := newFakeQueue(job.Job{ID: 1, ScanID: 1, Attempts: 1})
	q.extendErr = job.ErrLeaseLost
	cancelled := make(chan struct{})
	stop := runInBackground(t, q, func(ctx context.Context, _ job.Job) error {
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	}, discardLogger())

	<-cancelled // the handler's context ended although Run's context did not
	if err := stop(); err != nil {
		t.Errorf("Run() after cancel = %v, want nil", err)
	}
}

func TestRun_StopsOnCancel(t *testing.T) {
	// Inside the bubble, synctest.Wait returns once every goroutine is blocked,
	// so "Run has not returned yet" is checked without waiting on a clock.
	synctest.Test(t, func(t *testing.T) {
		q := newFakeQueue(job.Job{ID: 1, ScanID: 1, Attempts: 1})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		opts := job.Options{WorkerID: "fake-worker", Poll: time.Millisecond, Lease: time.Minute, Heartbeat: time.Millisecond}

		release := make(chan struct{})
		handlerCancelled := make(chan struct{})
		handlerReturned := false
		returned := make(chan error, 1)
		go func() {
			returned <- job.Run(ctx, discardLogger(), q, func(ctx context.Context, _ job.Job) error {
				<-ctx.Done()
				close(handlerCancelled)
				<-release // the handler is still finishing its work
				handlerReturned = true
				return ctx.Err()
			}, opts)
		}()

		synctest.Wait() // the handler is running
		cancel()
		synctest.Wait() // the handler saw the cancellation and is held
		select {
		case <-handlerCancelled:
		default:
			t.Fatal("Run() did not cancel the running handler's context when its own was cancelled")
		}
		select {
		case err := <-returned:
			t.Fatalf("Run() returned %v while the handler was still running, want it to wait", err)
		default:
		}

		close(release)
		synctest.Wait()
		select {
		case err := <-returned:
			if err != nil {
				t.Errorf("Run() after cancel = %v, want nil", err)
			}
		default:
			t.Fatal("Run() did not return after the handler did")
		}
		if !handlerReturned {
			t.Error("Run() returned before the handler did")
		}
	})
}

func TestRun_OneJobAtATime(t *testing.T) {
	q := newFakeQueue(job.Job{ID: 1, ScanID: 1, Attempts: 1}, job.Job{ID: 2, ScanID: 2, Attempts: 1})
	started := make(chan int64, 2)
	release := make(chan struct{})
	stop := runInBackground(t, q, func(_ context.Context, j job.Job) error {
		started <- j.ID
		<-release
		return nil
	}, discardLogger())

	if id := <-started; id != 1 {
		t.Fatalf("first handled job = %d, want 1", id)
	}
	select {
	case <-q.extended: // wait for a heartbeat so the loop has had time to misbehave
	case id := <-started:
		t.Fatalf("job %d started while job 1 was still running", id)
	}
	close(release)
	if id := <-started; id != 2 {
		t.Errorf("second handled job = %d, want 2", id)
	}
	if err := stop(); err != nil {
		t.Errorf("Run() after cancel = %v, want nil", err)
	}
}

func TestRun_SurvivesClaimError(t *testing.T) {
	q := newFakeQueue(job.Job{ID: 3, ScanID: 3, Attempts: 1})
	q.claimErrs = []error{errors.New("connection reset")}
	var logs syncBuffer
	handled := make(chan struct{})
	stop := runInBackground(t, q, func(context.Context, job.Job) error {
		close(handled)
		return nil
	}, slog.New(slog.NewJSONHandler(&logs, nil)))

	<-handled
	if err := stop(); err != nil {
		t.Errorf("Run() after cancel = %v, want nil", err)
	}
	if n := strings.Count(logs.String(), `"level":"ERROR"`); n != 1 {
		t.Errorf("Run() logged %d errors for one failed claim, want 1; log:\n%s", n, logs.String())
	}
}

func TestRun_LogsHandlerErrorOnce(t *testing.T) {
	q := newFakeQueue(job.Job{ID: 3, Kind: job.KindApply, ApplyID: 11, Attempts: 1})
	var logs syncBuffer
	handled := make(chan struct{})
	stop := runInBackground(t, q, func(context.Context, job.Job) error {
		defer close(handled)
		return errors.New("fake handler failure")
	}, slog.New(slog.NewJSONHandler(&logs, nil)))

	<-handled
	if err := stop(); err != nil {
		t.Errorf("Run() after cancel = %v, want nil", err)
	}
	if n := strings.Count(logs.String(), "fake handler failure"); n != 1 {
		t.Errorf("Run() logged the handler error %d times, want 1; log:\n%s", n, logs.String())
	}
	assertJobAttrs(t, logs.String(), "job failed", map[string]any{
		"job_id": float64(3), "kind": "apply", "scan_id": float64(0), "apply_id": float64(11),
	})
}

func TestRun_RejectsBadOptions(t *testing.T) {
	good := job.Options{WorkerID: "w", Poll: time.Second, Lease: time.Minute, Heartbeat: time.Second}
	tests := []struct {
		name string
		edit func(*job.Options)
	}{
		{"no worker ID", func(o *job.Options) { o.WorkerID = "" }},
		{"no poll", func(o *job.Options) { o.Poll = 0 }},
		{"no lease", func(o *job.Options) { o.Lease = 0 }},
		{"no heartbeat", func(o *job.Options) { o.Heartbeat = 0 }},
		{"heartbeat not shorter than lease", func(o *job.Options) { o.Heartbeat = o.Lease }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := good
			tt.edit(&opts)
			err := job.Run(t.Context(), discardLogger(), newFakeQueue(), func(context.Context, job.Job) error { return nil }, opts)
			if err == nil {
				t.Errorf("Run(%+v) = nil, want an error", opts)
			}
		})
	}
}

func TestRun_ReleasesJobOnCancel(t *testing.T) {
	q := newFakeQueue(job.Job{ID: 4, ScanID: 4, Attempts: 1})
	var handlerReturned atomic.Bool
	type call struct {
		afterHandler bool
		ctxErr       error
		hasDeadline  bool
	}
	calls := make(chan call, 2)
	q.onRelease = func(ctx context.Context) {
		_, ok := ctx.Deadline()
		calls <- call{afterHandler: handlerReturned.Load(), ctxErr: ctx.Err(), hasDeadline: ok}
	}
	started := make(chan struct{})
	stop := runInBackground(t, q, func(ctx context.Context, _ job.Job) error {
		close(started)
		<-ctx.Done()
		defer handlerReturned.Store(true)
		return ctx.Err()
	}, discardLogger())

	<-started
	if err := stop(); err != nil {
		t.Errorf("Run() after cancel = %v, want nil", err)
	}
	if got := q.released(); !slices.Equal(got, []int64{4}) {
		t.Fatalf("Run() released jobs %v on cancel, want [4]", got)
	}
	c := <-calls
	if !c.afterHandler || c.ctxErr != nil || !c.hasDeadline {
		t.Errorf("ReleaseJob() call = %+v, want it after the handler returned, with a live context that has a deadline", c)
	}
}

func TestRun_NoReleaseWhenLeaseLost(t *testing.T) {
	q := newFakeQueue(job.Job{ID: 1, ScanID: 1, Attempts: 1})
	q.extendErr = job.ErrLeaseLost
	cancelled := make(chan struct{})
	stop := runInBackground(t, q, func(ctx context.Context, _ job.Job) error {
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	}, discardLogger())

	<-cancelled
	if err := stop(); err != nil {
		t.Errorf("Run() after cancel = %v, want nil", err)
	}
	if got := q.released(); len(got) != 0 {
		t.Errorf("Run() released jobs %v after the lease was lost, want none", got)
	}
}

func TestRun_NoReleaseWhenHandlerFinished(t *testing.T) {
	tests := []struct {
		name string
		// handler runs as the job's handler; started is closed when it runs.
		handler func(started chan struct{}) job.Handler
	}{
		{"returned before the stop", func(started chan struct{}) job.Handler {
			return func(context.Context, job.Job) error {
				close(started)
				return nil
			}
		}},
		{"finished the job after the stop", func(started chan struct{}) job.Handler {
			return func(ctx context.Context, _ job.Job) error {
				close(started)
				<-ctx.Done()
				return nil
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := newFakeQueue(job.Job{ID: 1, ScanID: 1, Attempts: 1})
			started := make(chan struct{})
			stop := runInBackground(t, q, tt.handler(started), discardLogger())
			<-started
			if err := stop(); err != nil {
				t.Errorf("Run() after cancel = %v, want nil", err)
			}
			if got := q.released(); len(got) != 0 {
				t.Errorf("Run() released jobs %v after the handler finished the job, want none", got)
			}
		})
	}
}

func TestRun_ReleaseErrorIsLoggedOnce(t *testing.T) {
	q := newFakeQueue(job.Job{ID: 1, Kind: job.KindScan, ScanID: 1, Attempts: 1})
	q.releaseErr = errors.New("fake release failure")
	var logs syncBuffer
	started := make(chan struct{})
	stop := runInBackground(t, q, func(ctx context.Context, _ job.Job) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}, slog.New(slog.NewJSONHandler(&logs, nil)))

	<-started
	if err := stop(); err != nil {
		t.Errorf("Run() after a failed release = %v, want nil", err)
	}
	if n := strings.Count(logs.String(), "fake release failure"); n != 1 {
		t.Errorf("Run() logged the release error %d times, want 1; log:\n%s", n, logs.String())
	}
	assertJobAttrs(t, logs.String(), "releasing the job failed", map[string]any{
		"job_id": float64(1), "kind": "scan", "scan_id": float64(1), "apply_id": float64(0),
	})
}

// leaseExpiry runs one job in a synctest bubble with a lease of one minute
// renewed every 20 seconds, and returns how long after the start the
// handler's context was cancelled, and the jobs released.
func leaseExpiry(t *testing.T, q *fakeQueue) (time.Duration, []int64) {
	t.Helper()
	var elapsed time.Duration
	var released []int64
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		opts := job.Options{WorkerID: "fake-worker", Poll: time.Hour, Lease: time.Minute, Heartbeat: 20 * time.Second}
		start := time.Now()
		cancelled := make(chan time.Duration, 1)
		returned := make(chan error, 1)
		go func() {
			returned <- job.Run(ctx, discardLogger(), q, func(ctx context.Context, _ job.Job) error {
				// A handler that is never cancelled gives up after ten
				// minutes, so a failure is reported rather than hanging.
				select {
				case <-ctx.Done():
				case <-time.After(10 * time.Minute):
				}
				cancelled <- time.Since(start)
				return ctx.Err()
			}, opts)
		}()
		elapsed = <-cancelled
		cancel()
		if err := <-returned; err != nil {
			t.Errorf("Run() after cancel = %v, want nil", err)
		}
		released = q.released()
	})
	return elapsed, released
}

func TestRun_CancelsHandlerWhenLeaseExpiresLocally(t *testing.T) {
	q := newFakeQueue(job.Job{ID: 1, ScanID: 1, Attempts: 1})
	q.extendErr = errors.New("fake connection refused")
	elapsed, released := leaseExpiry(t, q)
	if elapsed != time.Minute {
		t.Errorf("Run() with failing renewals cancelled the handler %v after the claim, want at the lease of 1m0s", elapsed)
	}
	if len(released) != 0 {
		t.Errorf("Run() released jobs %v after the lease ran out, want none", released)
	}
}

func TestRun_RenewalAfterFailureKeepsHandler(t *testing.T) {
	q := newFakeQueue(job.Job{ID: 1, ScanID: 1, Attempts: 1})
	// The renewal at 20s fails, the one at 40s succeeds, and every later one
	// fails, so the lease runs out one minute after the 40s renewal.
	q.extendErrs = []error{errors.New("fake connection refused"), nil}
	q.extendErr = errors.New("fake connection refused")
	elapsed, _ := leaseExpiry(t, q)
	if want := 40*time.Second + time.Minute; elapsed != want {
		t.Errorf("Run() cancelled the handler %v after the claim, want %v, one lease after the last renewal", elapsed, want)
	}
}
