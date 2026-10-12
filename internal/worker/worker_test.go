package worker

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cyverse-de/data-info/internal/clients/asynctasks"
	"github.com/sirupsen/logrus"
)

// fakeTasks records what a runner posted, and can be made to fail a fixed number of times.
type fakeTasks struct {
	mu sync.Mutex

	task *asynctasks.Task
	get  error

	statuses  []asynctasks.Status
	completed []asynctasks.Status

	// trail is every status recorded, progress and terminal alike, in order and with when.
	trail []posted

	// failuresLeft makes the next N posts fail, to exercise the retry budgets.
	failuresLeft int

	// slowProgress delays each progress post, modelling a degraded async-tasks. The
	// terminal status is deliberately unaffected: what the flush bound protects is the
	// release of the job's paths, and only the progress queue can delay it.
	slowProgress time.Duration
}

func (f *fakeTasks) GetByID(context.Context, string) (*asynctasks.Task, error) {
	if f.get != nil {
		return nil, f.get
	}
	if f.task != nil {
		return f.task, nil
	}
	return &asynctasks.Task{ID: "t", Type: asynctasks.TypeMove, Username: "u"}, nil
}

func (f *fakeTasks) AddStatus(_ context.Context, _ string, status asynctasks.Status) error {
	if f.slowProgress > 0 && status.Status == asynctasks.StatusRunning {
		time.Sleep(f.slowProgress)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failuresLeft > 0 {
		f.failuresLeft--
		return errors.New("async-tasks is unhappy")
	}
	f.statuses = append(f.statuses, status)
	f.trail = append(f.trail, posted{status, time.Now()})
	return nil
}

func (f *fakeTasks) AddCompletedStatus(_ context.Context, _ string, status asynctasks.Status) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failuresLeft > 0 {
		f.failuresLeft--
		return errors.New("async-tasks is unhappy")
	}
	f.completed = append(f.completed, status)
	f.trail = append(f.trail, posted{status, time.Now()})
	return nil
}

// posted is one recorded status and when it arrived.
type posted struct {
	status asynctasks.Status
	at     time.Time
}

func (f *fakeTasks) recorded() []posted {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]posted(nil), f.trail...)
}

func (f *fakeTasks) terminal() []asynctasks.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]asynctasks.Status(nil), f.completed...)
}

func (f *fakeTasks) progress() []asynctasks.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]asynctasks.Status(nil), f.statuses...)
}

func testRunner(tasks Tasks) *Runner {
	discard := logrus.New()
	discard.SetOutput(io.Discard)
	return NewRunner(tasks, logrus.NewEntry(discard), "test-instance")
}

// shutdown drains the runner, failing the test rather than hanging if a job will not stop.
func shutdown(t *testing.T, r *Runner) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := r.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestRunnerReportsOutcomes(t *testing.T) {
	cases := []struct {
		name       string
		jobErr     error
		panics     bool
		wantStatus string
	}{
		{name: "a job that succeeds", wantStatus: asynctasks.StatusCompleted},
		{name: "a job that fails", jobErr: errors.New("iRODS said no"), wantStatus: asynctasks.StatusFailed},
		// Without a recover the process would exit, leaving the paths locked until
		// async-tasks gave up on the task.
		{name: "a job that panics", panics: true, wantStatus: asynctasks.StatusFailed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &fakeTasks{}
			runner := testRunner(tasks)

			if err := runner.Start("/tasks/abc", "move", JobFunc(
				func(context.Context, *asynctasks.Task, Progress) error {
					if tc.panics {
						panic("a bug")
					}
					return tc.jobErr
				},
			)); err != nil {
				t.Fatalf("Start: %v", err)
			}
			shutdown(t, runner)

			terminal := tasks.terminal()
			if len(terminal) != 1 {
				t.Fatalf("terminal statuses = %d, want exactly one", len(terminal))
			}
			if terminal[0].Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", terminal[0].Status, tc.wantStatus)
			}

			progress := tasks.progress()
			if len(progress) == 0 || progress[0].Status != asynctasks.StatusStarted {
				t.Errorf("first status = %+v, want %q", progress, asynctasks.StatusStarted)
			}
		})
	}
}

// panickingNotifier is a job that succeeds and then panics while telling its user so.
type panickingNotifier struct{ JobFunc }

func (panickingNotifier) Notify(context.Context, *asynctasks.Task, bool) { panic("a bug") }

