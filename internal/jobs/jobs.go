// Package jobs holds the work that outlives the request which asked for it: moves, renames,
// deletions and restores.
//
// Each job is handed a task rather than arguments. The task is the record the whole DE reads
// to decide whether a path is busy, so what a job operates on has to come from there and
// nowhere else -- a job working on something the task does not name is work nothing else can
// see.
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
	// OpenScope gives a job its own view of the data store. A job cannot reuse the
	// request's: that scope is closed when the request ends, which is long before the job
	// does.
	OpenScope func(ctx context.Context, user string) (*rods.Scope, error)

	Notifier  Notifier
	Publisher Publisher
	Log       *logrus.Entry

	Layout     paths.Layout
	AdminUsers map[string]bool
	ProxyUser  string
}

// Progress action names. They appear in a task's status detail and someone reads them when a
// job goes wrong, so they say what happened rather than which function ran.
const (
	actionBegin = "begin"
	// actionErrorDeleting marks a path that could not be removed. The job carries on: the
	// reference collects the failures and reports them at the end rather than stopping.
	actionErrorDeleting = "error-deleting"
	actionValidate      = "validated-path-lengths"
	actionRenamed       = "did-rename"
	actionEnd           = "end"
)

// The pseudo-paths a multi-path job brackets its trail with. They sit in the path position
// of a status detail, where a real path goes, because the reference puts them there: a
// status reading "[instance] deleted paths: begin" is what a client sees before the first
// path is touched. They are literal strings and not derived from anything, so they are
// spelled here once.
const (
	// pathsetDeleted brackets both a delete and a restore. The same string for both is the
	// reference's, not a copy-paste here: restore-paths-thread opens with "deleted paths"
	// as well, and a client reading the trail sees it on either operation.
	pathsetDeleted = "deleted paths"
	pathsetSeveral = "several paths"
	pathsetTrash   = "delete trash"
)

// notify sends a completion notification, swallowing a failure.
//
// The operation is already done by the time this runs. Failing the job because the user could
// not be told would report a failure that did not happen, and would leave a task recorded as
// failed with its work complete.
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

// stringsFrom reads a list of strings out of a task's data.
//
// Task data is decoded JSON, so a list is []any. A job whose data is not the shape it expects
// cannot do anything useful, so this is strict where the lock's reader is forgiving: the lock
// must never fail over one odd task, and a job must never operate on a guess.
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

// moveOne moves a single path and repairs the permissions afterwards, reporting each step.
//
// The steps are the reference's, and they are reported rather than merely performed because
// a task's status history is the only record of how far a job got before it stopped.
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
