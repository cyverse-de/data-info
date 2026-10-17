// Package worker runs the work that outlives the request which asked for it.
//
// Everything here exists to keep one invariant: a task that starts must reach a terminal
// status. An absent end date is what holds the lock on a task's paths, so a job that dies
// without reporting leaves those paths unusable.
//
// The async-tasks stall timeout is the other half of that, and only since this service began
// registering it with "complete" set (asynctasks.StallBehavior): it releases the paths of a
// task that stops reporting, which covers what a graceful shutdown cannot -- SIGKILL, an OOM,
// a node disappearing. This still matters because it is the difference between releasing
// those paths immediately and releasing them ten minutes later.
package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/cyverse-de/data-info/internal/clients/asynctasks"
	"github.com/cyverse-de/data-info/internal/panics"
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

// fetchTimeout bounds reading a task before running it. It is short because the work has not
// started yet: failing quickly and recording the task as failed releases its paths, where
// waiting only delays that.
const fetchTimeout = 10 * time.Second

// terminalDeadline bounds the whole retry sequence for a terminal status.
//
// The retry budget alone does not: a hundred attempts against an unreachable service, each
// waiting out its own HTTP timeout, would hold the job's goroutine for the best part of an
// hour and block any shutdown for just as long. The budget decides how hard to try; this
// decides when trying has stopped being useful.
const terminalDeadline = 2 * time.Minute

// progressBuffer is how many progress updates may be waiting to be sent before new ones are
// dropped. Progress is reported per path, so a job over a large tree can produce them far
// faster than they can be posted; dropping is the intended behaviour, because a status the
// caller never sees is better than a job that runs at the speed of an HTTP round trip.
const progressBuffer = 64

// progressFlushDeadline bounds how long a finished job waits for its buffered progress to be
// posted before the terminal status goes out regardless. Short, because the only thing on the
// other side of it is the release of the job's paths.
const progressFlushDeadline = 5 * time.Second

// Heartbeat defaults. The interval leaves four missed beats inside the ten-minute stall
// timeout. The maximum silence bounds how long a job that reports nothing is kept alive; it
// should match the longest a single iRODS call may run, which main passes in.
const (
	DefaultHeartbeatInterval   = 2 * time.Minute
	DefaultHeartbeatMaxSilence = 12 * time.Hour
)

// heartbeatTimeout bounds one heartbeat post. It is tried once: the next is only an interval
// away, and a missed one costs nothing while the stall timeout is several intervals long.
const heartbeatTimeout = 10 * time.Second

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

// Job is the work a task stands for. Run must return when ctx is cancelled, or shutdown
// cannot release the task's paths.
type Job interface {
	Run(ctx context.Context, task *asynctasks.Task, progress Progress) error
}

// Notifying is a Job that tells its user how it went. Notify runs after the terminal status is
// recorded, as the Clojure service's end-fn did, so a user acting on the notification straight
// away does not find the task's paths still locked.
type Notifying interface {
	Notify(ctx context.Context, task *asynctasks.Task, failed bool)
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

	// heartbeatInterval and heartbeatMaxSilence; see WithHeartbeat.
	heartbeatInterval   time.Duration
	heartbeatMaxSilence time.Duration
}

// Option configures a Runner.
type Option func(*Runner)

// WithHeartbeat sets how often a job that has posted nothing is reported as still running,
// and how long a job may stay silent before that stops.
//
// The heartbeat is what keeps the stall timeout from releasing the paths of a job that is
// busy in one long iRODS call, such as a recursive delete, which reports nothing until it
// returns. It shows only that the process is alive, not that the call is progressing, so
// maxSilence caps it: past that, a job is treated as hung and the stall timeout releases it.
func WithHeartbeat(interval, maxSilence time.Duration) Option {
	return func(r *Runner) {
		r.heartbeatInterval = interval
		r.heartbeatMaxSilence = maxSilence
	}
}

