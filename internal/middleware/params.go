// Package middleware holds data-info's cross-cutting HTTP behaviour.
package middleware

import (
	"net/url"
	"strings"

	"github.com/labstack/echo/v4"
)

// LowercaseQueryParams makes query parameter names case-insensitive, since callers send names
// like Limit and INFO-TYPE. It must run as a Pre middleware because it rewrites RawQuery.
func LowercaseQueryParams() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			req := c.Request()
			raw := req.URL.RawQuery
			if raw == "" || !hasUpper(raw) {
				return next(c)
			}

			// Walk the raw query so repeated values keep their order; ranging over a
			// parsed url.Values would shuffle them.
			lowered := make(url.Values)
			for rest := raw; rest != ""; {
				pair := rest
				if i := strings.IndexAny(rest, "&;"); i >= 0 {
					pair, rest = rest[:i], rest[i+1:]
				} else {
					rest = ""
				}
				if pair == "" {
					continue
				}

				name, value := pair, ""
				if i := strings.IndexByte(pair, '='); i >= 0 {
					name, value = pair[:i], pair[i+1:]
				}

				decodedName, err := url.QueryUnescape(name)
				if err != nil {
					// Leave a malformed query for the handler to report properly.
					return next(c)
				}
				decodedValue, err := url.QueryUnescape(value)
				if err != nil {
					return next(c)
				}

				lower := strings.ToLower(decodedName)
				lowered[lower] = append(lowered[lower], decodedValue)
			}

			req.URL.RawQuery = lowered.Encode()
			return next(c)
		}
	}
}

// hasUpper reports whether any parameter name has an uppercase ASCII letter. Values are
// skipped because they often do and need no rewrite.
func hasUpper(raw string) bool {
	for len(raw) > 0 {
		pair := raw
		if i := strings.IndexAny(raw, "&;"); i >= 0 {
			pair, raw = raw[:i], raw[i+1:]
		} else {
			raw = ""
		}
		name := pair
		if i := strings.IndexByte(pair, '='); i >= 0 {
			name = pair[:i]
		}
		for i := 0; i < len(name); i++ {
			if name[i] >= 'A' && name[i] <= 'Z' {
				return true
			}
		}
	}
	return false
}
