package jobs

import (
	"context"
	"fmt"

	"github.com/cyverse-de/data-info/internal/clients/asynctasks"
	"github.com/cyverse-de/data-info/internal/clients/notifications"
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/cyverse-de/data-info/internal/worker"
)

// Move moves several paths into one collection.
type Move struct{ Deps Deps }

// Run implements worker.Job.
//
// Paths are moved one at a time and the job stops at the first failure, leaving the ones
// already moved where they now are. That is the reference's behaviour and it is the safer of
// the two: unwinding would mean moving things back through the same permission repair that
// just failed, on a tree somebody may already be looking at.
func (m Move) Run(ctx context.Context, task *asynctasks.Task, progress worker.Progress) error {
	sources, err := stringsFrom(task.Data, "sources")
	if err != nil {
		return err
	}
	destination, err := stringFrom(task.Data, "destination")
	if err != nil {
		return err
	}

	scope, err := m.Deps.scopeFor(ctx, task)
	if err != nil {
		return err
	}
	defer scope.Close()

	destinations := DestinationsUnder(destination, sources)

	// Reported before anything moves, so a failure part-way through does not make the
	// notification claim more than happened.
	runErr := m.move(ctx, scope, task.Username, sources, destinations, progress)

	m.Deps.notify(ctx, notifications.Move(task.Username, sources, destinations, runErr != nil))
	return runErr
}

func (m Move) move(
	ctx context.Context,
	scope *rods.Scope,
	user string,
	sources, destinations []string,
	progress worker.Progress,
) error {
	for i, source := range sources {
		if err := ctx.Err(); err != nil {
			// Shutdown. Stopping here leaves what has already moved in place and lets the
			// task be recorded, which is what releases the rest of its paths.
			return fmt.Errorf("the move was stopped after %d of %d paths: %w", i, len(sources), err)
		}

		if err := moveOne(ctx, scope, m.Deps, user, source, destinations[i], progress); err != nil {
			return err
		}
	}
	return nil
}

// DestinationsUnder names where each source will land inside a collection.
//
// A move keeps each thing's own name, so the destination collection joined to the source's
// base name is where it goes -- and those are the paths the lock has to hold, not the
// collection itself, which usually already exists.
func DestinationsUnder(destination string, sources []string) []string {
	out := make([]string, 0, len(sources))
	for _, source := range sources {
		out = append(out, paths.Join(destination, paths.Base(source)))
	}
	return out
}
