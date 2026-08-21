package handlers

import (
	"context"
	"strings"

	"github.com/cyverse-de/data-info/internal/clients/asynctasks"
	"github.com/cyverse-de/data-info/internal/jobs"
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/labstack/echo/v4"
)

// moveRequest is the body of POST /mover.
type moveRequest struct {
	Sources []string `json:"sources"`
	Dest    string   `json:"dest"`
}

// nameRequest is the body of PUT /data/{data-id}/name.
type nameRequest struct {
	Filename string `json:"filename"`
}

// dirnameRequest is the body of the two routes that move a data item by id.
type dirnameRequest struct {
	Dirname string `json:"dirname"`
}

// multiMoveResponse is what the endpoints moving several paths return.
type multiMoveResponse struct {
	User    string   `json:"user"`
	Sources []string `json:"sources"`
	Dest    string   `json:"dest"`

	// TaskID is absent when nothing had to be done, which is how a caller tells a move that
	// was started from one that was a no-op.
	TaskID string `json:"async-task-id,omitempty"`
}

// moveResponse is what the endpoints moving one path return.
type moveResponse struct {
	User   string `json:"user"`
	Source string `json:"source"`
	Dest   string `json:"dest"`
	TaskID string `json:"async-task-id,omitempty"`
}

// Move handles POST /mover.
//
// Everything is validated before the task is created, so a request that cannot succeed is
// refused rather than started: once a task exists its paths are locked, and a task created
// for work that was never going to happen locks them for nothing.
func (h *Writes) Move(c echo.Context) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return err
	}

	var body moveRequest
	if err := bindBody(c, &body); err != nil {
		return err
	}
	// An empty list is allowed, and produces a task that moves nothing. The reference's
	// schema accepts it, and refusing here would turn a harmless no-op into an error for a
	// caller that built its list by filtering.
	if strings.TrimSpace(body.Dest) == "" {
		return schemaError("dest must be a non-blank string")
	}

	sources := trimAll(body.Sources)
	dest := strings.TrimRight(body.Dest, "/")

	scope, err := h.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	taskID, err := h.startMove(ctx, scope, user, sources, dest)
	if err != nil {
		return err
	}

	return writeJSONOK(c, multiMoveResponse{User: user, Sources: sources, Dest: dest, TaskID: taskID})
}

// RenameByID handles PUT /data/{data-id}/name: a new name in the same collection.
func (h *Writes) RenameByID(c echo.Context) error {
	var body nameRequest
	return h.moveOneByID(c, &body, func() (string, error) {
		if strings.TrimSpace(body.Filename) == "" {
			return "", schemaError("filename must be a non-blank string")
		}
		return body.Filename, nil
	}, func(source, name string) string {
		return paths.Join(paths.Dir(source), name)
	})
}

// MoveByID handles PUT /data/{data-id}/dir: the same name in a new collection.
func (h *Writes) MoveByID(c echo.Context) error {
	var body dirnameRequest
	return h.moveOneByID(c, &body, func() (string, error) {
		if strings.TrimSpace(body.Dirname) == "" {
			return "", schemaError("dirname must be a non-blank string")
		}
		return body.Dirname, nil
	}, func(source, dirname string) string {
		return paths.Join(dirname, paths.Base(source))
	})
}

// moveOneByID serves the two routes that move a single data item named by id.
//
// They differ only in what the body carries and how the destination is built from it, so the
// validation, the task and the response shape are shared -- which is also what keeps the two
// from drifting apart.
func (h *Writes) moveOneByID(
	c echo.Context,
	body any,
	readTarget func() (string, error),
	destinationOf func(source, target string) string,
) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return err
	}
	if err := bindBody(c, body); err != nil {
		return err
	}

	target, err := readTarget()
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

	source, err := resolveID(ctx, scope, c.Param("data-id"))
	if err != nil {
		return err
	}
	source = strings.TrimRight(source, "/")
	destination := strings.TrimRight(destinationOf(source, target), "/")

	taskID, err := h.startRename(ctx, scope, user, source, destination)
	if err != nil {
		return err
	}

	return writeJSONOK(c, moveResponse{User: user, Source: source, Dest: destination, TaskID: taskID})
}

