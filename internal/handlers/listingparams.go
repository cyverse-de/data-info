package handlers

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/cyverse-de/data-info/internal/service"
	"github.com/labstack/echo/v4"
)

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
