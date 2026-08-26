package handlers

import (
	"context"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/icat"
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

// Gather handles POST /path-info, which accepts field filtering and the ignore flags.
func (h *Stats) Gather(c echo.Context) error {
	return h.gather(c, true)
}

// GatherPlain handles POST /stat-gatherer.
//
// The two endpoints share an implementation because they shared one in the Clojure service,
// but not a parameter set: /stat-gatherer's schema declares validation-behavior and nothing
// else, so filter-include, filter-exclude, ignore-missing and ignore-inaccessible are not its
// to honour. Reading them on both routes would make /stat-gatherer quietly accept a request
// the Clojure service rejects.
func (h *Stats) GatherPlain(c echo.Context) error {
	return h.gather(c, false)
}

func (h *Stats) gather(c echo.Context, filtered bool) error {
	ctx := c.Request().Context()

	var body statRequest
	if err := bindBody(c, &body); err != nil {
		return err
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

	// The plural key here, not the singular one: this endpoint validates through
	// clj-irods, whose envelope carries a list.
	if err := requireKnownUser(ctx, scope, user, true); err != nil {
		return err
	}

	opts := service.StatOptions{
		Fields:      service.ParseFieldSet("", ""),
		Layout:      h.deps.Layout,
		PermsFilter: h.deps.PermsFilter,
	}

	var ignoreMissing, ignoreInaccessible bool
	if filtered {
		opts.Fields = service.ParseFieldSet(c.QueryParam("filter-include"), c.QueryParam("filter-exclude"))

		var err error
		if ignoreMissing, err = boolParam(c, "ignore-missing"); err != nil {
			return err
		}
		if ignoreInaccessible, err = boolParam(c, "ignore-inaccessible"); err != nil {
			return err
		}
	}

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

	// Existence is asked as the service's own account, not the caller's, because those are
	// different questions. A path that exists but is not shared with the caller must be
	// omitted from the response, not reported as missing -- which is the common case for
	// a caller asking about someone else's tree.
	if !ignoreMissing && len(body.Paths) > 0 {
		if err := h.requirePathsExist(ctx, body.Paths); err != nil {
			return err
		}
	}

	stats, err := service.StatsOf(ctx, scope, user, requested, opts)
	if err != nil {
		return err
	}

	visible, err := h.filterVisible(ctx, scope, requested, stats, behavior, ignoreInaccessible)
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
//
// An id that resolves to nothing is reported by id, never by path. Resolution is not scoped
// to the caller -- it is a catalog lookup -- so an id belonging to someone else resolves
// successfully; echoing its path in an error would disclose a path the caller is not allowed
// to see. The Clojure service reports only the id for the same reason.
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

// requirePathsExist rejects paths that are not in the catalog at all, asking as the
// service's own account so that "not shared with you" is not reported as "not there".
func (h *Stats) requirePathsExist(ctx context.Context, requested []string) error {
	proxy, err := h.deps.OpenProxyScope(ctx)
	if err != nil {
		return err
	}
	defer proxy.Close()

	if _, err := proxy.Stats(ctx, requested).Get(ctx); err != nil {
		return err
	}

	var missing []string
	for _, p := range requested {
		stat, err := proxy.Stat(ctx, p).Get(ctx)
		if err != nil {
			return err
		}
		if !stat.Exists {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		return apierror.New(apierror.ErrDoesNotExist).With("paths", missing)
	}
	return nil
}

// filterVisible drops or rejects paths that are missing or that the user may not see at the
// requested level.
func (h *Stats) filterVisible(
	ctx context.Context,
	scope *rods.Scope,
	requested []string,
	stats map[string]service.Stat,
	behavior rods.Permission,
	ignoreInaccessible bool,
) (map[string]service.Stat, error) {
	var inaccessible []string
	out := make(map[string]service.Stat, len(stats))

	for _, p := range requested {
		base, err := scope.Stat(ctx, p).Get(ctx)
		if err != nil {
			return nil, err
		}

		// Invisible to this caller. Existence was already established against the
		// service's own account, so this is a permission matter and the entry is simply
		// omitted rather than reported as missing.
		if !base.Exists {
			continue
		}
		if !rods.Permits(base.Permission, behavior) {
			inaccessible = append(inaccessible, p)
			continue
		}
		if stat, ok := stats[p]; ok {
			out[p] = stat
		}
	}

	if len(inaccessible) > 0 && !ignoreInaccessible {
		return nil, insufficientPermission(behavior).
			With("paths", inaccessible).
			With("user", scope.User())
	}

	return out, nil
}

// insufficientPermission names the failure for the permission level that was required.
//
// The Clojure service picks a different validator per validation-behavior, and each throws
// its own code, so a request asking for own on a merely-readable path reports ERR_NOT_OWNER
// rather than ERR_NOT_READABLE.
func insufficientPermission(required rods.Permission) *apierror.Error {
	switch required {
	case rods.PermissionOwn:
		return apierror.New(apierror.ErrNotOwner)
	case rods.PermissionWrite:
		return apierror.New(apierror.ErrNotWriteable)
	default:
		return apierror.New(apierror.ErrNotReadable)
	}
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

// dataIDsRequest is the body /stat-lister takes. The list is required, and paths are not
// accepted: this endpoint pages a set of ids and has nowhere to put a path.
type dataIDsRequest struct {
	IDs []string `json:"ids"`
}

// dataIDListing is what /stat-lister returns. The two arrays are always present, empty
// included, and their order is the page's order within each type.
type dataIDListing struct {
	Files   []service.Stat `json:"files"`
	Folders []service.Stat `json:"folders"`
	Total   int64          `json:"total"`
}

// Listing handles POST /stat-lister.
//
// It is /stat-gatherer paged: the same entries, selected and ordered by the catalog so that
// sorting and paging apply across the whole set rather than within a response. That is why
// it goes to the catalog directly -- the query unions collections and data objects, sorts
// them together and pages the result, which GenQuery cannot express.
func (h *Stats) Listing(c echo.Context) error {
	ctx := c.Request().Context()

	var body dataIDsRequest
	if err := bindBody(c, &body); err != nil {
		return err
	}
	if body.IDs == nil {
		return schemaError("ids is required")
	}

	user, err := requireUser(c)
	if err != nil {
		return err
	}
	if err := h.deps.CheckPathCount(len(body.IDs)); err != nil {
		return err
	}

	// Both are required *by the schema* here, where the folder listing declares limit as
	// optional and checks it in the handler. The difference shows in the answer: a missing
	// limit is a schema failure and a 400 on this route, and ERR_MISSING_QUERY_PARAMETER on
	// that one -- which its own error style then answers with a 500.
	limit, err := requiredIntParam(c, "limit")
	if err != nil {
		return err
	}
	if limit <= 0 {
		return schemaError("limit must be a positive integer")
	}
	offset, err := requiredIntParam(c, "offset")
	if err != nil {
		return err
	}
	if offset < 0 {
		return schemaError("offset must be a non-negative integer")
	}

	column, err := icat.ResolveSortColumn(c.QueryParam("sort-field"))
	if err != nil {
		return schemaError(err.Error())
	}
	direction, err := icat.ResolveSortDirection(c.QueryParam("sort-dir"))
	if err != nil {
		return schemaError(err.Error())
	}

	scope, err := h.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	if err := requireKnownUser(ctx, scope, user, true); err != nil {
		return err
	}

	infoTypes, includeUnknown := infoTypeFilter(c)
	query := rods.UUIDListingQuery{
		UUIDs:                  body.IDs,
		InfoTypes:              infoTypes,
		IncludeUnknownInfoType: includeUnknown,
		SortColumn:             column,
		SortDirection:          direction,
		Limit:                  int(limit),
		Offset:                 int(offset),
	}

	// The page and the total are dispatched together and then awaited, so the two catalog
	// queries run at once rather than one after the other.
	pageValue := scope.UUIDListing(ctx, query)
	totalValue := scope.UUIDCount(ctx, query)

	page, err := pageValue.Get(ctx)
	if err != nil {
		return err
	}
	total, err := totalValue.Get(ctx)
	if err != nil {
		return err
	}

	// The rows arrived with the page, so decorating them asks the catalog only for what a
	// row does not carry: access lists for a share count, child counts for a folder.
	ordered := make([]string, 0, len(page))
	for _, row := range page {
		ordered = append(ordered, row.FullPath)
	}
	stats, err := service.StatsOfLoaded(ctx, scope, user, ordered, service.StatOptions{
		Fields:      service.ParseFieldSet(c.QueryParam("filter-include"), c.QueryParam("filter-exclude")),
		Layout:      h.deps.Layout,
		PermsFilter: h.deps.PermsFilter,
	})
	if err != nil {
		return err
	}

	out := dataIDListing{
		Files:   make([]service.Stat, 0, len(page)),
		Folders: make([]service.Stat, 0, len(page)),
		Total:   total,
	}
	for _, row := range page {
		stat, ok := stats[row.FullPath]
		if !ok {
			continue
		}
		if row.IsCollection() {
			out.Folders = append(out.Folders, stat)
		} else {
			out.Files = append(out.Files, stat)
		}
	}

	return writeJSONOK(c, out)
}
