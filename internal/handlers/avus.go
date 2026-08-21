package handlers

import (
	"context"
	"strings"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/clients/metadata"
	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/cyverse-de/data-info/internal/service"
	"github.com/labstack/echo/v4"
)

// avuChangeRequest is the body of the AVU add and set routes.
//
// Everything except irods-avus is forwarded to the metadata service untouched, so the rest of
// the body is kept as decoded JSON rather than modelled: its shape is that service's to
// change, and a struct here would silently drop whatever this one has not been taught about.
type avuChangeRequest struct {
	IRODSAVUs []service.AVU  `json:"irods-avus"`
	Rest      map[string]any `json:"-"`
}

// copyRequest is the body of POST /data/{data-id}/metadata/copy.
type copyRequest struct {
	DestinationIDs []string `json:"destination_ids"`
}

// avuChangeResponse is what the AVU mutations return.
type avuChangeResponse struct {
	Path string `json:"path"`
	User string `json:"user"`
}

// AVUs serves the metadata endpoints.
type AVUs struct{ deps Deps }

// NewAVUs builds the AVU handlers.
func NewAVUs(deps Deps) *AVUs { return &AVUs{deps: deps} }

// Get handles GET /data/{data-id}/metadata.
func (a *AVUs) Get(c echo.Context) error {
	return a.get(c, requireUser, false)
}

// AdminGet handles GET /admin/data/{data-id}/metadata, which also reports the DE's own AVUs.
//
// It acts as the service's own account rather than as the caller, so it sees everything --
// which is also why this route has no authorization of its own beyond being mounted under
// /admin. Reproduced as it is; see docs/deferred-fixes.md.
func (a *AVUs) AdminGet(c echo.Context) error {
	return a.get(c, a.proxyUser, true)
}

func (a *AVUs) get(c echo.Context, whom func(echo.Context) (string, error), includeReserved bool) error {
	ctx := c.Request().Context()

	user, err := whom(c)
	if err != nil {
		return err
	}

	scope, err := a.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	if err := requireKnownUser(ctx, scope, user, false); err != nil {
		return err
	}

	stat, err := a.readableItem(ctx, scope, user, c.Param("data-id"))
	if err != nil {
		return err
	}

	avus, err := scope.AVUs(ctx, stat.Path).Get(ctx)
	if err != nil {
		return err
	}

	// The metadata service's view is the base and this service's additions are merged over
	// it. That order is the reference's, and it means a key this service adds wins -- which
	// only matters for "path", which the metadata service does not report.
	out, err := a.deps.Metadata.ListAVUs(ctx, user, service.MetadataTargetType(stat.Type), c.Param("data-id"))
	if err != nil {
		return err
	}
	out["irods-avus"] = service.VisibleAVUs(avus, includeReserved)
	out["path"] = stat.Path

	return writeJSONOK(c, out)
}

// Add handles PATCH /data/{data-id}/metadata.
func (a *AVUs) Add(c echo.Context) error {
	return a.add(c, requireUser, false)
}

// AdminAdd handles PATCH /admin/data/{data-id}/metadata, which may write the DE's own AVUs.
func (a *AVUs) AdminAdd(c echo.Context) error {
	return a.add(c, a.proxyUser, true)
}

func (a *AVUs) add(c echo.Context, whom func(echo.Context) (string, error), allowReserved bool) error {
	ctx := c.Request().Context()

	user, err := whom(c)
	if err != nil {
		return err
	}

	body, err := bindAVUChange(c)
	if err != nil {
		return err
	}
	if !allowReserved {
		if err := requireNoReservedAVUs(body.IRODSAVUs); err != nil {
			return err
		}
	}

	scope, err := a.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	if err := requireKnownUser(ctx, scope, user, false); err != nil {
		return err
	}

	stat, err := a.readableItem(ctx, scope, user, c.Param("data-id"))
	if err != nil {
		return err
	}
	if err := requireWriteable(ctx, scope, stat.Path); err != nil {
		return err
	}

	// The metadata service first. Its half of the change is the one a caller is most likely
	// to be watching for, and doing it second would leave the two halves inconsistent for
	// longer when it fails.
	if len(body.Rest) > 0 {
		targetType := service.MetadataTargetType(stat.Type)
		if err := a.deps.Metadata.UpdateAVUs(ctx, user, targetType, c.Param("data-id"), body.Rest); err != nil {
			return err
		}
	}

	for _, avu := range body.IRODSAVUs {
		if err := scope.AddAVUIfAbsent(ctx, stat.Path, service.StoredAVU(avu)); err != nil {
			return err
		}
	}

	return writeJSONOK(c, avuChangeResponse{Path: stat.Path, User: user})
}

// Set handles PUT /data/{data-id}/metadata.
//
// The AVUs sent are the whole set afterwards, so anything the item carries that is not among
// them is removed -- except the DE's own, which are neither sent nor removed. A caller cannot
// wipe the service's bookkeeping by sending an empty list.
func (a *AVUs) Set(c echo.Context) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return err
	}

	body, err := bindAVUChange(c)
	if err != nil {
		return err
	}
	if err := requireNoReservedAVUs(body.IRODSAVUs); err != nil {
		return err
	}

	scope, err := a.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	if err := requireKnownUser(ctx, scope, user, false); err != nil {
		return err
	}

	stat, err := a.readableItem(ctx, scope, user, c.Param("data-id"))
	if err != nil {
		return err
	}
	if err := requireWriteable(ctx, scope, stat.Path); err != nil {
		return err
	}

	held, err := scope.AVUs(ctx, stat.Path).Get(ctx)
	if err != nil {
		return err
	}

	wanted := map[service.AVU]bool{}
	for _, avu := range body.IRODSAVUs {
		wanted[avu] = true
	}

	targetType := service.MetadataTargetType(stat.Type)
	if err := a.deps.Metadata.SetAVUs(ctx, user, targetType, c.Param("data-id"), body.Rest); err != nil {
		return err
	}

	// Removals first, then additions. An AVU that is in both sets survives untouched, which
	// is what makes setting the same list twice a no-op rather than a delete and a re-add.
	for _, avu := range service.VisibleAVUs(held, false) {
		if wanted[avu] {
			continue
		}
		if err := scope.DeleteAVU(ctx, stat.Path, service.StoredAVU(avu)); err != nil {
			return err
		}
	}
	for _, avu := range body.IRODSAVUs {
		if err := scope.AddAVUIfAbsent(ctx, stat.Path, service.StoredAVU(avu)); err != nil {
			return err
		}
	}

	return writeJSONOK(c, avuChangeResponse{Path: stat.Path, User: user})
}

