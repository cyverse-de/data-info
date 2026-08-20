// Package middleware holds data-info's cross-cutting HTTP behaviour.
package middleware

import (
	"net/url"
	"strings"

	"github.com/labstack/echo/v4"
)

// LowercaseQueryParams makes query parameter names case-insensitive.
//
// The Clojure service ran clojure-commons' wrap-lcase-params, which lowercases every
// parameter name, so ?Limit=10&INFO-TYPE=csv works today. Callers rely on it: dropping it
// would turn a working request into a silent "parameter missing".
//
// This runs as a Pre middleware, before routing, because it rewrites RawQuery.
func LowercaseQueryParams() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			req := c.Request()
			raw := req.URL.RawQuery
			if raw == "" || !hasUpper(raw) {
				return next(c)
			}

			// Rebuild in the order the parameters appeared. Ranging over a parsed
			// url.Values would use Go's randomized map iteration, so ?Limit=1&limit=2
			// would resolve to "1" or "2" at random from one request to the next, and
			// repeated path= values on the bulk endpoints would come back shuffled.
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
					// Leave a malformed query alone; the handler's own binding reports
					// it with the right error code rather than failing opaquely here.
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

// hasUpper reports whether any parameter *name* in the raw query contains an uppercase
// ASCII letter. Values are ignored deliberately: most requests carry an uppercase
// character somewhere in a path or username, and rewriting the query for those would cost
// a parse and re-encode on nearly every request for no benefit.
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
