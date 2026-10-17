// Package jobs runs work that outlives its request: moves, renames, deletions and restores.
// A job takes its inputs only from its async task, because the task is what lock checks see.
package jobs

import (
	"context"
	"fmt"

	"github.com/cyverse-de/data-info/internal/clients/asynctasks"
	"github.com/cyverse-de/data-info/internal/clients/notifications"
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/cyverse-de/data-info/internal/service"
	"github.com/cyverse-de/data-info/internal/worker"
	"github.com/sirupsen/logrus"
)

// Every job tells its user how it went, after the worker has recorded the outcome.
var (
	_ worker.Notifying = Move{}
	_ worker.Notifying = Rename{}
	_ worker.Notifying = Delete{}
	_ worker.Notifying = EmptyTrash{}
	_ worker.Notifying = Restore{}
)

// Notifier tells a user that their operation finished.
type Notifier interface {
	Send(ctx context.Context, message notifications.Message) error
}

// TaskCreator records a task, returning the id that names it.
type TaskCreator interface {
	Create(ctx context.Context, task asynctasks.Task) (string, error)
}

// Publisher emits an event for other services.
type Publisher interface {
	Publish(ctx context.Context, routingKey string, message any)
}

// Deps are what every job needs.
type Deps struct {
	// OpenScope gives a job its own data-store scope; the request's closes before the job ends.
	OpenScope func(ctx context.Context, user string) (*rods.Scope, error)

	Notifier  Notifier
	Publisher Publisher
	Log       *logrus.Entry

	Layout     paths.Layout
	AdminUsers map[string]bool
	ProxyUser  string
}

// Progress action names. People read them in a task's status when a job fails, so they say
// what happened rather than which function ran.
const (
	actionBegin = "begin"
	// actionErrorDeleting marks a path EmptyTrash could not remove; it carries on regardless.
	actionErrorDeleting = "error-deleting"
	actionValidate      = "validated-path-lengths"
	actionRenamed       = "did-rename"
	actionEnd           = "end"
)

// Pseudo-paths that stand in for a path in a multi-path job's bracketing statuses, as in
// "[instance] deleted paths: begin".
const (
	// pathsetDeleted brackets restores as well as deletes, as it did in the Clojure service.
	pathsetDeleted = "deleted paths"
	pathsetSeveral = "several paths"
	pathsetTrash   = "delete trash"
)

// notify sends a completion notification, logging rather than returning a failure: the
// operation itself has already finished.
func (d Deps) notify(ctx context.Context, message notifications.Message) {
	if d.Notifier == nil {
		return
	}
	if err := d.Notifier.Send(ctx, message); err != nil {
		d.Log.WithError(err).
			WithField("action", message.Payload.Action).
			Error("could not tell the user their operation finished; " +
				"the operation itself succeeded, so this is a missing notification rather than lost work")
	}
}

// stringsFrom reads a list of strings from task data. Unlike the lock check's reader it is
// strict: a job must never operate on a guess.
func stringsFrom(data map[string]any, key string) ([]string, error) {
	raw, ok := data[key]
	if !ok {
		return nil, fmt.Errorf("the task has no %q", key)
	}

	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("the task's %q is not a list", key)
	}

	out := make([]string, 0, len(list))
	for i, item := range list {
		value, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("entry %d of the task's %q is not a path", i, key)
		}
		out = append(out, value)
	}
	return out, nil
}

// stringFrom reads one string out of a task's data.
func stringFrom(data map[string]any, key string) (string, error) {
	value, ok := data[key].(string)
	if !ok || value == "" {
		return "", fmt.Errorf("the task has no %q", key)
	}
	return value, nil
}

// scopeFor opens a job's view of the data store as the user the task belongs to.
func (d Deps) scopeFor(ctx context.Context, task *asynctasks.Task) (*rods.Scope, error) {
	if task.Username == "" {
		return nil, fmt.Errorf("the task names no user, so there is nobody to act as")
	}
	return d.OpenScope(ctx, task.Username)
}

// moveOne moves one path and repairs its permissions, reporting each step: the task's status
// history is the only record of how far a job got.
func moveOne(
	ctx context.Context,
	scope *rods.Scope,
	deps Deps,
	user, source, destination string,
	progress worker.Progress,
) error {
	progress(source, actionBegin)

	if err := checkLength(source); err != nil {
		return err
	}
	if err := checkLength(destination); err != nil {
		return err
	}
	progress(source, actionValidate)

	if err := scope.Move(ctx, source, destination); err != nil {
		return fmt.Errorf("moving %q to %q: %w", source, destination, err)
	}
	progress(source, actionRenamed)

	err := service.RepairPermissions(ctx, scope, service.MoveContext{
		Source:      source,
		Destination: destination,
		User:        user,
		AdminUsers:  deps.AdminUsers,
		ProxyUser:   deps.ProxyUser,
		Layout:      deps.Layout,
	})
	if err != nil {
		return fmt.Errorf("repairing permissions after moving %q: %w", source, err)
	}

	progress(source, actionEnd)
	return nil
}

// checkLength rejects a path iRODS would refuse to name.
func checkLength(path string) error {
	if violation := paths.CheckLength(path); violation != paths.LengthOK {
		return fmt.Errorf("%q is too long: the %s exceeds what iRODS allows", path, violation)
	}
	return nil
}
