package handlers

import (
	"context"
	"net/http"
	"strings"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/lazy"
	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/cyverse-de/data-info/internal/service"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// UUIDForPath handles GET /data/uuid.
func (h *Listings) UUIDForPath(c echo.Context) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return err
	}

	path := c.QueryParam("path")
	if strings.TrimSpace(path) == "" {
		return schemaError("path must be a non-blank string")
	}

	scope, err := h.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	// This route validates through clj-irods, whose envelopes are plural throughout.
	if err := requireKnownUser(ctx, scope, user, true); err != nil {
		return err
	}

	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}
	if !stat.Exists {
		return apierror.New(apierror.ErrDoesNotExist).With("paths", []string{path})
	}
	if !rods.Permits(stat.Permission, rods.PermissionRead) {
		return apierror.New(apierror.ErrNotReadable).With("paths", []string{path}).With("user", user)
	}

	return writeJSONOK(c, map[string]string{"id": stat.UUID})
}

// Head handles HEAD /data/{data-id}.
//
// Its contract is the status alone: 200 readable, 403 not, 404 for an unknown id, 422 for an
// id that is not a UUID or a user that does not exist. There is no body, so nothing else can
// carry the answer.
func (h *Listings) Head(c echo.Context) error {
	ctx := c.Request().Context()

	id := c.Param("data-id")
	if _, err := uuid.Parse(id); err != nil {
		// A 400, not the 422 this endpoint uses elsewhere: the path parameter is typed as
		// a UUID, so an unparseable one fails schema coercion before the handler is
		// reached, and that is reported the same way on every route.
		return schemaError("data-id must be a UUID")
	}

	// A missing user is a schema failure, like everywhere else: the route declares it as
	// a required non-blank parameter. The 422 below is for a user that parses but does
	// not exist, which is a different answer.
	user, err := requireUser(c)
	if err != nil {
		return err
	}

	scope, err := h.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	known, err := scope.UserExists(ctx, user).Get(ctx)
	if err != nil {
		return err
	}
	if !known {
		return apierror.New(apierror.ErrNotAUser).
			WithStatus(http.StatusUnprocessableEntity).With("user", user)
	}

	paths, err := scope.PathsForUUIDs(ctx, []string{id}).Get(ctx)
	if err != nil {
		return err
	}
	path, ok := paths[id]
	if !ok {
		return apierror.New(apierror.ErrDoesNotExist).WithStatus(http.StatusNotFound).With("id", id)
	}

	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}
	if !stat.Exists || !rods.Permits(stat.Permission, rods.PermissionRead) {
		return apierror.New(apierror.ErrNotReadable).WithStatus(http.StatusForbidden).With("id", id)
	}

	return c.NoContent(http.StatusOK)
}

// findReadme looks for a README directly under a folder, returning nil when there is none.
//
// The names are tried in a fixed order and the first that exists wins, matching the Clojure
// service. A folder that has one reports the whole entry rather than a flag, because the UI
// renders it.
//
// The lookups are dispatched together and then awaited, so six probes cost one round of
// concurrent work rather than six sequential ones.
func (h *Listings) findReadme(ctx context.Context, scope *rods.Scope, folder string, rule service.BadNameRule) (any, error) {
	stats := make([]*lazy.Value[rods.Stat], 0, len(service.ReadmeNames))
	for _, name := range service.ReadmeNames {
		stats = append(stats, scope.Stat(ctx, folder+"/"+name))
	}

	for i, value := range stats {
		stat, err := value.Get(ctx)
		if err != nil {
			return nil, err
		}
		if !stat.Exists || stat.Type != rods.ObjectTypeFile {
			continue
		}

		return service.ListingEntry{
			ID:           stat.UUID,
			DateCreated:  stat.CreatedMS,
			DateModified: stat.ModifiedMS,
			BadName:      rule.Matches(stat.Path, service.ReadmeNames[i]),
			InfoType:     service.FileInfoType(stat.InfoType),
			Name:         service.ReadmeNames[i],
			Path:         stat.Path,
			Permission:   string(stat.Permission),
			Size:         stat.Size,
		}, nil
	}

	return nil, nil
}

// requireExists reports a path that is not in the catalog at all, asking as the service's
// own account so that a path the caller merely cannot see is not reported as missing.
func (h *Listings) requireExists(ctx context.Context, path string) error {
	proxy, err := h.deps.OpenProxyScope(ctx)
	if err != nil {
		return err
	}
	defer proxy.Close()

	stat, err := proxy.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}
	if !stat.Exists {
		return apierror.New(apierror.ErrDoesNotExist).With("paths", []string{path})
	}
	return nil
}