// NewRunner builds a runner. Call Shutdown before the process exits.
func NewRunner(tasks Tasks, log *logrus.Entry, instance string, opts ...Option) *Runner {
	ctx, cancel := context.WithCancel(context.Background())

	r := &Runner{
		tasks:               tasks,
		log:                 log,
		instance:            instance,
		ctx:                 ctx,
		cancel:              cancel,
		heartbeatInterval:   DefaultHeartbeatInterval,
		heartbeatMaxSilence: DefaultHeartbeatMaxSilence,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// InstanceID names this process the way the Clojure service names it: by hostname, which in a
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

	// Not r.ctx. Shutdown cancels that, and a job started by a request that was still in
	// flight when the listener closed would then fail to read its own task -- leaving the
	// record with no end date and its paths locked by the very shutdown meant to release
	// them.
	fetchCtx, cancelFetch := context.WithTimeout(context.WithoutCancel(r.ctx), fetchTimeout)
	task, err := r.tasks.GetByID(fetchCtx, taskID)
	cancelFetch()

	if err != nil {
		// Nothing else will run this task, so record it as failed now rather than leave its
		// paths locked until the stall timeout.
		log.WithError(err).Error("could not read the task to run; " +
			"recording it as failed so that its paths are released")
		r.post(log, taskID, asynctasks.Status{
			Status: asynctasks.StatusFailed,
			Detail: fmt.Sprintf("[%s] could not read the task: %v", r.instance, err),
		}, true, terminalRetries)
		return
	}

	r.post(log, taskID, asynctasks.Status{Status: asynctasks.StatusStarted}, false, progressRetries)

	progress, done := r.progressReporter(log, taskID)

	runErr := runJob(r.ctx, job, task, progress)

	// Stop accepting progress before the terminal status, so the history cannot end with a
	// "running" line posted after the job finished, and give what is already buffered a
	// bounded chance to land first.
	done()

	if runErr != nil {
		entry := log.WithError(runErr)
		var panicked *panics.Error
		if errors.As(runErr, &panicked) {
			entry = entry.WithField("stack", string(panicked.Stack))
		}
		entry.Error("the job failed; its paths are being released")
		detail := fmt.Sprintf("[%s] %v", r.instance, runErr)
		if r.ctx.Err() != nil {
			// The job stopped because this instance is shutting down, not because the
			// work failed. An iRODS call it was waiting on may still have finished some or
			// all of it, so the user should check before retrying.
			detail = fmt.Sprintf("[%s] interrupted by shutdown; iRODS may have finished part "+
				"of the work: %v", r.instance, runErr)
		}
		r.post(log, taskID, asynctasks.Status{
			Status: asynctasks.StatusFailed,
			Detail: detail,
		}, true, terminalRetries)
	} else {
		r.post(log, taskID, asynctasks.Status{
			Status: asynctasks.StatusCompleted,
			Detail: "[" + r.instance + "]",
		}, true, terminalRetries)
		log.Info("finished")
	}

	// Not r.ctx, which Shutdown cancels: the work is done and the user should still hear so.
	if n, ok := job.(Notifying); ok {
		notify(context.WithoutCancel(r.ctx), log, n, task, runErr != nil)
	}
}

// runJob runs a job, turning a panic into its error so that the task is still recorded as
// failed and its paths released.
func runJob(ctx context.Context, job Job, task *asynctasks.Task, progress Progress) (err error) {
	defer func() {
		if p := panics.From(recover()); p != nil {
			err = p
		}
	}()
	return job.Run(ctx, task, progress)
}

// notify sends a job's notification. A panic in it is logged rather than allowed to end the
// process, which would take every other job in flight with it.
func notify(ctx context.Context, log *logrus.Entry, n Notifying, task *asynctasks.Task, failed bool) {
	defer func() {
		if p := panics.From(recover()); p != nil {
			log.WithError(p).WithField("stack", string(p.Stack)).Error("sending the job's notification panicked")
		}
	}()
	n.Notify(ctx, task, failed)
}

// Shutdown stops accepting jobs, cancels the running ones, and waits until ctx for them to
// record a terminal status. A job still running at the deadline keeps its paths locked until
// the stall timeout.
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
		return fmt.Errorf("worker: jobs were still running at shutdown; their paths stay locked until the async-tasks stall timeout: %w", ctx.Err())
	}
}
