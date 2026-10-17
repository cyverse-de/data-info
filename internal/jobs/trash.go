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

// usageRoutingKeyPrefix routes a request for the data-usage service to recount a user's storage.
const usageRoutingKeyPrefix = "index.usage.data.user."

// Progress action names for restoring.
const (
	actionBeginRestore = "begin-restore"
	actionEndRestore   = "end-restore"
)

// EmptyTrash removes everything in a user's trash for good.
type EmptyTrash struct{ Deps Deps }

// Run implements worker.Job. It runs as the proxy account so items that reached the trash
// through a shared collection can still be removed, and it tries every path before failing.
func (e EmptyTrash) Run(ctx context.Context, task *asynctasks.Task, progress worker.Progress) error {
	trashPaths, err := stringsFrom(task.Data, "trash-paths")
	if err != nil {
		return err
	}

	scope, err := e.Deps.OpenScope(ctx, e.Deps.ProxyUser)
	if err != nil {
		return err
	}
	defer scope.Close()

	var failures int
	progress(pathsetTrash, actionBegin)

	for i, path := range trashPaths {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("emptying the trash was stopped after %d of %d paths: %w",
				i, len(trashPaths), err)
		}

		progress(path, actionBeginDelete)

		if err := scope.Delete(ctx, path, true); err != nil {
			failures++
			e.Deps.Log.WithError(err).WithField("path", path).
				Error("could not remove something from the trash; continuing with the rest")
			progress(path, actionErrorDeleting)
		}

		// Reported even after a failure, so error-deleting is always followed by end-delete.
		progress(path, actionEndDelete)
	}

	progress(pathsetTrash, actionEnd)

	var runErr error
	if failures > 0 {
		runErr = fmt.Errorf("%d of %d paths could not be removed from the trash", failures, len(trashPaths))
	}

	// Published even after a failure: some paths may have gone, and data-usage has no other
	// way to find out.
	if e.Deps.Publisher != nil {
		e.Deps.Publisher.Publish(ctx, usageRoutingKeyPrefix+task.Username, "Update requested by data-info")
	}

	return runErr
}

// Notify implements worker.Notifying.
func (e EmptyTrash) Notify(ctx context.Context, task *asynctasks.Task, failed bool) {
	e.Deps.notify(ctx, notifications.EmptyTrash(task.Username, e.Deps.Layout, failed))
}

// Restore takes paths back out of the trash.
type Restore struct{ Deps Deps }

// Run implements worker.Job.
func (r Restore) Run(ctx context.Context, task *asynctasks.Task, progress worker.Progress) error {
	trashPaths, err := stringsFrom(task.Data, "paths")
	if err != nil {
		return err
	}

	plans, err := restorePlansFrom(task.Data)
	if err != nil {
		return err
	}

	scope, err := r.Deps.scopeFor(ctx, task)
	if err != nil {
		return err
	}
	defer scope.Close()

	return r.restore(ctx, scope, task.Username, trashPaths, plans, progress)
}

// Notify implements worker.Notifying.
func (r Restore) Notify(ctx context.Context, task *asynctasks.Task, failed bool) {
	trashPaths, err := stringsFrom(task.Data, "paths")
	if err != nil {
		return
	}
	plans, err := restorePlansFrom(task.Data)
	if err != nil {
		return
	}

	destinations := make([]string, 0, len(trashPaths))
	for _, path := range trashPaths {
		destinations = append(destinations, plans[path])
	}
	r.Deps.notify(ctx, notifications.Restore(task.Username, trashPaths, destinations, failed))
}

func (r Restore) restore(
	ctx context.Context,
	scope *rods.Scope,
	user string,
	trashPaths []string,
	plans map[string]string,
	progress worker.Progress,
) error {
	progress(pathsetDeleted, actionBegin)

	for i, path := range trashPaths {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("the restore was stopped after %d of %d paths: %w", i, len(trashPaths), err)
		}

		destination, ok := plans[path]
		if !ok || destination == "" {
			return fmt.Errorf("the task records no destination for %q", path)
		}

		progress(path, actionBeginRestore)

		// Re-checked because the plan was made when the request arrived, possibly long ago.
		stat, err := scope.Stat(ctx, destination).Get(ctx)
		if err != nil {
			return err
		}
		if stat.Exists {
			return fmt.Errorf("%q is already there, so %q cannot be restored to it", destination, path)
		}

		if err := r.restoreParents(ctx, scope, user, destination); err != nil {
			return err
		}

		if err := moveOne(ctx, scope, r.Deps, user, path, destination, progress); err != nil {
			return err
		}

		progress(path, actionEndRestore)
	}

	progress(pathsetDeleted, actionEnd)
	return nil
}

// restoreParents recreates missing parent collections and makes the user own each one it
// created (up to their home), so they can write where the restored item lands.
func (r Restore) restoreParents(ctx context.Context, scope *rods.Scope, user, destination string) error {
	parent := paths.Dir(destination)

	stat, err := scope.Stat(ctx, parent).Get(ctx)
	if err != nil {
		return err
	}
	if stat.Exists {
		return nil
	}

	extant, err := firstExtantAncestor(ctx, scope, parent)
	if err != nil {
		return err
	}

	if err := scope.MakeDir(ctx, parent, true); err != nil {
		return fmt.Errorf("recreating %q: %w", parent, err)
	}

	home := r.Deps.Layout.UserHome(user)
	for current := parent; current != extant && current != home; current = paths.Dir(current) {
		owned, err := scope.Stat(ctx, current).Get(ctx)
		if err != nil {
			return err
		}
		if owned.Permission == rods.PermissionOwn {
			continue
		}
		if err := scope.SetOwner(ctx, current, user, false); err != nil {
			return fmt.Errorf("giving %q ownership of %q: %w", user, current, err)
		}
	}
	return nil
}

// firstExtantAncestor walks up until it finds a collection that is there.
func firstExtantAncestor(ctx context.Context, scope *rods.Scope, path string) (string, error) {
	for current := paths.Dir(path); ; current = paths.Dir(current) {
		stat, err := scope.Stat(ctx, current).Get(ctx)
		if err != nil {
			return "", err
		}
		if stat.Exists {
			return current, nil
		}
		if parent := paths.Dir(current); parent == current {
			return current, nil
		}
	}
}

// restorePlansFrom reads where each trashed path is going.
func restorePlansFrom(data map[string]any) (map[string]string, error) {
	raw, ok := data["restoration-paths"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("the task has no restoration-paths")
	}

	out := make(map[string]string, len(raw))
	for path, value := range raw {
		plan, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("the restoration plan for %q is not an object", path)
		}
		restored, ok := plan["restored-path"].(string)
		if !ok {
			return nil, fmt.Errorf("the restoration plan for %q names no restored-path", path)
		}
		out[path] = restored
	}
	return out, nil
}
