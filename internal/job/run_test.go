package job_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

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
type fakeQueue struct {
	extendErr error
	extended  chan struct{}

	mu        sync.Mutex
	jobs      []job.Job
	claimErrs []error
	claims    int
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
	return q.extendErr
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
	case id := <-q.extended: // wait for a heartbeat so the loop has had time to misbehave
		_ = id
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
	q := newFakeQueue(job.Job{ID: 3, ScanID: 3, Attempts: 1})
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
