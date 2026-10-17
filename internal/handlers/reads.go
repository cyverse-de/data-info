package handlers

import (
	"strings"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/paths"
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

// Existence handles POST /existence-marker, reporting whether each path exists and is readable
// by the caller.
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

	if err := requireKnownUser(ctx, scope, user, false); err != nil {
		return err
	}

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
		// Access levels below read make an object visible in the catalog but unreadable.
		out[p] = stat.Exists && rods.Permits(stat.Permission, rods.PermissionRead)
	}

	return writeJSONOK(c, map[string]any{"paths": out})
}

// Creatability handles POST /creatability-marker, reporting whether each path's deepest
// existing ancestor is a folder the caller can write to. Existence is checked as the service
// account, so the walk can't step past an unshared folder and call a path under someone
// else's home creatable.
func (h *Reads) Creatability(c echo.Context) error {
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

	if err := requireKnownUser(ctx, scope, user, false); err != nil {
		return err
	}

	proxy, err := h.deps.OpenProxyScope(ctx)
	if err != nil {
		return err
	}
	defer proxy.Close()

	// Every ancestor of every requested path in one query, rather than a query per step of
	// each walk. A path twenty levels deep would otherwise cost twenty round trips.
	chains := make(map[string][]string, len(body.Paths))
	var candidates []string
	for _, p := range body.Paths {
		chain := ancestorsOf(p)
		chains[p] = chain
		candidates = append(candidates, chain...)
	}
	if _, err := proxy.Stats(ctx, candidates).Get(ctx); err != nil {
		return err
	}

	// The deepest existing ancestor of each path, then the caller's access to those in one
	// more query.
	ancestors := make(map[string]string, len(body.Paths))
	var found []string
	for _, p := range body.Paths {
		for _, candidate := range chains[p] {
			stat, err := proxy.Stat(ctx, candidate).Get(ctx)
			if err != nil {
				return err
			}
			if !stat.Exists {
				continue
			}
			// Only a folder can hold what would be created. A path whose deepest extant
			// ancestor is a file is not creatable, and the walk stops there rather than
			// continuing past it.
			if stat.Type == rods.ObjectTypeDir {
				ancestors[p] = candidate
				found = append(found, candidate)
			}
			break
		}
	}
	if _, err := scope.Stats(ctx, found).Get(ctx); err != nil {
		return err
	}

	out := make(map[string]bool, len(body.Paths))
	for _, p := range body.Paths {
		ancestor, ok := ancestors[p]
		if !ok {
			out[p] = false
			continue
		}
		stat, err := scope.Stat(ctx, ancestor).Get(ctx)
		if err != nil {
			return err
		}
		out[p] = rods.Permits(stat.Permission, rods.PermissionWrite)
	}

	return writeJSONOK(c, map[string]any{"paths": out})
}

// ancestorsOf is a path followed by each of its ancestors, stopping at the zone root rather
// than "/", which the catalog refuses; such paths are uncreatable either way.
func ancestorsOf(p string) []string {
	current := strings.TrimRight(p, "/")
	if current == "" {
		return nil
	}

	var out []string
	for {
		out = append(out, current)
		parent := paths.Dir(current)
		if parent == current || parent == "/" {
			return out
		}
		current = parent
	}
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

	if err := requireKnownUser(ctx, scope, user, false); err != nil {
		return err
	}

	admin, err := h.deps.OpenProxyScope(ctx)
	if err != nil {
		return err
	}
	defer admin.Close()

	if _, err := admin.Stats(ctx, body.Paths).Get(ctx); err != nil {
		return err
	}
	if _, err := scope.Stats(ctx, body.Paths).Get(ctx); err != nil {
		return err
	}

	// Existence (as the service account) is checked for every path before ownership, and each
	// check reports all failures, so a missing path wins over an unowned one and a path the
	// caller can't see is reported as not owned.
	var missing, notOwned []string
	for _, p := range body.Paths {
		exists, err := admin.Stat(ctx, p).Get(ctx)
		if err != nil {
			return err
		}
		if !exists.Exists {
			missing = append(missing, p)
			continue
		}

		stat, err := scope.Stat(ctx, p).Get(ctx)
		if err != nil {
			return err
		}
		if stat.Permission != rods.PermissionOwn {
			notOwned = append(notOwned, p)
		}
	}
	if len(missing) > 0 {
		return apierror.New(apierror.ErrDoesNotExist).With("paths", missing)
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
		// acls is keyed by canonical path, so resolve a caller's trailing slash via the stat.
		stat, err := scope.Stat(ctx, p).Get(ctx)
		if err != nil {
			return err
		}
		out = append(out, pathPermissions{
			Path:            p,
			UserPermissions: h.deps.visiblePermissions(acls[stat.Path], user),
		})
	}

	return writeJSONOK(c, map[string]any{"paths": out})
}

// PermissionsByID handles GET /data/{data-id}/permissions. Read access suffices, unlike the
// bulk endpoint's ownership check, because the DE shows an item's sharing panel to anyone who
// can open it.
func (h *Reads) PermissionsByID(c echo.Context) error {
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

	// The id is resolved before the user is checked, so an unknown id is reported even when
	// the user is also unknown.
	path, err := resolveID(ctx, scope, c.Param("data-id"))
	if err != nil {
		return err
	}
	path = strings.TrimRight(path, "/")

	if err := requireKnownUser(ctx, scope, user, false); err != nil {
		return err
	}
	if err := requirePathReadable(ctx, scope, user, path); err != nil {
		return err
	}

	acl, err := scope.ACL(ctx, path).Get(ctx)
	if err != nil {
		return err
	}

	return writeJSONOK(c, map[string]any{"user-permissions": h.deps.visiblePermissions(acl, user)})
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

	scope, err := h.deps.OpenScope(c.Request().Context(), user)
	if err != nil {
		return err
	}
	defer scope.Close()

	// Reports an unknown user under the plural "users" key.
	if err := requireKnownUser(c.Request().Context(), scope, user, true); err != nil {
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
	if err := bindBody(c, &body); err != nil {
		return "", body, err
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
