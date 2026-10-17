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

// Run implements worker.Job. It runs as the proxy account because a user cannot delete
// tickets someone else issued, and a deleted path must not leave tickets behind.
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

	return d.delete(ctx, scope, task.Username, requested, trashPaths, progress)
}

// Notify implements worker.Notifying. Trashed and deleted paths get separate notifications
// because only the trashed ones can be got back.
func (d Delete) Notify(ctx context.Context, task *asynctasks.Task, failed bool) {
	requested, err := stringsFrom(task.Data, "paths")
	if err != nil {
		return
	}
	trashPaths, err := mapFrom(task.Data, "trash-paths")
	if err != nil {
		return
	}

	trashed, deleted := partitionByTrash(requested, trashPaths)

	if len(deleted) > 0 {
		d.Deps.notify(ctx, notifications.Delete(task.Username, deleted, d.Deps.Layout, failed))
	}
	if len(trashed) > 0 {
		destinations := make([]string, 0, len(trashed))
		for _, path := range trashed {
			destinations = append(destinations, trashPaths[path])
		}
		d.Deps.notify(ctx, notifications.Trash(task.Username, trashed, destinations, failed))
	}
}

func (d Delete) delete(
	ctx context.Context,
	scope *rods.Scope,
	user string,
	requested []string,
	trashPaths map[string]string,
	progress worker.Progress,
) error {
	progress(pathsetDeleted, actionBegin)

	// Listed once rather than per path: iRODS cannot list one path's tickets, so every listing
	// returns every ticket the account can see.
	var tickets []rods.Ticket
	ticketsDeleted := make(map[string]bool)

	for i, path := range requested {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("the delete was stopped after %d of %d paths: %w", i, len(requested), err)
		}

		progress(path, actionBeginDelete)

		if tickets == nil {
			listed, err := scope.Tickets(ctx)
			if err != nil {
				return fmt.Errorf("listing the tickets on %q: %w", path, err)
			}
			tickets = append(make([]rods.Ticket, 0, len(listed)), listed...)
		}
		if err := deleteTickets(ctx, scope, rods.TicketsUnder(tickets, path), ticketsDeleted, path); err != nil {
			return err
		}
		progress(path, actionDeletedTickets)

		trashPath, toTrash := trashPaths[path]
		if !toTrash {
			// Already in the trash. Forced, since an unforced delete would move it to the
			// proxy account's trash.
			if err := scope.Delete(ctx, path, true); err != nil {
				return fmt.Errorf("deleting %q: %w", path, err)
			}
			progress(path, actionEndDelete)
			continue
		}

		// Trashing is a move, so moveOne's statuses precede the trash-specific ones.
		if err := moveOne(ctx, scope, d.Deps, user, path, trashPath, progress); err != nil {
			return err
		}

		// Lets a restore put the item back where it came from rather than in the user's home.
		err := scope.SetAVU(ctx, trashPath, rods.AVU{
			Attribute: service.TrashOriginAttribute,
			Value:     path,
			Unit:      service.SystemUnit,
		})
		if err != nil {
			return fmt.Errorf("recording where %q came from: %w", trashPath, err)
		}
		progress(path, actionSetTrashOrigin)

		progress(path, actionEndDelete)
	}

	progress(pathsetDeleted, actionEnd)
	return nil
}

// deleteTickets removes the tickets on a path, since iRODS leaves them dangling. deleted skips
// tickets an earlier path in the same request already removed.
func deleteTickets(ctx context.Context, scope *rods.Scope, tickets []rods.Ticket, deleted map[string]bool, path string) error {
	for _, ticket := range tickets {
		if deleted[ticket.Name] {
			continue
		}
		if err := scope.DeleteTicket(ctx, ticket.Name); err != nil {
			return fmt.Errorf("deleting ticket %q on %q: %w", ticket.Name, path, err)
		}
		deleted[ticket.Name] = true
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

// mapFrom reads a map of strings from task data. It is strict because absence from
// trash-paths means "delete outright": misreading bad data as an empty map would irrecoverably
// delete everything the task named.
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
