package handlers

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/mediatype"
	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/cyverse-de/data-info/internal/service"
	"github.com/labstack/echo/v4"
)

// Chunks serves the manifest and the two chunk-reading endpoints, which together are what
// the DE's file preview is built on.
//
// Each has two routes -- one naming a data id and one naming a path -- that differ only in
// how the path is found. Everything after that is shared, which is also how the reference
// is arranged.
type Chunks struct {
	deps Deps
}

// NewChunks builds the manifest and chunking handlers.
func NewChunks(deps Deps) *Chunks { return &Chunks{deps: deps} }

// manifestResponse describes a file well enough for a client to decide how to show it.
type manifestResponse struct {
	ContentType string        `json:"content-type"`
	InfoType    string        `json:"infoType"`
	URLs        []manifestURL `json:"urls"`
}

// manifestURL is one address the file can be reached at other than through this service.
type manifestURL struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// chunkResponse is a span of a file's bytes.
//
// Every number is a string. That is the wire contract and not an oversight on this side:
// the reference stringifies each of them, and the client parses them back.
type chunkResponse struct {
	Path      string `json:"path"`
	User      string `json:"user"`
	Start     string `json:"start"`
	ChunkSize string `json:"chunk-size"`
	FileSize  string `json:"file-size"`
	Chunk     string `json:"chunk"`
}

// tabularChunkResponse is a page of a delimited file, parsed into rows.
type tabularChunkResponse struct {
	Path        string              `json:"path"`
	Page        string              `json:"page"`
	NumberPages string              `json:"number-pages"`
	User        string              `json:"user"`
	MaxCols     string              `json:"max-cols"`
	ChunkSize   string              `json:"chunk-size"`
	FileSize    string              `json:"file-size"`
	CSV         []map[string]string `json:"csv"`
}

// Manifest handles GET /data/{data-id}/manifest.
func (h *Chunks) Manifest(c echo.Context) error {
	return h.serve(c, h.pathFromID, h.manifest)
}

// ManifestByPath handles GET /data/by-path/manifest/{path}.
func (h *Chunks) ManifestByPath(c echo.Context) error {
	return h.serve(c, pathFromWildcard, h.manifest)
}

// Chunk handles GET /data/{data-id}/chunks.
func (h *Chunks) Chunk(c echo.Context) error {
	return h.serve(c, h.pathFromID, h.chunk)
}

// ChunkByPath handles GET /data/by-path/chunks/{path}.
func (h *Chunks) ChunkByPath(c echo.Context) error {
	return h.serve(c, pathFromWildcard, h.chunk)
}

// TabularChunk handles GET /data/{data-id}/chunks-tabular.
func (h *Chunks) TabularChunk(c echo.Context) error {
	return h.serve(c, h.pathFromID, h.tabularChunk)
}

// TabularChunkByPath handles GET /data/by-path/chunks-tabular/{path}.
func (h *Chunks) TabularChunkByPath(c echo.Context) error {
	return h.serve(c, pathFromWildcard, h.tabularChunk)
}

// serve runs the part every one of these six routes shares: find the caller, find the path,
// and establish that it is a file the caller can read.
//
// The validators are the clj-irods family, which reports a path inside a list even when the
// endpoint concerns exactly one. The order is theirs too -- the file check before the
// readability check -- because a caller pointing at an unreadable folder is told it is not a
// file rather than that they cannot read it.
func (h *Chunks) serve(
	c echo.Context,
	resolve func(echo.Context, context.Context, *rods.Scope) (string, error),
	act func(echo.Context, context.Context, *rods.Scope, string, string) error,
) error {
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

	if err := requireKnownUser(ctx, scope, user, true); err != nil {
		return err
	}

	path, err := resolve(c, ctx, scope)
	if err != nil {
		return err
	}
	path = strings.TrimRight(path, "/")

	if err := requireRodsExists(ctx, scope, path); err != nil {
		return err
	}
	if err := requireRodsIsFile(ctx, scope, path); err != nil {
		return err
	}
	if err := requireRodsReadable(ctx, scope, user, path); err != nil {
		return err
	}

	return act(c, ctx, scope, user, path)
}

// pathFromID resolves the data-id path parameter.
func (h *Chunks) pathFromID(c echo.Context, ctx context.Context, scope *rods.Scope) (string, error) {
	return resolveID(ctx, scope, c.Param("data-id"))
}

// manifest reports how to render a file and where else it can be fetched from.
func (h *Chunks) manifest(c echo.Context, ctx context.Context, scope *rods.Scope, _, path string) error {
	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}

	readable, err := h.anonymouslyReadable(ctx, path)
	if err != nil {
		return err
	}

	urls := make([]manifestURL, 0, 1)
	if readable {
		urls = append(urls, manifestURL{Label: "anonymous", URL: h.deps.anonURL(path)})
	}

	// "unknown" rather than an empty string for a file nothing has typed yet, which is what
	// the client switches on.
	infoType := stat.InfoType
	if infoType == "" {
		infoType = "unknown"
	}

	return writeJSONOK(c, manifestResponse{
		ContentType: mediatype.OfName(path),
		InfoType:    infoType,
		URLs:        urls,
	})
}

// anonymouslyReadable reports whether the anonymous account can read a path, which is what
// decides whether the manifest offers an anon-files URL.
func (h *Chunks) anonymouslyReadable(ctx context.Context, path string) (bool, error) {
	if h.deps.AnonUser == "" {
		return false, nil
	}

	anon, err := h.deps.OpenScope(ctx, h.deps.AnonUser)
	if err != nil {
		return false, err
	}
	defer anon.Close()

	stat, err := anon.Stat(ctx, path).Get(ctx)
	if err != nil {
		return false, err
	}
	return rods.Permits(stat.Permission, rods.PermissionRead), nil
}

