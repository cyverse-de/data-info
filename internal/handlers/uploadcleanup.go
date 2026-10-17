package handlers

import (
	"context"
	"time"
)

// tempCleanupSchedule backs off because iRODS keeps a replica locked briefly after a transfer
// aborts, so the first deletes usually fail. The waits are the Clojure service's, tuned
// against a real server.
var tempCleanupSchedule = []time.Duration{
	3 * time.Second, 6 * time.Second, 12 * time.Second,
	24 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second,
}

// scheduleTempCleanup removes an orphaned temporary upload in the background, detached from
// the failed request; each attempt opens a fresh view because the failure often broke the
// upload's connection. Failures are only logged (deferred fix 20).
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
				// The upload failed before creating anything.
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

// removeTempObject runs as the service account because the uploader may not own the object
// (a QA rule hands new objects in a home to its owner).
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

	// Hard delete first, since a partial upload isn't worth recovering; fall back to the trash
	// if the replica can't be removed.
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
