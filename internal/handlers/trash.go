package handlers

import (
	"context"
	"strings"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/clients/asynctasks"
	"github.com/cyverse-de/data-info/internal/jobs"
	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/cyverse-de/data-info/internal/service"
	"github.com/labstack/echo/v4"
)

// trashResponse is what the endpoints that trash paths return.
type trashResponse struct {
	Paths      []string          `json:"paths"`
	TrashPaths map[string]string `json:"trash-paths"`
	TaskID     string            `json:"async-task-id,omitempty"`
}

// emptyTrashResponse is what DELETE /trash returns.
type emptyTrashResponse struct {
	Trash  string   `json:"trash"`
	Paths  []string `json:"paths"`
	TaskID string   `json:"async-task-id,omitempty"`
}

// restoreResponse is what POST /restorer returns.
type restoreResponse struct {
	Restored map[string]service.RestorePlan `json:"restored"`
	TaskID   string                         `json:"async-task-id,omitempty"`
}

// Delete handles POST /deleter.
func (h *Writes) Delete(c echo.Context) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return err
	}

	var body pathsRequest
	if err := bindBody(c, &body); err != nil {
		return err
	}
	if len(body.Paths) == 0 {
		return schemaError("paths must not be empty")
	}

	scope, err := h.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	return h.deletePaths(c, ctx, scope, user, trimAll(body.Paths))
}

// DeleteByID handles DELETE /data/{data-id}.
func (h *Writes) DeleteByID(c echo.Context) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return err
	}

	scope, err := h.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	path, err := resolveID(ctx, scope, c.Param("data-id"))
	if err != nil {
		return err
	}

	return h.deletePaths(c, ctx, scope, user, []string{strings.TrimRight(path, "/")})
}

// DeleteChildrenByID handles DELETE /data/{data-id}/children: the collection stays and what
// is inside it goes.
func (h *Writes) DeleteChildrenByID(c echo.Context) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return err
	}

	scope, err := h.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	path, err := resolveID(ctx, scope, c.Param("data-id"))
	if err != nil {
		return err
	}
	path = strings.TrimRight(path, "/")

	if err := requireIsDir(ctx, scope, path); err != nil {
		return err
	}

	children, err := scope.Children(ctx, path)
	if err != nil {
		return err
	}

	return h.deletePaths(c, ctx, scope, user, children)
}

// deletePaths validates a deletion, records it, and answers with where everything is going.
func (h *Writes) deletePaths(
	c echo.Context,
	ctx context.Context,
	scope *rods.Scope,
	user string,
	requested []string,
) error {
	if err := requireKnownUser(ctx, scope, user, false); err != nil {
		return err
	}
	if err := requireAllExist(ctx, scope, requested); err != nil {
		return err
	}
	if err := requireOwnsAll(ctx, scope, user, requested); err != nil {
		return err
	}
	if err := h.requireNotHome(user, requested); err != nil {
		return err
	}

	// Anything already in the user's trash has nowhere further to go and is removed
	// outright; everything else gets a name reserved for it in the trash. Which of the two
	// happened is visible to the caller, because the response only names the ones that
	// moved.
	trashPaths := map[string]string{}
	for _, path := range requested {
		if h.deps.Layout.InTrash(path) {
			continue
		}

		trashPath, err := service.TrashPathFor(h.deps.Layout, user, path)
		if err != nil {
			return err
		}
		trashPaths[path] = trashPath
	}

	locked := append([]string{}, requested...)
	for _, trashPath := range trashPaths {
		locked = append(locked, trashPath)
	}
	if err := h.deps.RequireUnlocked(ctx, locked...); err != nil {
		return err
	}

	taskID, err := h.startTask(ctx, asynctasks.TypeDelete, user, map[string]any{
		"paths":       requested,
		"trash-paths": trashPaths,
	}, jobs.Delete{Deps: h.jobDeps()})
	if err != nil {
		return err
	}

	return writeJSONOK(c, trashResponse{Paths: requested, TrashPaths: trashPaths, TaskID: taskID})
}

// EmptyTrash handles DELETE /trash.
func (h *Writes) EmptyTrash(c echo.Context) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return err
	}

	scope, err := h.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	if err := requireKnownUser(ctx, scope, user, false); err != nil {
		return err
	}

	trash := h.deps.Layout.UserTrash(user)

	contents, err := scope.Children(ctx, trash)
	if err != nil {
		return err
	}

	if err := h.deps.RequireUnlocked(ctx, contents...); err != nil {
		return err
	}

	taskID, err := h.startTask(ctx, asynctasks.TypeDeleteTrash, user, map[string]any{
		"trash-paths": contents,
	}, jobs.EmptyTrash{Deps: h.jobDeps()})
	if err != nil {
		return err
	}

	return writeJSONOK(c, emptyTrashResponse{Trash: trash, Paths: contents, TaskID: taskID})
}

// Restore handles POST /restorer.
//
// An empty or missing list of paths means the whole trash, which is what the DE's "restore
// all" does.
func (h *Writes) Restore(c echo.Context) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return err
	}

	var body pathsRequest
	if err := bindBody(c, &body); err != nil {
		return err
	}

	scope, err := h.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	requested := trimAll(body.Paths)
	if len(requested) == 0 {
		requested, err = scope.Children(ctx, h.deps.Layout.UserTrash(user))
		if err != nil {
			return err
		}
	}

	// A trash that is already empty is not an error and needs no task.
	if len(requested) == 0 {
		return writeJSONOK(c, restoreResponse{Restored: map[string]service.RestorePlan{}})
	}

	if err := requireKnownUser(ctx, scope, user, false); err != nil {
		return err
	}
	if err := requireAllExist(ctx, scope, requested); err != nil {
		return err
	}
	if err := requireAllWriteable(ctx, scope, user, requested); err != nil {
		return err
	}

	plans := make(map[string]service.RestorePlan, len(requested))
	destinations := make([]string, 0, len(requested))
	for _, path := range requested {
		plan, err := service.PlanRestore(ctx, scope, h.deps.Layout, user, path)
		if err != nil {
			return err
		}
		plans[path] = plan
		destinations = append(destinations, plan.RestoredPath)
	}

	if err := h.deps.RequireUnlocked(ctx, append(append([]string{}, requested...), destinations...)...); err != nil {
		return err
	}

	taskID, err := h.startTask(ctx, asynctasks.TypeRestore, user, map[string]any{
		"paths":             requested,
		"restoration-paths": plans,
	}, jobs.Restore{Deps: h.jobDeps()})
	if err != nil {
		return err
	}

	return writeJSONOK(c, restoreResponse{Restored: plans, TaskID: taskID})
}

// requireNotHome refuses to delete a user's own home collection.
//
// It is the one path whose deletion cannot be undone by restoring it, because everything the
// user has lives under it -- including the trash the deletion would try to use.
func (h *Writes) requireNotHome(user string, requested []string) error {
	home := h.deps.Layout.UserHome(user)

	var refused []string
	for _, path := range requested {
		if strings.TrimRight(path, "/") == home {
			refused = append(refused, path)
		}
	}
	if len(refused) > 0 {
		return apierror.New(apierror.ErrNotAuthorized).With("paths", refused)
	}
	return nil
}
