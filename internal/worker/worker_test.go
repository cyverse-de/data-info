package worker

import (
	"context"
	"errors"
	"io"
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

	// failuresLeft makes the next N posts fail, to exercise the retry budgets.
	failuresLeft int
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
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failuresLeft > 0 {
		f.failuresLeft--
		return errors.New("async-tasks is unhappy")
	}
	f.statuses = append(f.statuses, status)
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
	return nil
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
		wantStatus string
	}{
		{name: "a job that succeeds", wantStatus: asynctasks.StatusCompleted},
		{name: "a job that fails", jobErr: errors.New("iRODS said no"), wantStatus: asynctasks.StatusFailed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &fakeTasks{}
			runner := testRunner(tasks)

			if err := runner.Start("/tasks/abc", "move", JobFunc(
				func(context.Context, *asynctasks.Task, Progress) error { return tc.jobErr },
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

// A task that cannot be read has no username and no data, so there is nothing to run. It must
// not be reported as completed: doing so would release paths that were never touched, and
// hide a record that needs a person.
func TestAnUnreadableTaskIsNotCompleted(t *testing.T) {
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
	if terminal := tasks.terminal(); len(terminal) != 0 {
		t.Errorf("terminal statuses = %+v, want none", terminal)
	}
}