// chunk returns a span of a file's bytes.
func (h *Chunks) chunk(c echo.Context, ctx context.Context, scope *rods.Scope, user, path string) error {
	position, err := requiredIntParam(c, "position")
	if err != nil {
		return err
	}
	size, err := requiredIntParam(c, "size")
	if err != nil {
		return err
	}

	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}

	chunk, err := h.read(ctx, scope, path, position, size, stat.Size)
	if err != nil {
		return err
	}

	// start and chunk-size echo what was asked for, not what was read. A caller that asked
	// for a megabyte of a short file is told a megabyte, and the reference says the same.
	return writeJSONOK(c, chunkResponse{
		Path:      path,
		User:      user,
		Start:     strconv.FormatInt(position, 10),
		ChunkSize: strconv.FormatInt(size, 10),
		FileSize:  strconv.FormatInt(stat.Size, 10),
		Chunk:     chunk,
	})
}

// tabularChunk returns a page of a delimited file, parsed into rows.
func (h *Chunks) tabularChunk(c echo.Context, ctx context.Context, scope *rods.Scope, user, path string) error {
	separator, err := requiredParam(c, "separator")
	if err != nil {
		return err
	}
	// The separator arrives url-encoded, because a tab cannot travel in a query string as
	// itself. %09 is the one the DE sends for a TSV.
	decoded, err := url.QueryUnescape(separator)
	if err != nil || decoded == "" {
		return schemaError("separator must be a url-encoded character")
	}

	page, err := requiredIntParam(c, "page")
	if err != nil {
		return err
	}
	size, err := requiredIntParam(c, "size")
	if err != nil {
		return err
	}

	// Both checks come before anything is read, and each has its own code. They run after
	// the path has been validated here, where the reference runs them before -- its
	// pre-hook fires ahead of the function body. Nothing observable turns on the order: a
	// request that fails both gets one of two 500s either way.
	if page <= 0 {
		return apierror.New(apierror.ErrPageNotPos).With("page", page)
	}
	if size <= 0 {
		return apierror.New(apierror.ErrChunkTooSmall).With("chunk-size", strconv.FormatInt(size, 10))
	}

	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}

	plan := service.PlanTabularPage(page, size, stat.Size)
	// The bound is inclusive, so a request for one page past the end is accepted and
	// answers with an empty page. That is the reference's arithmetic, and the page it
	// reports here is the zero-based one while the response reports the one-based one.
	if plan.Page > plan.Pages {
		return apierror.New(apierror.ErrInvalidPage).
			With("page", strconv.FormatInt(plan.Page, 10)).
			With("number-pages", strconv.FormatInt(plan.Pages, 10))
	}

	raw, err := h.read(ctx, scope, path, plan.LoadOffset, plan.LoadLength, stat.Size)
	if err != nil {
		return err
	}

	chunk := service.TrimToWholeLines(raw, size, plan)
	rows, err := service.ParseDelimited(chunk, []rune(decoded)[0])
	if err != nil {
		// Not an error code of its own: the reference lets the parser's exception reach the
		// default handler, which answers 500 with ERR_UNCHECKED_EXCEPTION. Naming it here
		// would report a 400 for a file the reference calls a server fault.
		return fmt.Errorf("parsing %q as delimited text: %w", path, err)
	}

	return writeJSONOK(c, tabularChunkResponse{
		Path:        path,
		Page:        strconv.FormatInt(page, 10),
		NumberPages: strconv.FormatInt(plan.Pages, 10),
		User:        user,
		MaxCols:     strconv.Itoa(service.WidestRow(rows)),
		ChunkSize:   strconv.Itoa(len(chunk)),
		FileSize:    strconv.FormatInt(stat.Size, 10),
		CSV:         rows,
	})
}

// read fetches a span of a file, decoded as UTF-8 with its partial edge characters dropped.
//
// The length is narrowed to what the file actually holds before the read. The reference
// allocates whatever was asked for and reads what it finds, which gives the same answer;
// doing it this way means a caller asking for a gigabyte of a small file allocates the small
// file rather than the gigabyte.
func (h *Chunks) read(ctx context.Context, scope *rods.Scope, path string, offset, length, fileSize int64) (string, error) {
	if offset < 0 || length < 0 {
		return "", schemaError("position and size must not be negative")
	}
	if offset >= fileSize {
		return "", nil
	}
	if remaining := fileSize - offset; length > remaining {
		length = remaining
	}

	buffer, err := scope.ReadAt(ctx, path, offset, length)
	if err != nil {
		return "", err
	}
	return service.DecodeChunk(buffer, offset > 0), nil
}

// pathFromWildcard reconstructs the iRODS path a by-path route was asked about.
//
// The whole wildcard is the path, zone included, so unlike pathFromRoute there is no zone
// parameter to put back in front of it. The raw URL is used for the same reason as there:
// echo decodes the wildcard once before the handler sees it, so a name containing an encoded
// slash would otherwise arrive looking like two path elements.
func pathFromWildcard(c echo.Context, _ context.Context, _ *rods.Scope) (string, error) {
	escaped := c.Request().URL.EscapedPath()

	prefix := strings.TrimSuffix(c.Path(), "*")
	if !strings.HasPrefix(escaped, prefix) {
		return "", schemaError("the request path does not match its route")
	}

	decoded, err := decodePathSegments(strings.TrimPrefix(escaped[len(prefix):], "/"))
	if err != nil {
		return "", schemaError("the request path is not valid")
	}
	if decoded == "" {
		return "", schemaError("a path is required")
	}
	return "/" + decoded, nil
}
