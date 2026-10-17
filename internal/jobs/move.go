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

// Run implements worker.Job. It stops at the first failure without moving anything back, since
// unwinding would rerun the permission repair that just failed.
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

	return m.move(ctx, scope, task.Username, sources, paths.DestinationsUnder(destination, sources), progress)
}

// Notify implements worker.Notifying. It names every requested path, not just those that
// moved; the task's status history shows how far it got.
func (m Move) Notify(ctx context.Context, task *asynctasks.Task, failed bool) {
	sources, err := stringsFrom(task.Data, "sources")
	if err != nil {
		return
	}
	destination, err := stringFrom(task.Data, "destination")
	if err != nil {
		return
	}

	m.Deps.notify(ctx, notifications.Move(task.Username, sources, paths.DestinationsUnder(destination, sources), failed))
}

func (m Move) move(
	ctx context.Context,
	scope *rods.Scope,
	user string,
	sources, destinations []string,
	progress worker.Progress,
) error {
	// Every destination is checked before anything moves, so one over-long name fails the
	// whole move rather than stranding it part way.
	progress(pathsetSeveral, actionBegin)
	for _, destination := range destinations {
		if err := checkLength(destination); err != nil {
			return err
		}
	}
	progress(pathsetSeveral, actionValidate)

	for i, source := range sources {
		if err := ctx.Err(); err != nil {
			// Shutdown: stop so the task gets recorded, which releases its remaining paths.
			return fmt.Errorf("the move was stopped after %d of %d paths: %w", i, len(sources), err)
		}

		if err := moveOne(ctx, scope, m.Deps, user, source, destinations[i], progress); err != nil {
			return err
		}
	}

	progress(pathsetSeveral, actionEnd)
	return nil
}