// MoveChildrenByID handles PUT /data/{data-id}/children/dir: everything inside a collection
// moves, and the collection itself stays where it is.
func (h *Writes) MoveChildrenByID(c echo.Context) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return err
	}

	var body dirnameRequest
	if err := bindBody(c, &body); err != nil {
		return err
	}
	if strings.TrimSpace(body.Dirname) == "" {
		return schemaError("dirname must be a non-blank string")
	}
	dest := strings.TrimRight(body.Dirname, "/")

	scope, err := h.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	source, err := resolveID(ctx, scope, c.Param("data-id"))
	if err != nil {
		return err
	}
	source = strings.TrimRight(source, "/")

	if err := requireIsDir(ctx, scope, source); err != nil {
		return err
	}

	sources, err := scope.Children(ctx, source)
	if err != nil {
		return err
	}

	taskID, err := h.startMove(ctx, scope, user, sources, dest)
	if err != nil {
		return err
	}

	return writeJSONOK(c, multiMoveResponse{User: user, Sources: sources, Dest: dest, TaskID: taskID})
}

// startMove validates a multi-path move and dispatches it.
func (h *Writes) startMove(
	ctx context.Context,
	scope *rods.Scope,
	user string,
	sources []string,
	dest string,
) (string, error) {
	destinations := jobs.DestinationsUnder(dest, sources)

	// Before anything else. Everything below reads the data store, and there is no point
	// asking whether a move is possible if something else is already moving the same tree.
	if err := h.deps.RequireUnlocked(ctx, append(append([]string{}, sources...), destinations...)...); err != nil {
		return "", err
	}

	// This route validates through the jargon family, so a missing user is reported under
	// the singular key.
	if err := requireKnownUser(ctx, scope, user, false); err != nil {
		return "", err
	}
	if err := requireAllExist(ctx, scope, sources); err != nil {
		return "", err
	}
	if err := requireAllExist(ctx, scope, []string{dest}); err != nil {
		return "", err
	}
	if err := requireIsDir(ctx, scope, dest); err != nil {
		return "", err
	}
	if err := requireOwnsAll(ctx, scope, user, sources); err != nil {
		return "", err
	}
	if err := requireWriteable(ctx, scope, dest); err != nil {
		return "", err
	}
	if err := requireNoneExist(ctx, scope, destinations); err != nil {
		return "", err
	}

	return h.startTask(ctx, asynctasks.TypeMove, user, map[string]any{
		"sources":     sources,
		"destination": dest,
	}, jobs.Move{Deps: h.jobDeps()})
}

// startRename validates a single-path move and dispatches it.
func (h *Writes) startRename(
	ctx context.Context,
	scope *rods.Scope,
	user, source, destination string,
) (string, error) {
	// Renaming something to what it is already called is not an error and does not need a
	// task. The response says so by carrying no task id.
	if source == destination {
		return "", nil
	}

	if err := h.deps.RequireUnlocked(ctx, source, destination); err != nil {
		return "", err
	}

	destinationParent := paths.Dir(destination)

	if err := requireKnownUser(ctx, scope, user, false); err != nil {
		return "", err
	}
	if err := requireAllExist(ctx, scope, []string{source, destinationParent}); err != nil {
		return "", err
	}
	if err := requireIsDir(ctx, scope, destinationParent); err != nil {
		return "", err
	}
	if err := requireOwns(ctx, scope, user, source); err != nil {
		return "", err
	}
	// Only when the collection changes. Moving something within a collection the caller
	// owns but cannot write to is still allowed, which owning the source already covers.
	if paths.Dir(source) != destinationParent {
		if err := requireWriteable(ctx, scope, destinationParent); err != nil {
			return "", err
		}
	}
	if err := requireDoesNotExist(ctx, scope, destination); err != nil {
		return "", err
	}

	return h.startTask(ctx, asynctasks.TypeRename, user, map[string]any{
		"source":      source,
		"destination": destination,
	}, jobs.Rename{Deps: h.jobDeps()})
}

// trimAll removes trailing slashes, which iRODS does not treat as part of a name.
func trimAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, value := range in {
		out = append(out, strings.TrimRight(value, "/"))
	}
	return out
}