func TestANotificationPanicIsContained(t *testing.T) {
	tasks := &fakeTasks{}
	runner := testRunner(tasks)

	job := panickingNotifier{JobFunc(func(context.Context, *asynctasks.Task, Progress) error { return nil })}
	if err := runner.Start("/tasks/abc", "move", job); err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdown(t, runner)

	terminal := tasks.terminal()
	if len(terminal) != 1 || terminal[0].Status != asynctasks.StatusCompleted {
		t.Errorf("terminal statuses = %+v, want one %q", terminal, asynctasks.StatusCompleted)
	}
}

// The whole point of the package: a job that is cancelled mid-flight must still reach a
// terminal status, because that is what sets the end date and releases its paths.
func TestShutdownReleasesARunningJob(t *testing.T) {
	tasks := &fakeTasks{}
	runner := testRunner(tasks)

	started := make(chan struct{})
	if err := runner.Start("/tasks/abc", "move", JobFunc(
		func(ctx context.Context, _ *asynctasks.Task, _ Progress) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	)); err != nil {
		t.Fatalf("Start: %v", err)
	}

	<-started
	shutdown(t, runner)

	terminal := tasks.terminal()
	if len(terminal) != 1 {
		t.Fatalf("terminal statuses = %d, want exactly one", len(terminal))
	}
	if terminal[0].Status != asynctasks.StatusFailed {
		t.Errorf("status = %q, want %q", terminal[0].Status, asynctasks.StatusFailed)
	}
}

