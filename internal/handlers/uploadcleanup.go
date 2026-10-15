package handlers

import (
	"context"
	"time"
)

// tempCleanupSchedule is how long to wait before each attempt at removing an orphaned upload.
// iRODS holds a lock on a replica for a short while after a transfer aborts, so the first
// delete reliably fails; the waits back off to give the lock time to clear. It matches the
// Clojure service's schedule, which was tuned against a real server.
var tempCleanupSchedule = []time.Duration{
	3 * time.Second, 6 * time.Second, 12 * time.Second,
	24 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second,
}

// scheduleTempCleanup removes an orphaned temporary upload object in the background.
//
// It runs detached from the request: the request has already failed and its context is
// about to be cancelled, while the object may not be removable for another minute. Each
// attempt opens its own view, because the failure that orphaned the object usually broke
// the connection the upload was using.
//
// Best effort. A failure here leaves a hidden object behind rather than user-visible
// damage, so it is logged with what a sweep would need and never reported to the caller.
//
// The Clojure service registers this as a tracked async task so an operator can see it; this
// one does not yet, so the log is the only record. See deferred fix 20.
func (h *Writes) scheduleTempCleanup(user, path string) {
	log := h.deps.Logger().WithFields(map[string]any{"path": path, "user": user})

	go func() {
		budget := time.Minute
		for _, wait := range tempCleanupSchedule {
			budget += wait
		}

		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()

		for _, wait := range tempCleanupSchedule {
			select {
			case <-ctx.Done():
				log.Warn("gave up removing a partial upload: the cleanup budget expired; " +
					"the object is hidden but still counts against quota")
				return
			case <-time.After(wait):
			}

			outcome, err := h.removeTempObject(ctx, path)
			if err != nil {
				log.WithError(err).Debug("could not remove a partial upload yet; " +
					"iRODS usually still holds a lock on the replica this soon after an aborted transfer")
				continue
			}

			switch outcome {
			case cleanupRemoved:
				log.Info("removed a partial upload left by a failed transfer")
				return
			case cleanupAbsent:
				// The upload failed before it created anything -- a refused connection,
				// or a permission iRODS checked at open time. Saying it was removed would
				// invent an object that never existed.
				log.Debug("nothing to remove for a failed upload: no partial object was created")
				return
			case cleanupPending:
			}
		}

		log.Warn("gave up removing a partial upload after every attempt failed; " +
			"its replica is probably still locked, or the service account cannot delete it, " +
			"and the object needs removing by hand")
	}()
}

// cleanupOutcome is what one attempt at removing an orphaned upload achieved.
type cleanupOutcome int

const (
	// cleanupPending means the object is still there and the attempt should be repeated.
	cleanupPending cleanupOutcome = iota
	// cleanupRemoved means this attempt deleted it.
	cleanupRemoved
	// cleanupAbsent means there was nothing to delete.
	cleanupAbsent
)

// removeTempObject makes one attempt at deleting an orphaned upload object.
//
// As the service account, as the Clojure service does. The object may never have become the
// uploader's -- in QA a rule hands new objects in a home to its owner -- and then the uploader
// cannot remove it, so attempting it as them leaves a hidden object behind for good.
func (h *Writes) removeTempObject(ctx context.Context, path string) (cleanupOutcome, error) {
	scope, err := h.deps.OpenProxyScope(ctx)
	if err != nil {
		return cleanupPending, err
	}
	defer scope.Close()

	present, err := scope.FileExists(ctx, path)
	if err != nil {
		return cleanupPending, err
	}
	if !present {
		return cleanupAbsent, nil
	}

	// A hard delete first: a partial upload has no value to recover, and leaving it in the
	// trash would only move the problem. Falling back to the trash keeps the object out of
	// the way when the replica cannot be removed outright.
	if err := scope.DeleteFile(ctx, path, true); err != nil {
		if err := scope.DeleteFile(ctx, path, false); err != nil {
			return cleanupPending, err
		}
	}

	// Confirmed against the server rather than inferred from the delete returning nil: a
	// locked replica can accept the call and keep the object.
	stillThere, err := scope.FileExists(ctx, path)
	if err != nil {
		return cleanupPending, err
	}
	if stillThere {
		return cleanupPending, nil
	}
	return cleanupRemoved, nil
}
