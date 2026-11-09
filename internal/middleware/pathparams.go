package middleware

import (
	"net/url"

	"github.com/labstack/echo/v4"
)

// RouteOnEscapedPath makes the router always match against the escaped path. echo routes on
// URL.RawPath when it is set and URL.Path otherwise, and net/url keeps RawPath only for
// non-canonical encodings, so a parameter arrived decoded for %20 but still encoded for %40.
// It must run as a Pre middleware, before routing; DecodePathParams then decodes each value
// exactly once.
func RouteOnEscapedPath() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			u := c.Request().URL
			u.RawPath = u.EscapedPath()
			return next(c)
		}
	}
}

// DecodePathParams percent-decodes every path parameter once, as the Clojure service's router
// did, so /groups/%40grouper-x names the group @grouper-x. It relies on RouteOnEscapedPath.
func DecodePathParams() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			values := c.ParamValues()
			decoded := make([]string, len(values))
			for i, value := range values {
				unescaped, err := url.PathUnescape(value)
				if err != nil {
					// net/http rejects malformed escapes before routing, so this is not
					// expected; leave the value as it came.
					unescaped = value
				}
				decoded[i] = unescaped
			}
			c.SetParamValues(decoded...)
			return next(c)
		}
	}
}
