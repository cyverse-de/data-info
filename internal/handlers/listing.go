package handlers

import (
	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/cyverse-de/data-info/internal/service"
	"github.com/labstack/echo/v4"
)

// DefaultListingLimit is unused: listings reject a request that has no limit.
const DefaultListingLimit = 1000

// Listings serves the folder listing and navigation endpoints.
type Listings struct {
	deps Deps
}

// NewListings builds the listing handlers.
func NewListings(deps Deps) *Listings { return &Listings{deps: deps} }

// Navigation handles GET /navigation/path/{zone}/*.
//
// It reports a folder's own status plus its immediate subfolders, and nothing about the
// files inside it. That asymmetry is the endpoint's purpose: it backs a tree view.
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

	// Existence is asked as the service's own account so that a folder the caller cannot read
	// is reported as unreadable rather than as absent. The Clojure service checks the two
	// separately and the codes differ; asking both as the caller collapses them.
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

	// The folders-only query rather than the paged listing filtered down. It is unpaged,
	// so a folder with more subfolders than a page is not silently truncated, and it
	// touches only the collection table -- the paged query would build intermediate
	// results over every data object in the folder before discarding them, which on a home
	// directory holding tens of thousands of files makes a tree view far more expensive
	// than it needs to be.
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

// FolderListing handles GET /data/path/{zone}/*.
//
// The route serves a listing for a folder and the file itself for a file. The two are told
// apart by what is at the path rather than by anything in the request, so the dispatch
// happens here.
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

	// No caller check on this route. The Clojure service validates the path as the requesting
	// user and nothing else, so an unknown user is reported as the path not existing -- which
	// is literally true from that user's point of view, since they can see nothing.
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
		// The Clojure service lets an unrecognised field reach a bare exception and answers
		// 500 without a code. Reporting it as a bad parameter is a deliberate improvement,
		// recorded in docs/deferred-fixes.md.
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
		BadName:      rule.Matches(stat.Path, pathBase(stat.Path)),
		Name:         pathBase(stat.Path),
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
