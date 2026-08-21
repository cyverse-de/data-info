package jobs

import (
	"context"

	"github.com/cyverse-de/data-info/internal/clients/asynctasks"
	"github.com/cyverse-de/data-info/internal/clients/notifications"
	"github.com/cyverse-de/data-info/internal/worker"
)

// Rename moves one path to one other path.
//
// It is a separate job from Move rather than a move of one thing, because the two record
// different data and send different notifications -- and because the DE shows them
// differently to the user.
type Rename struct{ Deps Deps }

// Run implements worker.Job.
func (r Rename) Run(ctx context.Context, task *asynctasks.Task, progress worker.Progress) error {
	source, err := stringFrom(task.Data, "source")
	if err != nil {
		return err
	}
	destination, err := stringFrom(task.Data, "destination")
	if err != nil {
		return err
	}

	scope, err := r.Deps.scopeFor(ctx, task)
	if err != nil {
		return err
	}
	defer scope.Close()

	runErr := moveOne(ctx, scope, r.Deps, task.Username, source, destination, progress)

	r.Deps.notify(ctx, notifications.Rename(
		task.Username, []string{source}, []string{destination}, runErr != nil))
	return runErr
}
