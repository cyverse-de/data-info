package jobs

import (
	"context"

	"github.com/cyverse-de/data-info/internal/clients/asynctasks"
	"github.com/cyverse-de/data-info/internal/clients/notifications"
	"github.com/cyverse-de/data-info/internal/worker"
)

// Rename moves one path to another. It is separate from Move because it records different
// task data and sends a different notification.
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

	return moveOne(ctx, scope, r.Deps, task.Username, source, destination, progress)
}

// Notify implements worker.Notifying.
func (r Rename) Notify(ctx context.Context, task *asynctasks.Task, failed bool) {
	source, err := stringFrom(task.Data, "source")
	if err != nil {
		return
	}
	destination, err := stringFrom(task.Data, "destination")
	if err != nil {
		return
	}

	r.Deps.notify(ctx, notifications.Rename(task.Username, []string{source}, []string{destination}, failed))
}
