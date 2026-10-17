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

// Chunks serves the manifest and chunk-reading endpoints behind the DE's file preview; each has
// an id route and a path route that share everything after path resolution.
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

// chunkResponse is a span of a file's bytes. Every number is deliberately a string; the client
// parses them back.
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
	return h.serveTabular(c, h.pathFromID)
}

// TabularChunkByPath handles GET /data/by-path/chunks-tabular/{path}.
func (h *Chunks) TabularChunkByPath(c echo.Context) error {
	return h.serveTabular(c, pathFromWildcard)
}

// serveTabular checks paging before the caller and path, so page=0 on a missing path answers
// ERR_PAGE_NOT_POS rather than ERR_DOES_NOT_EXIST, as callers have always seen.
func (h *Chunks) serveTabular(
	c echo.Context,
	resolve func(context.Context, echo.Context, *rods.Scope) (string, error),
) error {
	params, err := tabularParams(c)
	if err != nil {
		return err
	}

	return h.serve(c, resolve, func(ctx context.Context, c echo.Context, scope *rods.Scope, user, path string) error {
		return h.tabularChunk(ctx, c, scope, user, path, params)
	})
}

// tabularRequest carries the parameters a tabular chunk request was made with.
type tabularRequest struct {
	// Separator is percent-decoded again after echo's decoding, as the Clojure service did.
	// It may be empty; see ParseDelimited.
	Separator string

	Page int64
	Size int64
}

// tabularParams reads the paging parameters and rejects a non-positive page or size.
func tabularParams(c echo.Context) (tabularRequest, error) {
	separator, err := presentParam(c, "separator")
	if err != nil {
		return tabularRequest{}, err
	}
	decoded, err := url.QueryUnescape(separator)
	if err != nil {
		return tabularRequest{}, schemaError("separator must be a url-encoded character")
	}

	page, err := requiredIntParam(c, "page")
	if err != nil {
		return tabularRequest{}, err
	}
	size, err := requiredIntParam(c, "size")
	if err != nil {
		return tabularRequest{}, err
	}

	// page is reported as a number but chunk-size as a string, as the Clojure service did.
	if page <= 0 {
		return tabularRequest{}, apierror.New(apierror.ErrPageNotPos).With("page", page)
	}
	if size <= 0 {
		return tabularRequest{}, apierror.New(apierror.ErrChunkTooSmall).
			With("chunk-size", strconv.FormatInt(size, 10))
	}

	return tabularRequest{Separator: decoded, Page: page, Size: size}, nil
}

// serve resolves the caller and path and checks it is a readable file. The file check comes
// first, so an unreadable folder is reported as not a file.
func (h *Chunks) serve(
	c echo.Context,
	resolve func(context.Context, echo.Context, *rods.Scope) (string, error),
	act func(context.Context, echo.Context, *rods.Scope, string, string) error,
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

	path, err := resolve(ctx, c, scope)
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

	return act(ctx, c, scope, user, path)
}

// pathFromID resolves the data-id path parameter.
func (h *Chunks) pathFromID(ctx context.Context, c echo.Context, scope *rods.Scope) (string, error) {
	return resolveID(ctx, scope, c.Param("data-id"))
}

// manifest reports how to render a file and where else it can be fetched from.
func (h *Chunks) manifest(ctx context.Context, c echo.Context, scope *rods.Scope, _, path string) error {
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

	return writeJSONOK(c, manifestResponse{
		ContentType: mediatype.OfName(path),
		InfoType:    service.FileInfoType(stat.InfoType),
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
func (h *Chunks) chunk(ctx context.Context, c echo.Context, scope *rods.Scope, user, path string) error {
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

	// start and chunk-size echo the request, not what was read.
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
//
// The paging parameters were read and checked before the validators ran; see serveTabular.
func (h *Chunks) tabularChunk(
	ctx context.Context,
	c echo.Context,
	scope *rods.Scope,
	user, path string,
	req tabularRequest,
) error {
	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}

	plan := service.PlanTabularPage(req.Page, req.Size, stat.Size)
	// The inclusive bound is the Clojure service's: one page past the end answers an empty page.
	// The error reports the zero-based page; the response reports the one-based one.
	if plan.Page > plan.Pages {
		return apierror.New(apierror.ErrInvalidPage).
			With("page", strconv.FormatInt(plan.Page, 10)).
			With("number-pages", strconv.FormatInt(plan.Pages, 10))
	}

	raw, err := h.read(ctx, scope, path, plan.LoadOffset, plan.LoadLength, stat.Size)
	if err != nil {
		return err
	}

	chunk := service.TrimToWholeLines(raw, req.Size, plan)
	rows, err := service.ParseDelimited(chunk, req.Separator)
	if err != nil {
		// Deliberately uncoded: an unparseable file answers 500 ERR_UNCHECKED_EXCEPTION, as
		// callers have always seen.
		return fmt.Errorf("parsing %q as delimited text: %w", path, err)
	}

	return writeJSONOK(c, tabularChunkResponse{
		Path:        path,
		Page:        strconv.FormatInt(req.Page, 10),
		NumberPages: strconv.FormatInt(plan.Pages, 10),
		User:        user,
		MaxCols:     strconv.Itoa(service.WidestRow(rows)),
		ChunkSize:   strconv.Itoa(len(chunk)),
		FileSize:    strconv.FormatInt(stat.Size, 10),
		CSV:         rows,
	})
}

// read fetches a span of a file as UTF-8, dropping partial edge characters. The length is
// clamped to the file, so a huge request on a small file doesn't allocate a huge buffer.
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

// pathFromWildcard reads the whole path, zone included, from the raw URL for the same reason
// as pathFromRoute.
func pathFromWildcard(_ context.Context, c echo.Context, _ *rods.Scope) (string, error) {
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
