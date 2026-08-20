package middleware

import (
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
)

// Deadlines applies a per-request read and write deadline.
//
// This replaces the Clojure service's Jetty idle-timeout customizer, which gave the two
// upload endpoints a much longer timeout than everything else. Reproducing the split
// matters: a single global timeout is either too short for a multi-gigabyte upload or too
// long for an ordinary request, and the failure mode of getting it wrong only shows up on
// large files, which is to say not in QA.
//
// isLong decides which budget a request gets. It runs after routing, so it can match on
// the matched route rather than the raw path.
func Deadlines(normal, long time.Duration, isLong func(echo.Context) bool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			budget := normal
			if isLong != nil && isLong(c) {
				budget = long
			}

			rc := http.NewResponseController(c.Response())
			deadline := time.Now().Add(budget)

			// A server that cannot set deadlines -- an httptest recorder, say -- is not a
			// reason to fail the request. ErrNotSupported is expected there and nowhere
			// else, so it is ignored rather than reported.
			if err := rc.SetReadDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
				return err
			}
			if err := rc.SetWriteDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
				return err
			}

			return next(c)
		}
	}
}
