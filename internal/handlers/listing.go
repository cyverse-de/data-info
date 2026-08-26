package handlers

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/lazy"
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

	// The wildcard begins after the route's static prefix. Searching the whole path for
	// the zone name would find the prefix instead when a zone happens to share a name
	// with a route element -- a zone called "data" would make /data/path/data/home/x
	// resolve against the wrong occurrence -- and would fail outright when the zone
	// element arrives percent-encoded, since the route parameter is decoded and this is
	// not.
	prefix := routePrefix(c.Path())
	if !strings.HasPrefix(escaped, prefix) {
		return "", schemaError("the request path does not match its route")
	}

	remainder := strings.TrimPrefix(escaped[len(prefix):], "/")
	// The first element of what remains is the zone.
	if slash := strings.IndexByte(remainder, '/'); slash >= 0 {
		remainder = remainder[slash+1:]
	} else {
		remainder = ""
	}

	decoded, err := decodePathSegments(remainder)
	if err != nil {
		return "", schemaError("the request path is not valid")
	}

	return "/" + zone + "/" + decoded, nil
}

// urlPathUnescape decodes one path segment.
func urlPathUnescape(segment string) (string, error) { return url.PathUnescape(segment) }

// routePrefix is the static part of a wildcard route, up to but not including the zone
// parameter.
func routePrefix(pattern string) string {
	if index := strings.Index(pattern, "/:zone"); index >= 0 {
		return pattern[:index]
	}
	return ""
}

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

// listingLimit reads the limit parameter, which a folder listing requires.
//
// Defaulting it would be worse than it looks: a caller that forgot it would silently receive
// the first page of an arbitrarily large folder and have no way to know more existed. The
// Clojure service raises ERR_MISSING_QUERY_PARAMETER for the same reason.
func listingLimit(c echo.Context) (int, error) {
	raw := c.QueryParam("limit")
	if raw == "" {
		// The key is "parameters", not "param": that is what the Clojure service's
		// missing-arg validator attaches, and callers read it.
		return 0, apierror.New(apierror.ErrMissingQueryParam).With("parameters", "limit")
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
		return h.download(c, ctx, scope, stat)
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

// pathBase is the last element of an iRODS path.
func pathBase(p string) string { return paths.Base(p) }

// infoTypeFilter reads the info-type parameters.
//
// "unknown" means two things at once, and both are needed. It asks for objects that carry no
// info type at all, and it stays in the list of values to match, because info-typer records
// the literal string "unknown" on a file it could not identify. The Clojure service does the
// same: its condition is "the attribute is null OR it is one of these", with "unknown" left
// among the values. Dropping it here made a folder of untyped files come back empty.
func infoTypeFilter(c echo.Context) (types []string, includeUnknown bool) {
	for _, value := range c.QueryParams()["info-type"] {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if strings.EqualFold(part, "unknown") {
				includeUnknown = true
			}
			types = append(types, part)
		}
	}
	return types, includeUnknown
}

// badNameRule reads the parameters a client uses to have entries flagged as unrenderable.
//
// The characters come from the request and nowhere else. The service's own bad-chars
// setting governs what may be used in a *new* name; applying it here would flag existing
// files that are perfectly displayable, so a name containing an apostrophe would come back
// marked bad for a caller that never asked.
func badNameRule(c echo.Context) service.BadNameRule {
	var chars string
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
// Clojure service accepted them.
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
			InfoType:     infoTypeOrNil(stat.InfoType),
			Name:         service.ReadmeNames[i],
			Path:         stat.Path,
			Permission:   string(stat.Permission),
			Size:         stat.Size,
		}, nil
	}

	return nil, nil
}

// infoTypeOrNil reports null rather than an empty string when a file has no info type.
func infoTypeOrNil(infoType string) any {
	if infoType == "" {
		return nil
	}
	return infoType
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