// Copy handles POST /data/{data-id}/metadata/copy.
func (a *AVUs) Copy(c echo.Context) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return err
	}

	var body copyRequest
	if err := bindBody(c, &body); err != nil {
		return err
	}
	if err := a.deps.CheckPathCount(len(body.DestinationIDs)); err != nil {
		return err
	}

	scope, err := a.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	if err := requireKnownUser(ctx, scope, user, false); err != nil {
		return err
	}

	source, err := a.readableItem(ctx, scope, user, c.Param("data-id"))
	if err != nil {
		return err
	}

	destinations := make([]rods.Stat, 0, len(body.DestinationIDs))
	paths := make([]string, 0, len(body.DestinationIDs))
	targets := make([]metadata.CopyTarget, 0, len(body.DestinationIDs))

	for _, id := range body.DestinationIDs {
		path, err := resolveID(ctx, scope, id)
		if err != nil {
			return err
		}

		stat, err := scope.Stat(ctx, path).Get(ctx)
		if err != nil {
			return err
		}

		destinations = append(destinations, stat)
		paths = append(paths, stat.Path)
		targets = append(targets, metadata.CopyTarget{ID: id, Type: service.MetadataTargetType(stat.Type)})
	}

	if err := requireAllWriteable(ctx, scope, user, paths); err != nil {
		return err
	}

	sourceAVUs, err := scope.AVUs(ctx, source.Path).Get(ctx)
	if err != nil {
		return err
	}

	sourceType := service.MetadataTargetType(source.Type)
	if err := a.deps.Metadata.CopyAVUs(ctx, user, sourceType, c.Param("data-id"), targets); err != nil {
		return err
	}

	// Only what a caller can see is copied. The DE's own AVUs describe where something is
	// and what it is, so copying them onto another item would make that item claim to be
	// this one.
	visible := service.VisibleAVUs(sourceAVUs, false)
	for _, stat := range destinations {
		for _, avu := range visible {
			if err := scope.AddAVUIfAbsent(ctx, stat.Path, service.StoredAVU(avu)); err != nil {
				return err
			}
		}
	}

	return writeJSONOK(c, map[string]any{"user": user, "src": source.Path, "paths": paths})
}

// readableItem resolves a data id and checks the caller may read what it names.
func (a *AVUs) readableItem(ctx context.Context, scope *rods.Scope, user, id string) (rods.Stat, error) {
	path, err := resolveID(ctx, scope, id)
	if err != nil {
		return rods.Stat{}, err
	}

	stat, err := scope.Stat(ctx, strings.TrimRight(path, "/")).Get(ctx)
	if err != nil {
		return rods.Stat{}, err
	}
	if !rods.Permits(stat.Permission, rods.PermissionRead) {
		return rods.Stat{}, apierror.New(apierror.ErrNotReadable).
			With("path", stat.Path).
			With("user", user)
	}
	return stat, nil
}

// proxyUser answers with the service's own account, for the administrative routes.
func (a *AVUs) proxyUser(echo.Context) (string, error) { return a.deps.ProxyUser, nil }

// bindAVUChange decodes an AVU change, keeping what belongs to the metadata service separate
// from what belongs to iRODS.
func bindAVUChange(c echo.Context) (avuChangeRequest, error) {
	var raw map[string]any
	if err := decodeBody(c, &raw); err != nil {
		return avuChangeRequest{}, err
	}

	out := avuChangeRequest{Rest: map[string]any{}}
	for key, value := range raw {
		if key != "irods-avus" {
			out.Rest[key] = value
		}
	}

	list, ok := raw["irods-avus"].([]any)
	if !ok {
		return out, nil
	}

	for _, item := range list {
		object, ok := item.(map[string]any)
		if !ok {
			return avuChangeRequest{}, schemaError("each entry of irods-avus must be an object")
		}

		attribute, _ := object["attr"].(string)
		if strings.TrimSpace(attribute) == "" {
			return avuChangeRequest{}, schemaError("each entry of irods-avus needs a non-blank attr")
		}
		value, _ := object["value"].(string)
		unit, _ := object["unit"].(string)

		out.IRODSAVUs = append(out.IRODSAVUs, service.AVU{Attribute: attribute, Value: value, Unit: unit})
	}

	return out, nil
}

// requireNoReservedAVUs refuses a caller writing metadata that belongs to the DE.
func requireNoReservedAVUs(avus []service.AVU) error {
	for _, avu := range avus {
		if service.IsReservedAVU(avu.Attribute) {
			// The whole list, not the offending one: that is what the reference attaches,
			// and a caller sending a batch gets its own request echoed back.
			return apierror.New(apierror.ErrNotAuthorized).With("avus", avus)
		}
	}
	return nil
}
