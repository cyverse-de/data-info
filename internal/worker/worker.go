// Package worker runs the work that outlives the request which asked for it.
//
// Everything here exists to keep one invariant: a task that starts must reach a terminal
// status. An absent end date is what holds the lock on a task's paths, so a job that dies
// without reporting leaves those paths unusable -- and the async-tasks stall timeout does
// not rescue it, because the behaviour this service registers does not ask for the task to
// be completed. See docs/deferred-fixes.md.
package worker

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/cyverse-de/data-info/internal/clients/asynctasks"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// Retry budgets. Progress is telemetry and a lost update costs nothing; the terminal status
// is what releases the lock, so it is retried until it is absurd to keep trying.
const (
	progressRetries = 3
	terminalRetries = 100
	retryPause      = 2 * time.Second
)

// progressBuffer is how many progress updates may be waiting to be sent before new ones are
// dropped. Progress is reported per path, so a job over a large tree can produce them far
// faster than they can be posted; dropping is the intended behaviour, because a status the
// caller never sees is better than a job that runs at the speed of an HTTP round trip.
const progressBuffer = 64

// Tasks is the part of the async-tasks client a runner needs.
type Tasks interface {
	GetByID(ctx context.Context, id string) (*asynctasks.Task, error)
	AddStatus(ctx context.Context, id string, status asynctasks.Status) error
	AddCompletedStatus(ctx context.Context, id string, status asynctasks.Status) error
}

// Progress reports what a job is doing, one path at a time.
//
// Calls never block and are dropped under load, so a job may use it as freely as it likes.
type Progress func(path, action string)

// Job is the work a task stands for.
//
// A job must return when its context is cancelled. That is not a nicety: shutdown cancels it,
// and a job that ignores the cancellation is one whose paths stay locked after the pod goes
// away.
type Job interface {
	Run(ctx context.Context, task *asynctasks.Task, progress Progress) error
}

// JobFunc adapts a function to Job.
type JobFunc func(ctx context.Context, task *asynctasks.Task, progress Progress) error

// Run implements Job.
func (f JobFunc) Run(ctx context.Context, task *asynctasks.Task, progress Progress) error {
	return f(ctx, task, progress)
}

// Runner starts jobs and sees them through to a terminal status.
type Runner struct {
	tasks Tasks
	log   *logrus.Entry

	// instance names this replica in status details, so an operator reading a task's
	// history can tell which pod was doing the work.
	instance string

	// ctx is cancelled on shutdown and is what every running job is given.
	ctx    context.Context
	cancel context.CancelFunc

	running sync.WaitGroup

	mu       sync.Mutex
	stopping bool
}

// NewRunner builds a runner. Call Shutdown before the process exits.
func NewRunner(tasks Tasks, log *logrus.Entry, instance string) *Runner {
	ctx, cancel := context.WithCancel(context.Background())

	return &Runner{
		tasks:    tasks,
		log:      log,
		instance: instance,
		ctx:      ctx,
		cancel:   cancel,
	}
}

// InstanceID names this process the way the reference names it: by hostname, which in a
// cluster is the pod name.
func InstanceID() string {
	if host, err := os.Hostname(); err == nil && host != "" {
		return host
	}
	// Nothing reads this for correctness -- it only has to distinguish one replica's status
	// lines from another's -- so an identifier that is merely unique will do.
	return uuid.NewString()
}

// Start runs a job for a task in the background.
//
// It returns as soon as the job is dispatched; the task id is how the caller follows it. A
// runner that is already shutting down refuses, so a request arriving during a rollout is
// told to try again rather than having its work started and immediately abandoned.
func (r *Runner) Start(taskID, name string, job Job) error {
	r.mu.Lock()
	if r.stopping {
		r.mu.Unlock()
		return fmt.Errorf("worker: refusing to start %s: this instance is shutting down", name)
	}
	r.running.Add(1)
	r.mu.Unlock()

	go func() {
		defer r.running.Done()
		r.run(taskID, name, job)
	}()

	return nil
}

