package handlers

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/cyverse-de/data-info/internal/mediatype"
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/labstack/echo/v4"
)

// download streams a data object as the response body; FolderListing calls it when the path
// is a file.
func (h *Listings) download(ctx context.Context, c echo.Context, scope *rods.Scope, stat rods.Stat) error {
	attachment, err := boolParam(c, "attachment")
	if err != nil {
		return err
	}

	name := paths.Base(stat.Path)
	response := c.Response()
	response.Header().Set(echo.HeaderContentType, mediatype.OfName(stat.Path))
	response.Header().Set("Content-Disposition", contentDisposition(attachment, name))

	// Skip opening an empty file; it keeps an iRODS connection free.
	if stat.Size == 0 {
		return c.NoContent(http.StatusOK)
	}

	reader, err := scope.OpenFile(ctx, stat.Path)
	if err != nil {
		return err
	}
	// Before the scope is closed, not after: the reader borrows the scope's session.
	defer reader.Close() //nolint:errcheck // nothing was written

	return c.Stream(http.StatusOK, mediatype.OfName(stat.Path), reader)
}

// nonASCII matches everything outside the printable ASCII range, which is what the fallback
// filename has to be reduced to.
var nonASCII = regexp.MustCompile(`[^\x20-\x7E]`)

// contentDisposition emits RFC 6266 filename* with the UTF-8 name plus an ASCII filename=
// fallback whose unsafe characters, quote and backslash included, are replaced, not dropped.
func contentDisposition(attachment bool, filename string) string {
	kind := "inline"
	if attachment {
		kind = "attachment"
	}

	fallback := nonASCII.ReplaceAllString(filename, "_")
	fallback = strings.NewReplacer(`"`, "_", `\`, "_").Replace(fallback)

	return kind + `; filename="` + fallback + `"; filename*=UTF-8''` + rfc5987Encode(filename)
}

// rfc5987Encode percent-encodes for filename*: a space as %20 rather than +, and * escaped
// as RFC 5987 requires.
func rfc5987Encode(filename string) string {
	encoded := url.QueryEscape(filename)
	return strings.NewReplacer("+", "%20", "*", "%2A").Replace(encoded)
}
