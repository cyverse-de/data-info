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

// download streams a data object as the response body.
//
// This is the other half of GET /data/path/{zone}/*: the route serves a listing for a folder
// and the file itself for a file, and which one is decided by what is at the path rather
// than by anything in the request.
//
// The body is streamed rather than read. A data object is as large as whatever a user put
// there, so holding one in memory to hand it over would size the service to its largest
// file.
func (h *Listings) download(c echo.Context, ctx context.Context, scope *rods.Scope, stat rods.Stat) error {
	attachment, err := boolParam(c, "attachment")
	if err != nil {
		return err
	}

	name := paths.Base(stat.Path)
	response := c.Response()
	response.Header().Set(echo.HeaderContentType, mediatype.OfName(stat.Path))
	response.Header().Set("Content-Disposition", contentDisposition(attachment, name))

	// An empty file is answered without opening it. iRODS is content to open a zero-length
	// object, but the reference short-circuits it and doing the same keeps a connection
	// free for something that needs one.
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

// contentDisposition builds an RFC 6266 header value.
//
// Both spellings are emitted. filename* carries the real UTF-8 name percent-encoded, which
// keeps the header pure ASCII on the wire -- a header is transported as ISO-8859-1, so a
// name with a character above U+00FF would otherwise arrive mangled. filename= is the ASCII
// fallback for a client that does not understand the other one, with everything it cannot
// carry replaced rather than dropped, so the name stays the same length and a quote or a
// backslash cannot end the parameter early.
func contentDisposition(attachment bool, filename string) string {
	kind := "inline"
	if attachment {
		kind = "attachment"
	}

	fallback := nonASCII.ReplaceAllString(filename, "_")
	fallback = strings.NewReplacer(`"`, "_", `\`, "_").Replace(fallback)

	return kind + `; filename="` + fallback + `"; filename*=UTF-8''` + rfc5987Encode(filename)
}

// rfc5987Encode percent-encodes a filename for an RFC 5987 filename* parameter.
//
// url.QueryEscape and then two fixes, which is what the reference does with URLEncoder: a
// space has to be %20 rather than a plus, since this is not a form, and an asterisk has to
// be encoded because RFC 5987's grammar does not admit it unescaped.
func rfc5987Encode(filename string) string {
	encoded := url.QueryEscape(filename)
	return strings.NewReplacer("+", "%20", "*", "%2A").Replace(encoded)
}
