package handlers

import (
	"context"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/cyverse-de/data-info/internal/service"
	"github.com/labstack/echo/v4"
)

// statRequest is the body both stat endpoints take. Either list may be omitted.
type statRequest struct {
	Paths []string `json:"paths"`
	IDs   []string `json:"ids"`
}

// statResponse is what both stat endpoints return: the requested paths and ids, each mapped
// to its status information.
//
// Both keys are always present, empty object included. The Clojure service answers a
// request carrying only paths with {"paths":{...},"ids":{}}, and an empty request with
// {"paths":{},"ids":{}}; omitting either would be a wire change.
type statResponse struct {
	Paths map[string]service.Stat `json:"paths"`
	IDs   map[string]service.Stat `json:"ids"`
}

// Stats serves the status endpoints.
type Stats struct {
	deps Deps
}

// NewStats builds the status handlers.
func NewStats(deps Deps) *Stats { return &Stats{deps: deps} }

// Gather handles POST /stat-gatherer and POST /path-info.
//
// They are one handler because they were one handler in the Clojure service too: the
// endpoints differ only in which query parameters their schemas accept. /path-info adds
// field filtering and the two ignore flags; /stat-gatherer takes neither.
func (h *Stats) Gather(c echo.Context) error {
	ctx := c.Request().Context()

	var body statRequest
	if err := c.Bind(&body); err != nil {
		return apierror.New(apierror.ErrInvalidJSON).With("reason", err.Error()).WithCause(err)
	}

	user, err := requireUser(c)
	if err != nil {
		return err
	}

	if err := h.deps.CheckPathCount(len(body.Paths)); err != nil {
		return err
	}
	if err := h.deps.CheckPathCount(len(body.IDs)); err != nil {
		return err
	}

	scope, err := h.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	opts := service.StatOptions{
		Fields:      service.ParseFieldSet(c.QueryParam("filter-include"), c.QueryParam("filter-exclude")),
		Layout:      h.deps.Layout,
		PermsFilter: h.deps.PermsFilter,
	}

	ignoreMissing := boolParam(c, "ignore-missing")
	ignoreInaccessible := boolParam(c, "ignore-inaccessible")
	behavior := validationBehavior(c.QueryParam("validation-behavior"))

	// Ids are resolved to paths first, so that everything after this works in one
	// vocabulary. The response still reports them under their id.
	pathsByID, err := h.resolveIDs(ctx, scope, body.IDs, ignoreMissing)
	if err != nil {
		return err
	}

	requested := make([]string, 0, len(body.Paths)+len(pathsByID))
	requested = append(requested, body.Paths...)
	for _, p := range pathsByID {
		requested = append(requested, p)
	}

	stats, err := service.StatsOf(ctx, scope, user, requested, opts)
	if err != nil {
		return err
	}

	// A path that resolved to nothing either fails the request or is dropped, depending
	// on what the caller asked for.
	visible, err := h.filterVisible(ctx, scope, requested, stats, behavior, ignoreMissing, ignoreInaccessible)
	if err != nil {
		return err
	}

	resp := statResponse{
		Paths: make(map[string]service.Stat, len(body.Paths)),
		IDs:   make(map[string]service.Stat, len(pathsByID)),
	}
	for _, p := range body.Paths {
		if stat, ok := visible[p]; ok {
			resp.Paths[p] = stat
		}
	}
	for id, p := range pathsByID {
		if stat, ok := visible[p]; ok {
			resp.IDs[id] = stat
		}
	}

	return writeJSONOK(c, resp)
}

// resolveIDs maps data ids onto paths.
func (h *Stats) resolveIDs(ctx context.Context, scope *rods.Scope, ids []string, ignoreMissing bool) (map[string]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	found, err := scope.PathsForUUIDs(ctx, ids).Get(ctx)
	if err != nil {
		return nil, err
	}

	if !ignoreMissing {
		missing := make([]string, 0)
		for _, id := range ids {
			if _, ok := found[id]; !ok {
				missing = append(missing, id)
			}
		}
		if len(missing) > 0 {
			return nil, apierror.New(apierror.ErrDoesNotExist).With("ids", missing)
		}
	}

	return found, nil
}

// filterVisible drops or rejects paths that are missing or that the user may not see at the
// requested level.
func (h *Stats) filterVisible(
	ctx context.Context,
	scope *rods.Scope,
	requested []string,
	stats map[string]service.Stat,
	behavior rods.Permission,
	ignoreMissing, ignoreInaccessible bool,
) (map[string]service.Stat, error) {
	var missing, inaccessible []string
	out := make(map[string]service.Stat, len(stats))

	for _, p := range requested {
		base, err := scope.Stat(ctx, p).Get(ctx)
		if err != nil {
			return nil, err
		}

		if !base.Exists {
			missing = append(missing, p)
			continue
		}
		if !permits(base.Permission, behavior) {
			inaccessible = append(inaccessible, p)
			continue
		}
		if stat, ok := stats[p]; ok {
			out[p] = stat
		}
	}

	if len(missing) > 0 && !ignoreMissing {
		return nil, apierror.New(apierror.ErrDoesNotExist).With("paths", missing)
	}
	if len(inaccessible) > 0 && !ignoreInaccessible {
		return nil, apierror.New(apierror.ErrNotReadable).With("paths", inaccessible)
	}

	return out, nil
}

// validationBehavior resolves the requested permission level, defaulting to read.
func validationBehavior(raw string) rods.Permission {
	switch rods.Permission(raw) {
	case rods.PermissionOwn:
		return rods.PermissionOwn
	case rods.PermissionWrite:
		return rods.PermissionWrite
	default:
		return rods.PermissionRead
	}
}

// permits reports whether a held permission satisfies a required one.
func permits(held, required rods.Permission) bool {
	rank := map[rods.Permission]int{
		rods.PermissionNone:  0,
		rods.PermissionRead:  1,
		rods.PermissionWrite: 2,
		rods.PermissionOwn:   3,
	}
	return rank[held] >= rank[required] && held != rods.PermissionNone
}

// boolParam reads a boolean query parameter, treating anything unparseable as false, which
// is what the Clojure schema coercion did for these two flags.
func boolParam(c echo.Context, name string) bool {
	switch c.QueryParam(name) {
	case "true", "TRUE", "True", "1":
		return true
	default:
		return false
	}
}