// run drives one task from "started" to a terminal status.
func (r *Runner) run(taskID, name string, job Job) {
	log := r.log.WithFields(logrus.Fields{"task": asynctasks.NormalizeID(taskID), "job": name})

	task, err := r.tasks.GetByID(r.ctx, taskID)
	if err != nil {
		// Without the task there is no username and no data, so there is nothing to run.
		// The record still exists with no end date and its paths stay locked, which is why
		// this is reported loudly: it needs a person.
		log.WithError(err).Error("could not read the task to run; " +
			"its paths stay locked until it is completed by hand")
		return
	}

	r.post(log, taskID, asynctasks.Status{Status: asynctasks.StatusStarted}, false, progressRetries)

	progress, done := r.progressReporter(log, taskID)

	runErr := job.Run(r.ctx, task, progress)

	// Stop accepting progress before the terminal status, so the history cannot end with a
	// "running" line posted after the job finished.
	done()

	if runErr != nil {
		log.WithError(runErr).Error("the job failed; its paths are being released")
		r.post(log, taskID, asynctasks.Status{
			Status: asynctasks.StatusFailed,
			Detail: fmt.Sprintf("[%s] %v", r.instance, runErr),
		}, true, terminalRetries)
		return
	}

	r.post(log, taskID, asynctasks.Status{
		Status: asynctasks.StatusCompleted,
		Detail: "[" + r.instance + "]",
	}, true, terminalRetries)
	log.Info("finished")
}

// progressReporter returns a Progress that posts in the background, and a function that
// drains and stops it.
func (r *Runner) progressReporter(log *logrus.Entry, taskID string) (Progress, func()) {
	updates := make(chan asynctasks.Status, progressBuffer)

	var sending sync.WaitGroup
	sending.Add(1)
	go func() {
		defer sending.Done()
		for status := range updates {
			r.post(log, taskID, status, false, progressRetries)
		}
	}()

	var once sync.Once
	closed := make(chan struct{})

	progress := func(path, action string) {
		select {
		case <-closed:
			return
		default:
		}

		status := asynctasks.Status{
			Status: asynctasks.StatusRunning,
			Detail: fmt.Sprintf("[%s] %s: %s", r.instance, path, action),
		}

		// Never block the job. A dropped progress line is invisible to everyone except
		// somebody reading the history closely; a blocked job holds its paths.
		select {
		case updates <- status:
		default:
			log.WithField("path", path).Debug("dropped a progress update: the sender is behind")
		}
	}

	stop := func() {
		once.Do(func() {
			close(closed)
			close(updates)
			sending.Wait()
		})
	}

	return progress, stop
}

// post records a status, retrying.
//
// A terminal status is posted outside the runner's context. Shutdown cancels that context to
// stop the job, and using it here would cancel the very report that unlocks the job's paths
// -- turning a clean shutdown into the exact leak it is meant to prevent.
func (r *Runner) post(log *logrus.Entry, taskID string, status asynctasks.Status, terminal bool, attempts int) {
	ctx := r.ctx
	if terminal {
		ctx = context.WithoutCancel(r.ctx)
	}

	var err error
	for attempt := range attempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				// Only reachable for non-terminal statuses, which nothing depends on.
				return
			case <-time.After(retryPause):
			}
		}

		post := r.tasks.AddStatus
		if terminal {
			post = r.tasks.AddCompletedStatus
		}

		if err = post(ctx, taskID, status); err == nil {
			return
		}
	}

	entry := log.WithError(err).WithField("status", status.Status)
	if terminal {
		entry.Error("could not record the task's final status after every retry; " +
			"the task has no end date, so its paths stay locked until it is completed by hand")
		return
	}
	entry.Warn("could not record a progress status; the task itself is unaffected")
}

// Shutdown stops accepting jobs, cancels the ones running, and waits for them to report.
//
// This is what keeps a rollout from locking paths. Each job sees its context cancelled,
// returns, and is recorded as failed -- which sets the end date and releases its paths. A job
// that does not return in time is abandoned, and its paths stay locked, so the deadline here
// wants to be generous relative to how long a job takes to notice cancellation.
func (r *Runner) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	r.stopping = true
	r.mu.Unlock()

	r.cancel()

	finished := make(chan struct{})
	go func() {
		r.running.Wait()
		close(finished)
	}()

	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("worker: jobs were still running at shutdown; their paths stay locked: %w", ctx.Err())
	}
}
