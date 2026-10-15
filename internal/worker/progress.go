package worker

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cyverse-de/data-info/internal/clients/asynctasks"
	"github.com/sirupsen/logrus"
)

// progressReporter returns a Progress that posts in the background, and a function that
// stops it and waits, briefly, for what is already buffered.
//
// The wait is bounded rather than absent or unlimited, and the bound is the whole design. The
// status trail is contract -- terrain's move poller reads it to show progress -- so
// discarding the buffer outright loses it: a fast job finishes before the sender has posted
// anything, and a rename came back with "begin" followed by "completed", three statuses short
// of what the Clojure service reports. Waiting without a bound is the other failure: the
// terminal status is the only thing that releases the job's paths, so a degraded async-tasks
// would hold them for as long as it stayed degraded.
//
// So: drain until progressFlushDeadline, then give up and let the terminal status through.
// A healthy async-tasks accepts these in milliseconds, which is the case that matters.
func (r *Runner) progressReporter(log *logrus.Entry, taskID string) (Progress, func()) {
	updates := make(chan asynctasks.Status, progressBuffer)

	// closed says the channel is shut and no further send may be attempted; stopped says
	// the sender should discard rather than post what is left. They are separate because
	// stopping accepts no new updates while still draining the ones already queued, and
	// only a flush that runs out of time sets the second.
	//
	// Both are guarded by mu on the writer side so that no send can race the close.
	var (
		mu      sync.RWMutex
		closed  bool
		stopped bool
	)

	var sending sync.WaitGroup
	sending.Add(1)
	go func() {
		defer sending.Done()
		r.send(log, taskID, updates, func() (closing, discard bool) {
			mu.RLock()
			defer mu.RUnlock()
			return closed, stopped
		})
	}()

	progress := func(path, action string) {
		// The read lock is held across the send. Without it a job reporting from a helper
		// goroutine could send on the channel between stop closing it and this returning,
		// which panics the process -- and reporting from more than one goroutine is exactly
		// what a tree walk does.
		mu.RLock()
		defer mu.RUnlock()

		if closed {
			return
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

	var once sync.Once
	stop := func() {
		once.Do(func() {
			// Closed first so the sender sees the end of the stream, but `stopped` is not
			// set yet: the sender keeps posting what is already queued.
			mu.Lock()
			closed = true
			close(updates)
			mu.Unlock()

			flushed := make(chan struct{})
			go func() {
				sending.Wait()
				close(flushed)
			}()

			select {
			case <-flushed:
			case <-time.After(progressFlushDeadline):
				// Out of patience. Setting stopped makes the sender discard the rest, and
				// the terminal status goes out now rather than behind a queue nobody is
				// waiting on.
				mu.Lock()
				stopped = true
				mu.Unlock()
				log.Warn("gave up flushing progress updates before the terminal status; " +
					"async-tasks is probably slow or degraded, and this task's history " +
					"will be missing some of its running statuses")
				<-flushed
			}
		})
	}

	return progress, stop
}

// send posts a job's progress updates until the channel closes, and posts a heartbeat
// whenever the job has gone a heartbeat interval without one.
//
// Heartbeats are posted from this goroutine, not another, so they are serialised with the
// progress updates: one can never be in flight while another is, and none can follow the
// terminal status, which is posted only after this returns.
func (r *Runner) send(
	log *logrus.Entry,
	taskID string,
	updates <-chan asynctasks.Status,
	state func() (closing, discard bool),
) {
	// A timer reset after every post, not a ticker: a ticker checking "silent for an
	// interval" on its own cadence narrowly misses and waits a whole extra tick, which
	// spaced heartbeats two intervals apart.
	quiet := time.NewTimer(r.heartbeatInterval)
	defer quiet.Stop()

	// The "started" status has just been posted, which is where both clocks begin.
	lastReported := time.Now()
	lastDetail := "starting"
	gaveUp := false

	for {
		select {
		case status, ok := <-updates:
			if !ok {
				return
			}
			if _, discard := state(); discard {
				// Drain without posting, so the range ends promptly.
				continue
			}
			r.post(log, taskID, status, false, progressRetries)
			lastReported = time.Now()
			lastDetail = strings.TrimPrefix(status.Detail, "["+r.instance+"] ")
			quiet.Reset(r.heartbeatInterval)

		case <-quiet.C:
			quiet.Reset(r.heartbeatInterval)
			if closing, _ := state(); closing {
				continue
			}
			if time.Since(lastReported) > r.heartbeatMaxSilence {
				if !gaveUp {
					gaveUp = true
					log.Errorf("the job has reported nothing for %s, so its heartbeat has "+
						"stopped and the stall timeout will release its paths; it is probably "+
						"stuck in an iRODS call that will not return", r.heartbeatMaxSilence)
				}
				continue
			}
			r.heartbeat(log, taskID, lastDetail)
		}
	}
}

// heartbeat reports a silent job as still running, once.
func (r *Runner) heartbeat(log *logrus.Entry, taskID, lastDetail string) {
	ctx, cancel := context.WithTimeout(r.ctx, heartbeatTimeout)
	defer cancel()

	err := r.tasks.AddStatus(ctx, taskID, asynctasks.Status{
		Status: asynctasks.StatusRunning,
		Detail: fmt.Sprintf("[%s] still working; last reported %s", r.instance, lastDetail),
	})
	if err != nil {
		log.WithError(err).Warn("could not post a heartbeat; the next one will try again, " +
			"and the task is released only if several in a row fail")
	}
}

// post records a status, retrying.
//
// A terminal status is posted outside the runner's context. Shutdown cancels that context to
// stop the job, and using it here would cancel the very report that unlocks the job's paths
// -- turning a clean shutdown into the exact leak it is meant to prevent.
func (r *Runner) post(log *logrus.Entry, taskID string, status asynctasks.Status, terminal bool, attempts int) {
	ctx := r.ctx
	if terminal {
		var release context.CancelFunc
		ctx, release = context.WithTimeout(context.WithoutCancel(r.ctx), terminalDeadline)
		defer release()
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
