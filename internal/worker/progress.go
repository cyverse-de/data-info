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

// progressReporter returns a Progress that posts in the background, and a stop func that
// drains the buffer for up to progressFlushDeadline. The drain exists because terrain's move
// poller reads the status trail; the bound, because the terminal status that releases the
// paths must not wait on a degraded async-tasks.
func (r *Runner) progressReporter(log *logrus.Entry, taskID string) (Progress, func()) {
	updates := make(chan asynctasks.Status, progressBuffer)

	// closed stops new sends; stopped tells the sender to discard what is queued, and is set
	// only when the flush times out. Both are written under mu so no send races the close.
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

// send posts a job's progress updates until the channel closes, and a heartbeat whenever the
// job has gone a heartbeat interval without one. Sharing this goroutine serialises heartbeats
// with progress posts, so none can follow the terminal status.
func (r *Runner) send(
	log *logrus.Entry,
	taskID string,
	updates <-chan asynctasks.Status,
	state func() (closing, discard bool),
) {
	// A timer reset after every post, not a ticker: a ticker's own cadence narrowly misses
	// the silence check and spaces heartbeats two intervals apart.
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

// post records a status, retrying. A terminal status uses a context Shutdown doesn't cancel,
// since it is what unlocks the job's paths.
func (r *Runner) post(log *logrus.Entry, taskID string, status asynctasks.Status, terminal bool, attempts int) {
	ctx := r.ctx
	if terminal {
		var release context.CancelFunc
		ctx, release = context.WithTimeout(context.WithoutCancel(r.ctx), r.terminalDeadline)
		defer release()
	}

	var err error
retries:
	for attempt := range attempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				if !terminal {
					return // shutdown cancelled a progress post, which nothing depends on
				}
				break retries
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
		entry.Error("could not record the task's final status before giving up; " +
			"the task has no end date, so its paths stay locked until the async-tasks stall timeout " +
			"completes it, about ten minutes; this usually means async-tasks was unreachable")
		return
	}
	entry.Warn("could not record a progress status; the task itself is unaffected")
}