// The terminal status is posted outside the runner's context. If it used that context, the
// cancellation that stops the job would also cancel the report that unlocks its paths.
func TestTerminalStatusSurvivesCancellation(t *testing.T) {
	posted := make(chan error, 1)
	tasks := &cancellationWatcher{posted: posted}
	runner := testRunner(tasks)

	if err := runner.Start("/tasks/abc", "move", JobFunc(
		func(ctx context.Context, _ *asynctasks.Task, _ Progress) error {
			<-ctx.Done()
			return ctx.Err()
		},
	)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdown(t, runner)

	select {
	case err := <-posted:
		if err != nil {
			t.Fatalf("the terminal status was posted with a cancelled context: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the terminal status was never posted")
	}
}

// cancellationWatcher reports the context state seen by the terminal post.
type cancellationWatcher struct {
	fakeTasks
	posted chan error
}

func (c *cancellationWatcher) AddCompletedStatus(ctx context.Context, id string, status asynctasks.Status) error {
	c.posted <- ctx.Err()
	return c.fakeTasks.AddCompletedStatus(ctx, id, status)
}

func TestTerminalStatusIsRetriedPastAFailure(t *testing.T) {
	// Enough failures to consume the "started" post and several terminal attempts, but
	// well inside the terminal budget.
	tasks := &fakeTasks{failuresLeft: 4}
	runner := testRunner(tasks)

	if err := runner.Start("/tasks/abc", "move", JobFunc(
		func(context.Context, *asynctasks.Task, Progress) error { return nil },
	)); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := runner.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	if terminal := tasks.terminal(); len(terminal) != 1 {
		t.Fatalf("terminal statuses = %d, want exactly one after the retries", len(terminal))
	}
}

func TestProgressNeverBlocksTheJob(t *testing.T) {
	tasks := &fakeTasks{}
	runner := testRunner(tasks)

	finished := make(chan struct{})
	if err := runner.Start("/tasks/abc", "move", JobFunc(
		func(_ context.Context, _ *asynctasks.Task, progress Progress) error {
			// Far more updates than the buffer holds. The excess is dropped; what must not
			// happen is the job stalling.
			for i := range progressBuffer * 20 {
				progress("/z/home/u/a", "moved")
				_ = i
			}
			close(finished)
			return nil
		},
	)); err != nil {
		t.Fatalf("Start: %v", err)
	}

	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("the job stalled reporting progress")
	}
	shutdown(t, runner)
}

func TestStartIsRefusedDuringShutdown(t *testing.T) {
	runner := testRunner(&fakeTasks{})
	shutdown(t, runner)

	err := runner.Start("/tasks/abc", "move", JobFunc(
		func(context.Context, *asynctasks.Task, Progress) error { return nil },
	))
	if err == nil {
		t.Fatal("Start accepted a job while the runner was shutting down")
	}
}

// A task that cannot be read has no username and no data, so the job cannot run and nothing
// else will pick it up. Recording it as failed is what releases its paths; leaving it alone
// would lock them for good.
func TestAnUnreadableTaskIsStillReleased(t *testing.T) {
	tasks := &fakeTasks{get: errors.New("no such task")}
	runner := testRunner(tasks)

	ran := false
	if err := runner.Start("/tasks/abc", "move", JobFunc(
		func(context.Context, *asynctasks.Task, Progress) error {
			ran = true
			return nil
		},
	)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdown(t, runner)

	if ran {
		t.Error("the job ran although its task could not be read")
	}

	terminal := tasks.terminal()
	if len(terminal) != 1 {
		t.Fatalf("terminal statuses = %+v, want exactly one so the paths are released", terminal)
	}
	if terminal[0].Status != asynctasks.StatusFailed {
		t.Errorf("status = %q, want %q", terminal[0].Status, asynctasks.StatusFailed)
	}
}

// Reading the task must not use the context Shutdown cancels. A job started by a request that
// was still in flight when the listener closed would otherwise fail to read its own task and
// leave it locked -- caused by the shutdown meant to release it.
func TestATaskStartedDuringShutdownIsStillRead(t *testing.T) {
	tasks := &fakeTasks{}
	runner := testRunner(tasks)

	if err := runner.Start("/tasks/abc", "move", JobFunc(
		func(context.Context, *asynctasks.Task, Progress) error { return nil },
	)); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Cancel immediately, racing the fetch the goroutine is about to make.
	shutdown(t, runner)

	terminal := tasks.terminal()
	if len(terminal) != 1 {
		t.Fatalf("terminal statuses = %+v, want exactly one", terminal)
	}
	if terminal[0].Status == asynctasks.StatusFailed &&
		strings.Contains(terminal[0].Detail, "could not read the task") {
		t.Error("the task could not be read because shutdown had cancelled the fetch context")
	}
}

// A job may report progress from more than one goroutine -- a tree walk is the obvious case --
// and the reporter must not panic when one of them races the job returning.
func TestProgressIsSafeFromSeveralGoroutines(t *testing.T) {
	tasks := &fakeTasks{}
	runner := testRunner(tasks)

	if err := runner.Start("/tasks/abc", "move", JobFunc(
		func(_ context.Context, _ *asynctasks.Task, progress Progress) error {
			var reporting sync.WaitGroup
			for range 8 {
				reporting.Add(1)
				go func() {
					defer reporting.Done()
					for range 200 {
						progress("/z/home/u/a", "moved")
					}
				}()
			}

			// Return without waiting, so the helpers are still reporting when the reporter
			// is stopped. A send racing the close would panic the process.
			go reporting.Wait()
			return nil
		},
	)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdown(t, runner)
}

// TestProgressIsFlushedBeforeTheTerminalStatus covers the status trail, which is contract:
// terrain's move poller reads it to show a user what an operation is doing.
//
// A fast job finishes before the background sender has posted anything, so discarding the
// buffer at that point loses the whole trail. A shadow run against QA reported exactly that
// -- a rename came back as "begin" then "completed", three statuses short of the Clojure
// service.
func TestProgressIsFlushedBeforeTheTerminalStatus(t *testing.T) {
	tasks := &fakeTasks{}
	runner := testRunner(tasks)

	if err := runner.Start("/tasks/abc", "move", JobFunc(
		func(_ context.Context, _ *asynctasks.Task, progress Progress) error {
			for _, action := range []string{"begin", "validated-path-lengths", "did-rename", "end"} {
				progress("/a/path", action)
			}
			return nil
		},
	)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdown(t, runner)

	var actions []string
	for _, s := range tasks.progress() {
		if s.Status == asynctasks.StatusRunning {
			actions = append(actions, s.Detail)
		}
	}

	want := []string{
		"[test-instance] /a/path: begin",
		"[test-instance] /a/path: validated-path-lengths",
		"[test-instance] /a/path: did-rename",
		"[test-instance] /a/path: end",
	}
	if len(actions) != len(want) {
		t.Fatalf("progress = %v, want %v", actions, want)
	}
	for i := range want {
		if actions[i] != want[i] {
			t.Errorf("progress[%d] = %q, want %q", i, actions[i], want[i])
		}
	}

	if terminal := tasks.terminal(); len(terminal) != 1 || terminal[0].Status != asynctasks.StatusCompleted {
		t.Errorf("terminal = %v, want one completed status", terminal)
	}
}

// TestProgressFlushDoesNotOutlastItsDeadline is the other half of the same decision. The
// terminal status is the only thing that releases the job's paths, so a slow async-tasks
// must not be able to hold them: the flush gives up and lets it through.
func TestProgressFlushDoesNotOutlastItsDeadline(t *testing.T) {
	// A full buffer at this rate is over a minute of draining, against a five-second bound.
	tasks := &fakeTasks{slowProgress: time.Second}
	runner := testRunner(tasks)

	start := time.Now()
	if err := runner.Start("/tasks/abc", "move", JobFunc(
		func(_ context.Context, _ *asynctasks.Task, progress Progress) error {
			for i := 0; i < progressBuffer; i++ {
				progress("/a/path", "step")
			}
			return nil
		},
	)); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Waited for directly rather than through Shutdown, whose own budget is the same order
	// as the flush deadline and would be what the test measured.
	deadline := time.Now().Add(30 * time.Second)
	for len(tasks.terminal()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the terminal status never arrived; the flush deadline did not bound it")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The bound plus one post already in flight, with room to spare. Unbounded draining
	// would be progressBuffer seconds.
	if elapsed := time.Since(start); elapsed > progressFlushDeadline+5*time.Second {
		t.Errorf("the terminal status took %s, want under %s", elapsed, progressFlushDeadline+5*time.Second)
	}

	// And the trail is not empty: the flush posts what it can before giving up.
	if len(tasks.progress()) < 2 {
		t.Errorf("progress = %d statuses, want the flush to have landed some", len(tasks.progress()))
	}
}

// notifyingJob records how many terminal statuses had been posted when it was told to notify.
type notifyingJob struct {
	err   error
	tasks *fakeTasks

	mu             sync.Mutex
	notified       int
	failed         bool
	terminalBefore int
}

func (j *notifyingJob) Run(context.Context, *asynctasks.Task, Progress) error { return j.err }

func (j *notifyingJob) Notify(_ context.Context, _ *asynctasks.Task, failed bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.notified++
	j.failed = failed
	j.terminalBefore = len(j.tasks.terminal())
}

func TestNotifyComesAfterTheTerminalStatus(t *testing.T) {
	cases := []struct {
		name   string
		jobErr error
	}{
		{name: "a job that succeeds"},
		{name: "a job that fails", jobErr: errors.New("iRODS said no")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &fakeTasks{}
			job := &notifyingJob{err: tc.jobErr, tasks: tasks}
			runner := testRunner(tasks)

			if err := runner.Start("/tasks/abc", "move", job); err != nil {
				t.Fatalf("Start: %v", err)
			}
			shutdown(t, runner)

			job.mu.Lock()
			defer job.mu.Unlock()
			if job.notified != 1 {
				t.Fatalf("notified %d times, want once", job.notified)
			}
			if job.terminalBefore != 1 {
				t.Errorf("notified with %d terminal statuses posted, want 1", job.terminalBefore)
			}
			if job.failed != (tc.jobErr != nil) {
				t.Errorf("failed = %v, want %v", job.failed, tc.jobErr != nil)
			}
		})
	}
}

func isHeartbeat(s asynctasks.Status) bool {
	return s.Status == asynctasks.StatusRunning && strings.Contains(s.Detail, "still working; last reported")
}

// TestHeartbeat covers the job the stall timeout would otherwise release while it works: one
// busy in a single long iRODS call, reporting nothing until it returns.
func TestHeartbeat(t *testing.T) {
	cases := []struct {
		name          string
		interval      time.Duration
		maxSilence    time.Duration
		slowProgress  time.Duration
		run           func(Progress)
		minHeartbeats int
		maxHeartbeats int
		// heartbeatsBy, when set, is how long after the start every heartbeat must arrive.
		heartbeatsBy time.Duration
	}{
		{
			name:          "a silent job is reported as still running",
			interval:      20 * time.Millisecond,
			maxSilence:    time.Hour,
			run:           func(Progress) { time.Sleep(200 * time.Millisecond) },
			minHeartbeats: 3,
			maxHeartbeats: 10,
		},
		{
			name:       "a job that reports progress gets no heartbeat",
			interval:   50 * time.Millisecond,
			maxSilence: time.Hour,
			run: func(progress Progress) {
				for range 40 {
					progress("/zone/a", "working")
					time.Sleep(5 * time.Millisecond)
				}
			},
			maxHeartbeats: 0,
		},
		{
			// Past the cap the job is treated as hung, so the stall timeout can release it.
			name:          "the heartbeat stops once the job has been silent too long",
			interval:      10 * time.Millisecond,
			maxSilence:    60 * time.Millisecond,
			run:           func(Progress) { time.Sleep(300 * time.Millisecond) },
			minHeartbeats: 1,
			maxHeartbeats: 8,
			heartbeatsBy:  60*time.Millisecond + 30*time.Millisecond,
		},
		{
			// Posts are serialised, so a slow async-tasks spaces heartbeats out rather than
			// stacking them up.
			name:          "a slow async-tasks does not stack heartbeats up",
			interval:      5 * time.Millisecond,
			maxSilence:    time.Hour,
			slowProgress:  50 * time.Millisecond,
			run:           func(Progress) { time.Sleep(200 * time.Millisecond) },
			minHeartbeats: 1,
			maxHeartbeats: 5,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &fakeTasks{slowProgress: tc.slowProgress}
			discard := logrus.New()
			discard.SetOutput(io.Discard)
			runner := NewRunner(tasks, logrus.NewEntry(discard), "test-instance",
				WithHeartbeat(tc.interval, tc.maxSilence))

			start := time.Now()
			if err := runner.Start("/tasks/abc", "delete", JobFunc(
				func(_ context.Context, _ *asynctasks.Task, progress Progress) error {
					tc.run(progress)
					return nil
				},
			)); err != nil {
				t.Fatalf("Start: %v", err)
			}
			shutdown(t, runner)

			trail := tasks.recorded()
			if len(trail) == 0 || trail[len(trail)-1].status.Status != asynctasks.StatusCompleted {
				t.Fatalf("the terminal status must be the last one recorded: %+v", trail)
			}

			heartbeats := 0
			for _, p := range trail {
				if !isHeartbeat(p.status) {
					continue
				}
				heartbeats++
				if tc.heartbeatsBy > 0 && p.at.Sub(start) > tc.heartbeatsBy {
					t.Errorf("a heartbeat arrived %s after the start, past the cap", p.at.Sub(start))
				}
			}
			if heartbeats < tc.minHeartbeats || heartbeats > tc.maxHeartbeats {
				t.Errorf("heartbeats = %d, want between %d and %d", heartbeats, tc.minHeartbeats, tc.maxHeartbeats)
			}
		})
	}
}

// TestHeartbeatNamesTheLastReportedStep is what an operator reads to see where a long job is.
func TestHeartbeatNamesTheLastReportedStep(t *testing.T) {
	tasks := &fakeTasks{}
	discard := logrus.New()
	discard.SetOutput(io.Discard)
	runner := NewRunner(tasks, logrus.NewEntry(discard), "test-instance",
		WithHeartbeat(10*time.Millisecond, time.Hour))

	if err := runner.Start("/tasks/abc", "delete", JobFunc(
		func(_ context.Context, _ *asynctasks.Task, progress Progress) error {
			progress("/zone/home/u/big", "begin")
			time.Sleep(100 * time.Millisecond)
			return nil
		},
	)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdown(t, runner)

	want := "[test-instance] still working; last reported /zone/home/u/big: begin"
	for _, s := range tasks.progress() {
		if isHeartbeat(s) {
			if s.Detail != want {
				t.Errorf("detail = %q, want %q", s.Detail, want)
			}
			return
		}
	}
	t.Error("no heartbeat was posted")
}

// TestAShutdownSaysTheJobWasInterrupted: the user should check what iRODS already did before
// retrying, since a cancelled job's iRODS call may have finished some of its work.
func TestAShutdownSaysTheJobWasInterrupted(t *testing.T) {
	tasks := &fakeTasks{}
	runner := testRunner(tasks)

	if err := runner.Start("/tasks/abc", "delete", JobFunc(
		func(ctx context.Context, _ *asynctasks.Task, _ Progress) error {
			<-ctx.Done()
			return ctx.Err()
		},
	)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdown(t, runner)

	terminal := tasks.terminal()
	if len(terminal) != 1 || !strings.Contains(terminal[0].Detail, "interrupted by shutdown") {
		t.Errorf("terminal statuses = %+v, want one saying the job was interrupted by shutdown", terminal)
	}
}

// TestHeartbeatsAreOneIntervalApart pins the spacing. A ticker checking for silence on its own
// cadence spaced them two intervals apart, which halves the margin under the stall timeout.
func TestHeartbeatsAreOneIntervalApart(t *testing.T) {
	const interval = 40 * time.Millisecond

	tasks := &fakeTasks{}
	discard := logrus.New()
	discard.SetOutput(io.Discard)
	runner := NewRunner(tasks, logrus.NewEntry(discard), "test-instance", WithHeartbeat(interval, time.Hour))

	if err := runner.Start("/tasks/abc", "delete", JobFunc(
		func(_ context.Context, _ *asynctasks.Task, _ Progress) error {
			time.Sleep(10 * interval)
			return nil
		},
	)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdown(t, runner)

	var beats []time.Time
	for _, p := range tasks.recorded() {
		if isHeartbeat(p.status) {
			beats = append(beats, p.at)
		}
	}
	if len(beats) < 3 {
		t.Fatalf("heartbeats = %d, want at least 3", len(beats))
	}
	for i := 1; i < len(beats); i++ {
		if gap := beats[i].Sub(beats[i-1]); gap > interval*3/2 {
			t.Errorf("heartbeats %d and %d are %s apart, want about %s", i-1, i, gap, interval)
		}
	}
}
