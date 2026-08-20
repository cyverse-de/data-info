package handlers

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/cyverse-de/data-info/internal/service"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// DefaultListingLimit is how many entries a listing returns when the caller does not say.
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

	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}
	if !stat.Exists {
		return apierror.New(apierror.ErrDoesNotExist).With("paths", []string{path})
	}
	if stat.Type != rods.ObjectTypeDir {
		return apierror.New(apierror.ErrNotAFolder).With("paths", []string{path})
	}

	rows, err := scope.Listing(ctx, rods.ListingQuery{
		Path:          path,
		SortColumn:    icat.DefaultSortColumn,
		SortDirection: icat.SortAscending,
		Limit:         DefaultListingLimit,
	}).Get(ctx)
	if err != nil {
		return err
	}

	folders := make([]service.FolderEntry, 0, len(rows))
	for _, row := range rows {
		if row.IsCollection() {
			folders = append(folders, service.FolderEntryOf(row, user, h.deps.Layout))
		}
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

// pathFromRoute reconstructs the iRODS path a wildcard route was asked about.
//
// The path is taken from the raw, still-encoded URL rather than from echo's decoded
// parameter. iRODS names may contain a percent, a hash or an encoded slash, and echo decodes
// the wildcard once before the handler sees it -- so a file named with %2F would arrive
// looking like two path elements.
func pathFromRoute(c echo.Context) (string, error) {
	zone := c.Param("zone")
	if zone == "" {
		return "", schemaError("a zone is required")
	}

	escaped := c.Request().URL.EscapedPath()

	// Everything after the zone element is the path within the zone.
	marker := "/" + zone + "/"
	index := strings.Index(escaped, marker)
	if index < 0 {
		return "", schemaError("the request path does not name a zone")
	}
	remainder := escaped[index+len(marker):]

	decoded, err := decodePathSegments(remainder)
	if err != nil {
		return "", schemaError("the request path is not valid")
	}

	return "/" + zone + "/" + decoded, nil
}

// urlPathUnescape decodes one path segment.
func urlPathUnescape(segment string) (string, error) { return url.PathUnescape(segment) }

// decodePathSegments unescapes each element of a path separately, so that an encoded slash
// inside a name stays part of that name rather than becoming a separator.
func decodePathSegments(raw string) (string, error) {
	segments := strings.Split(raw, "/")
	out := make([]string, 0, len(segments))

	for _, segment := range segments {
		decoded, err := urlPathUnescape(segment)
		if err != nil {
			return "", err
		}
		out = append(out, decoded)
	}
	return strings.Join(out, "/"), nil
}

// listingLimit reads the limit parameter.
func listingLimit(c echo.Context) (int, error) {
	raw := c.QueryParam("limit")
	if raw == "" {
		return DefaultListingLimit, nil
	}

	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return 0, schemaError("limit must be a positive integer")
	}
	return limit, nil
}

// listingOffset reads the offset parameter.
func listingOffset(c echo.Context) (int, error) {
	raw := c.QueryParam("offset")
	if raw == "" {
		return 0, nil
	}

	offset, err := strconv.Atoi(raw)
	if err != nil || offset < 0 {
		return 0, schemaError("offset must be a non-negative integer")
	}
	return offset, nil
}

// FolderListing handles the folder half of GET /data/path/{zone}/*.
//
// The same route serves a file download; the two are told apart by what is at the path, not
// by the request, so the dispatch happens here.
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

	// No caller check on this route. The reference validates the path as the requesting
	// user and nothing else, so an unknown user is reported as the path not existing --
	// which is literally true from that user's point of view, since they can see nothing.
	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}
	if !stat.Exists {
		return apierror.New(apierror.ErrDoesNotExist).With("path", path)
	}
	if stat.Type != rods.ObjectTypeDir {
		// Downloading a file through this route arrives with the write path; until then
		// say so rather than answering a listing for something that is not a folder.
		return apierror.New(apierror.ErrNotAFolder).With("path", path)
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
		// The reference lets an unrecognised field reach a bare exception and answers 500
		// without a code. Reporting it as a bad parameter is a deliberate improvement,
		// recorded in docs/deferred-fixes.md.
		return schemaError(err.Error())
	}

	infoTypes, includeUnknown := infoTypeFilter(c)

	rows, err := scope.Listing(ctx, rods.ListingQuery{
		Path:                   path,
		SortColumn:             column,
		SortDirection:          icat.ResolveSortDirection(c.QueryParam("sort-dir")),
		Limit:                  limit,
		Offset:                 offset,
		InfoTypes:              infoTypes,
		IncludeUnknownInfoType: includeUnknown,
	}).Get(ctx)
	if err != nil {
		return err
	}

	rule := badNameRule(c, h.deps.BadChars)

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

	return writeJSONOK(c, service.ListingOf(self, rows, rule))
}

// pathBase is the last element of an iRODS path.
func pathBase(p string) string { return paths.Base(p) }

// infoTypeFilter reads the info-type parameters.
//
// "unknown" in the list is not an info type but an instruction to keep objects that have
// none, which is how the reference spells it.
func infoTypeFilter(c echo.Context) (types []string, includeUnknown bool) {
	for _, value := range c.QueryParams()["info-type"] {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			switch {
			case part == "":
			case strings.EqualFold(part, "unknown"):
				includeUnknown = true
			default:
				types = append(types, part)
			}
		}
	}
	return types, includeUnknown
}

// badNameRule reads the parameters a client uses to have entries flagged as unrenderable.
func badNameRule(c echo.Context, defaultChars string) service.BadNameRule {
	chars := defaultChars
	if supplied, ok := c.QueryParams()["bad-chars"]; ok && len(supplied) > 0 {
		chars = supplied[0]
	}

	return service.BadNameRule{
		Chars: chars,
		Names: splitAll(c.QueryParams()["bad-name"]),
		Paths: splitAll(c.QueryParams()["bad-path"]),
	}
}

// splitAll flattens repeated parameters that may also be comma-separated, which is how the
// reference accepted them.
func splitAll(values []string) []string {
	var out []string
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				out = append(out, trimmed)
			}
		}
	}
	return out
}

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

	if err := requireKnownUser(ctx, scope, user, false); err != nil {
		return err
	}

	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}
	if !stat.Exists {
		return apierror.New(apierror.ErrDoesNotExist).With("paths", []string{path})
	}
	if !permits(stat.Permission, rods.PermissionRead) {
		return apierror.New(apierror.ErrNotReadable).With("path", path).With("user", user)
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

	user := c.QueryParam("user")
	if strings.TrimSpace(user) == "" {
		return apierror.New(apierror.ErrIllegalArgument).
			WithStatus(http.StatusUnprocessableEntity).With("param", "user")
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
	if !stat.Exists || !permits(stat.Permission, rods.PermissionRead) {
		return apierror.New(apierror.ErrNotReadable).WithStatus(http.StatusForbidden).With("id", id)
	}

	return c.NoContent(http.StatusOK)
}
