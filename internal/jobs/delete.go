package jobs

import (
	"context"
	"fmt"

	"github.com/cyverse-de/data-info/internal/clients/asynctasks"
	"github.com/cyverse-de/data-info/internal/clients/notifications"
	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/cyverse-de/data-info/internal/service"
	"github.com/cyverse-de/data-info/internal/worker"
)

// Progress action names for deleting.
const (
	actionBeginDelete    = "begin-delete"
	actionDeletedTickets = "deleted-tickets"
	actionEndDelete      = "end-delete"
	actionSetTrashOrigin = "set-trash-path-metadata"
)

// Delete moves paths to the trash, or removes them outright when they are already there.
type Delete struct{ Deps Deps }

// Run implements worker.Job.
//
// This runs as the service's own account rather than as the user. Tickets belong to whoever
// issued them and a user cannot delete another's, but a path being deleted must not leave
// tickets behind that still point at it -- so the deletion is done with the account that can
// see them all. The reference does the same, and for the same reason.
func (d Delete) Run(ctx context.Context, task *asynctasks.Task, progress worker.Progress) error {
	requested, err := stringsFrom(task.Data, "paths")
	if err != nil {
		return err
	}

	trashPaths, err := mapFrom(task.Data, "trash-paths")
	if err != nil {
		return err
	}

	scope, err := d.Deps.OpenScope(ctx, d.Deps.ProxyUser)
	if err != nil {
		return err
	}
	defer scope.Close()

	runErr := d.delete(ctx, scope, task.Username, requested, trashPaths, progress)

	// Two notifications, because the two outcomes read differently to a user: something
	// moved to the trash can be got back, and something deleted outright cannot.
	trashed, deleted := partitionByTrash(requested, trashPaths)

	if len(deleted) > 0 {
		d.Deps.notify(ctx, notifications.Delete(task.Username, deleted, d.Deps.Layout, runErr != nil))
	}
	if len(trashed) > 0 {
		destinations := make([]string, 0, len(trashed))
		for _, path := range trashed {
			destinations = append(destinations, trashPaths[path])
		}
		d.Deps.notify(ctx, notifications.Trash(task.Username, trashed, destinations, runErr != nil))
	}

	return runErr
}

func (d Delete) delete(
	ctx context.Context,
	scope *rods.Scope,
	user string,
	requested []string,
	trashPaths map[string]string,
	progress worker.Progress,
) error {
	for i, path := range requested {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("the delete was stopped after %d of %d paths: %w", i, len(requested), err)
		}

		progress(path, actionBeginDelete)

		if err := d.deleteTickets(ctx, scope, path); err != nil {
			return err
		}
		progress(path, actionDeletedTickets)

		trashPath, toTrash := trashPaths[path]
		if !toTrash {
			// Already in the trash, so there is nowhere further to move it. Forced,
			// because an unforced delete would put it in the proxy account's trash rather
			// than removing it.
			if err := scope.Delete(ctx, path, true); err != nil {
				return fmt.Errorf("deleting %q: %w", path, err)
			}
			progress(path, actionEndDelete)
			continue
		}

		if err := scope.Move(ctx, path, trashPath); err != nil {
			return fmt.Errorf("moving %q to the trash: %w", path, err)
		}

		// Recorded after the move, on the trashed object, so that restoring it can put it
		// back where it came from. Without this a restore has nowhere to aim but the
		// user's home.
		err := scope.SetAVU(ctx, trashPath, rods.AVU{
			Attribute: service.TrashOriginAttribute,
			Value:     path,
			Unit:      service.SystemAVUUnit,
		})
		if err != nil {
			return fmt.Errorf("recording where %q came from: %w", trashPath, err)
		}
		progress(path, actionSetTrashOrigin)

		err = service.RepairPermissions(ctx, scope, service.MoveContext{
			Source:      path,
			Destination: trashPath,
			User:        user,
			AdminUsers:  d.Deps.AdminUsers,
			ProxyUser:   d.Deps.ProxyUser,
			Layout:      d.Deps.Layout,
		})
		if err != nil {
			return fmt.Errorf("repairing permissions after trashing %q: %w", path, err)
		}

		progress(path, actionEndDelete)
	}
	return nil
}

// deleteTickets removes every ticket pointing at a path.
//
// A ticket outliving the thing it grants access to is a dangling grant, and iRODS does not
// clean them up on delete. There is no way to ask for the tickets on one path, so this lists
// them and filters -- which is what the reference's query amounted to as well.
func (d Delete) deleteTickets(ctx context.Context, scope *rods.Scope, path string) error {
	tickets, err := scope.TicketsUnderPath(ctx, path)
	if err != nil {
		return fmt.Errorf("listing the tickets on %q: %w", path, err)
	}

	for _, ticket := range tickets {
		if err := scope.DeleteTicket(ctx, ticket.Name); err != nil {
			return fmt.Errorf("deleting ticket %q on %q: %w", ticket.Name, path, err)
		}
	}
	return nil
}

// partitionByTrash splits requested paths into the ones going to the trash and the ones being
// removed outright.
func partitionByTrash(requested []string, trashPaths map[string]string) (trashed, deleted []string) {
	for _, path := range requested {
		if _, ok := trashPaths[path]; ok {
			trashed = append(trashed, path)
			continue
		}
		deleted = append(deleted, path)
	}
	return trashed, deleted
}

// mapFrom reads a map of strings out of a task's data.
//
// Strict, and deliberately so. Absence from this map is the signal that a path is already in
// the trash and should be removed outright, so data of the wrong shape read as an empty map
// would force-delete everything the task named -- unrecoverably, and reporting success.
func mapFrom(data map[string]any, key string) (map[string]string, error) {
	raw, ok := data[key]
	if !ok {
		return nil, fmt.Errorf("the task has no %q", key)
	}

	object, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("the task's %q is not an object", key)
	}

	out := make(map[string]string, len(object))
	for name, value := range object {
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("the task's %q names something other than a path for %q", key, name)
		}
		out[name] = text
	}
	return out, nil
}
