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

// pathFromRoute reads the iRODS path from the still-encoded URL because echo decodes the
// wildcard, which would turn a %2F inside a name into a separator.
func pathFromRoute(c echo.Context) (string, error) {
	zone := c.Param("zone")
	if zone == "" {
		return "", schemaError("a zone is required")
	}

	escaped := c.Request().URL.EscapedPath()

	// Strip the static prefix rather than searching for the zone, which could match a route
	// element (a zone named "data") or arrive percent-encoded.
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

// listingLimit requires limit; a default would silently return only the first page of a large
// folder.
func listingLimit(c echo.Context) (int, error) {
	raw := c.QueryParam("limit")
	if raw == "" {
		// Callers read the "parameters" key.
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

// infoTypeFilter reads the info-type parameters. "unknown" both selects objects with no info
// type and stays a value to match, since info-typer records it on files it can't identify.
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
// Only the request's bad-chars apply; Deps.BadChars governs new names and would flag
// displayable existing files.
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

// splitAll flattens repeated parameters that may also be comma-separated.
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
