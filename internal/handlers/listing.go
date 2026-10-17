package handlers

import (
	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/cyverse-de/data-info/internal/service"
	"github.com/labstack/echo/v4"
)

// Listings serves the folder listing and navigation endpoints.
type Listings struct {
	deps Deps
}

// NewListings builds the listing handlers.
func NewListings(deps Deps) *Listings { return &Listings{deps: deps} }

// Navigation handles GET /navigation/path/{zone}/*, reporting a folder and its immediate
// subfolders but no files, for a tree view.
func (h *Listings) Navigation(c echo.Context) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return err
	}

	path, err := pathFromRoute(c)
	if err != nil {
		return err
	}

	scope, err := h.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	if err := requireKnownUser(ctx, scope, user, true); err != nil {
		return err
	}

	// Existence is asked as the service account so a folder the caller can't read reports
	// ERR_NOT_READABLE rather than ERR_DOES_NOT_EXIST.
	if err := h.requireExists(ctx, path); err != nil {
		return err
	}

	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}
	if !stat.Exists || !rods.Permits(stat.Permission, rods.PermissionRead) {
		return apierror.New(apierror.ErrNotReadable).With("paths", []string{path}).With("user", user)
	}
	if stat.Type != rods.ObjectTypeDir {
		return apierror.New(apierror.ErrNotAFolder).With("paths", []string{path})
	}

	// Not a filtered page: this is unpaged, so nothing is truncated, and it avoids scanning
	// every data object in a large folder.
	rows, err := scope.Subfolders(ctx, path).Get(ctx)
	if err != nil {
		return err
	}

	folders := make([]service.FolderEntry, 0, len(rows))
	for _, row := range rows {
		folders = append(folders, service.FolderEntryOf(row, user, h.deps.Layout))
	}

	// The folder's own status carries only the fields this endpoint reports.
	self, err := service.StatOf(ctx, scope, user, path, service.StatOptions{
		Fields: service.ParseFieldSet("id,label,path,date-created,date-modified,permission", ""),
		Layout: h.deps.Layout,
	})
	if err != nil {
		return err
	}

	return writeJSONOK(c, map[string]any{
		"folder": navigationFolder{Stat: self, Folders: folders},
	})
}

// navigationFolder is a folder's status with its subfolders alongside.
type navigationFolder struct {
	service.Stat
	Folders []service.FolderEntry `json:"folders"`
}

// FolderListing handles GET /data/path/{zone}/*, answering with a listing for a folder or the
// contents for a file.
func (h *Listings) FolderListing(c echo.Context) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return err
	}

	path, err := pathFromRoute(c)
	if err != nil {
		return err
	}

	scope, err := h.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	// No caller check: an unknown user can see nothing, so the path is reported as missing.
	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}
	if !stat.Exists {
		return apierror.New(apierror.ErrDoesNotExist).With("path", path)
	}
	if stat.Type != rods.ObjectTypeDir {
		return h.download(ctx, c, scope, stat)
	}

	limit, err := listingLimit(c)
	if err != nil {
		return err
	}
	offset, err := listingOffset(c)
	if err != nil {
		return err
	}

	column, err := icat.ResolveSortColumn(c.QueryParam("sort-field"))
	if err != nil {
		// Deliberately a schema failure rather than a bare 500; see docs/deferred-fixes.md.
		return schemaError(err.Error())
	}

	entityType, err := icat.ResolveEntityType(c.QueryParam("entity-type"))
	if err != nil {
		return schemaError(err.Error())
	}

	infoTypes, includeUnknown := infoTypeFilter(c)

	direction, err := icat.ResolveSortDirection(c.QueryParam("sort-dir"))
	if err != nil {
		return schemaError(err.Error())
	}

	rows, err := scope.Listing(ctx, rods.ListingQuery{
		Path:                   path,
		EntityType:             entityType,
		SortColumn:             column,
		SortDirection:          direction,
		Limit:                  limit,
		Offset:                 offset,
		InfoTypes:              infoTypes,
		IncludeUnknownInfoType: includeUnknown,
	}).Get(ctx)
	if err != nil {
		return err
	}

	rule := badNameRule(c)

	// The folder describes itself with the same fields as one of its entries.
	self := service.ListingEntry{
		ID:           stat.UUID,
		DateCreated:  stat.CreatedMS,
		DateModified: stat.ModifiedMS,
		BadName:      rule.Matches(stat.Path, paths.Base(stat.Path)),
		Name:         paths.Base(stat.Path),
		Path:         stat.Path,
		Permission:   string(stat.Permission),
	}

	listing := service.ListingOf(self, rows, rule)

	readme, err := h.findReadme(ctx, scope, path, rule)
	if err != nil {
		return err
	}
	if readme != nil {
		listing.Readme = readme
	}

	return writeJSONOK(c, listing)
}
