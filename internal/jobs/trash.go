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

// usageRoutingKeyPrefix is where a request to recount a user's storage is published. The
// data-usage service consumes it; nothing else does.
const usageRoutingKeyPrefix = "index.usage.data.user."

// Progress action names for emptying the trash and restoring.
const (
	actionBeginRestore = "begin-restore"
	actionEndRestore   = "end-restore"
)

// EmptyTrash removes everything in a user's trash for good.
type EmptyTrash struct{ Deps Deps }

// Run implements worker.Job.
//
// Like Delete this runs as the service's own account, so that objects the user can no longer
// act on -- ones that arrived in their trash through a shared collection -- still go.
//
// A failure on one path does not stop the others. Emptying a trash is all-or-nothing to the
// user, and stopping at the first problem would leave them with a trash that is neither empty
// nor what it was, and no way to tell which.
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

		// Reported whether or not the delete worked. The reference puts this outside the
		// try that catches the failure, so a path that could not be removed still gets an
		// end-delete after its error-deleting.
		progress(path, actionEndDelete)
	}

	progress(pathsetTrash, actionEnd)

	var runErr error
	if failures > 0 {
		runErr = fmt.Errorf("%d of %d paths could not be removed from the trash", failures, len(trashPaths))
	}

	// Published either way. The user's usage has changed whether or not every path went,
	// and the service that tracks it has no other way to find out.
	if e.Deps.Publisher != nil {
		e.Deps.Publisher.Publish(ctx, usageRoutingKeyPrefix+task.Username, "Update requested by data-info")
	}

	e.Deps.notify(ctx, notifications.EmptyTrash(task.Username, e.Deps.Layout, runErr != nil))
	return runErr
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

	destinations := make([]string, 0, len(trashPaths))
	for _, path := range trashPaths {
		destinations = append(destinations, plans[path])
	}

	runErr := r.restore(ctx, scope, task.Username, trashPaths, plans, progress)

	r.Deps.notify(ctx, notifications.Restore(task.Username, trashPaths, destinations, runErr != nil))
	return runErr
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

		// Re-checked here rather than trusted from the plan. The plan was made when the
		// request arrived and the job may run much later, by which time something else may
		// be sitting where this is going.
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

// restoreParents recreates the collections something was deleted out of.
//
// The user is made the owner of each one that had to be created, stopping at the first that
// already existed and at their own home. Without that they would own the restored item inside
// collections they cannot write to, which is the same as not having it back.
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
