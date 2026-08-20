package handlers

import (
	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/labstack/echo/v4"
)

// pathsRequest is the body the bulk path endpoints take.
type pathsRequest struct {
	Paths []string `json:"paths"`
}

// Reads serves the read-only endpoints that are not stat.
type Reads struct {
	deps Deps
}

// NewReads builds the read-only handlers.
func NewReads(deps Deps) *Reads { return &Reads{deps: deps} }

// Existence handles POST /existence-marker.
//
// It reports, for each path, whether it exists *and* the user can read it. The two are one
// answer here because the catalog cannot distinguish them: a user with no access rows for
// an object gets no rows, exactly as if it were not there.
func (h *Reads) Existence(c echo.Context) error {
	ctx := c.Request().Context()

	user, body, err := h.bindPaths(c)
	if err != nil {
		return err
	}

	scope, err := h.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	// One query for all of them, then read the results back per path.
	if _, err := scope.Stats(ctx, body.Paths).Get(ctx); err != nil {
		return err
	}

	out := make(map[string]bool, len(body.Paths))
	for _, p := range body.Paths {
		stat, err := scope.Stat(ctx, p).Get(ctx)
		if err != nil {
			return err
		}
		out[p] = stat.Exists
	}

	return writeJSONOK(c, map[string]any{"paths": out})
}

// userPermission is one user's access to a path, as the permission endpoints report it.
type userPermission struct {
	User       string `json:"user"`
	Permission string `json:"permission"`
}

// pathPermissions is one path's access list.
type pathPermissions struct {
	Path            string           `json:"path"`
	UserPermissions []userPermission `json:"user-permissions"`
}

// Permissions handles POST /permissions-gatherer.
//
// The caller must own every path. That check is the authorisation for the whole response:
// the catalog query behind it returns a path's full access list to whoever asks, so without
// it this endpoint would disclose who any readable path is shared with.
func (h *Reads) Permissions(c echo.Context) error {
	ctx := c.Request().Context()

	user, body, err := h.bindPaths(c)
	if err != nil {
		return err
	}

	scope, err := h.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	if _, err := scope.Stats(ctx, body.Paths).Get(ctx); err != nil {
		return err
	}

	var notOwned []string
	for _, p := range body.Paths {
		stat, err := scope.Stat(ctx, p).Get(ctx)
		if err != nil {
			return err
		}
		if !stat.Exists {
			// A list, not a single path: the Clojure validator reports every missing
			// path it was given, and callers read the plural key.
			return apierror.New(apierror.ErrDoesNotExist).With("paths", []string{p})
		}
		if stat.Permission != rods.PermissionOwn {
			notOwned = append(notOwned, p)
		}
	}
	if len(notOwned) > 0 {
		return apierror.New(apierror.ErrNotOwner).With("user", user).With("paths", notOwned)
	}

	acls, err := scope.ACLs(ctx, body.Paths).Get(ctx)
	if err != nil {
		return err
	}

	out := make([]pathPermissions, 0, len(body.Paths))
	for _, p := range body.Paths {
		out = append(out, pathPermissions{
			Path:            p,
			UserPermissions: h.visiblePermissions(acls[p], user),
		})
	}

	return writeJSONOK(c, map[string]any{"paths": out})
}

// visiblePermissions drops the entries the service does not report: the requesting user's
// own, and the service and administrative accounts a deployment filters out. Reporting
// those would expose the proxy account's access on every path.
func (h *Reads) visiblePermissions(acl []rods.ACLEntry, user string) []userPermission {
	out := make([]userPermission, 0, len(acl))
	for _, entry := range acl {
		if entry.User == user || h.deps.PermsFilter[entry.User] {
			continue
		}
		if entry.Permission == rods.PermissionNone {
			continue
		}
		out = append(out, userPermission{User: entry.User, Permission: string(entry.Permission)})
	}
	return out
}

// UserGroups handles GET /users/{username}/groups.
//
// The names are zone-qualified here, as name#zone. The client returns bare names, and the
// wire format is qualified.
func (h *Reads) UserGroups(c echo.Context) error {
	ctx := c.Request().Context()

	username := c.Param("username")
	if username == "" {
		return apierror.New(apierror.ErrBadOrMissingField).With("field", "username")
	}

	user, err := requireUser(c)
	if err != nil {
		return err
	}

	scope, err := h.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	// Both lookups are dispatched before either is awaited, so they resolve concurrently
	// rather than one after the other.
	existsValue := scope.UserExists(ctx, username)
	groupsValue := scope.UserGroups(ctx, username)

	present, err := existsValue.Get(ctx)
	if err != nil {
		return err
	}
	if !present {
		return apierror.New(apierror.ErrNotAUser).With("user", username)
	}

	names, err := groupsValue.Get(ctx)
	if err != nil {
		return err
	}
	qualified := make([]string, 0, len(names))
	for _, name := range names {
		qualified = append(qualified, name+"#"+h.deps.Layout.Zone)
	}

	return writeJSONOK(c, map[string]any{
		"user":   username + "#" + h.deps.Layout.Zone,
		"groups": qualified,
	})
}

// BasePaths handles GET /navigation/base-paths.
//
// It is pure path arithmetic: none of these collections is looked up, because the answer is
// the same whether or not they exist yet.
func (h *Reads) BasePaths(c echo.Context) error {
	user, err := requireUser(c)
	if err != nil {
		return err
	}

	return writeJSONOK(c, map[string]string{
		"user_home_path":  h.deps.Layout.UserHome(user),
		"user_trash_path": h.deps.Layout.UserTrash(user),
		"base_trash_path": h.deps.Layout.TrashBase(),
	})
}

// bindPaths reads the user parameter and a paths body, applying the bulk request limit.
func (h *Reads) bindPaths(c echo.Context) (string, pathsRequest, error) {
	var body pathsRequest
	if err := c.Bind(&body); err != nil {
		return "", body, apierror.New(apierror.ErrInvalidJSON).With("reason", err.Error()).WithCause(err)
	}

	user, err := requireUser(c)
	if err != nil {
		return "", body, err
	}
	if len(body.Paths) == 0 {
		return "", body, schemaError("paths must not be empty")
	}
	if err := h.deps.CheckPathCount(len(body.Paths)); err != nil {
		return "", body, err
	}

	return user, body, nil
}
