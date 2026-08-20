package middleware

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
)

// IdleTimeout applies a per-request idle timeout to the connection.
//
// This replaces the Clojure service's Jetty customizer (src/data_info/util/jetty.clj),
// which called Endpoint.setIdleTimeout with a short budget for ordinary requests and a
// long one for the upload endpoints.
//
// The semantics that matter are *idle*, not absolute. A plain deadline would cap the total
// duration of a request, so a large download -- which is not an upload route and so gets
// the short budget -- would be truncated mid-transfer once it exceeded the budget, no
// matter how healthy the connection was. An idle timeout instead fires only when nothing
// has moved for the whole budget, so a transfer that keeps making progress runs as long as
// it needs to.
//
// isLong decides which budget a request gets. It runs after routing, so it can match on
// the matched route rather than the raw path.
func IdleTimeout(normal, long time.Duration, isLong func(echo.Context) bool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			budget := normal
			if isLong != nil && isLong(c) {
				budget = long
			}

			rc := http.NewResponseController(c.Response())
			ext := &extender{rc: rc, budget: budget}

			if !ext.extend() {
				// The writer cannot take deadlines -- an httptest recorder, say. Nothing
				// to enforce, so serve the request rather than failing it.
				return next(c)
			}

			req := c.Request()
			if req.Body != nil {
				req.Body = &idleReader{ReadCloser: req.Body, ext: ext}
			}
			c.Response().Writer = &idleWriter{ResponseWriter: c.Response().Writer, ext: ext}

			return next(c)
		}
	}
}

// extender pushes the connection's deadlines forward as bytes move.
type extender struct {
	rc     *http.ResponseController
	budget time.Duration

	// last is when the deadline was most recently set. Refreshing on every read and
	// write would mean two syscalls per buffer, so it is refreshed only once the budget
	// is a quarter spent -- close enough to an idle timeout, cheap enough to ignore.
	last time.Time
}

// extend pushes both deadlines out by the budget. It reports whether the connection
// supports deadlines at all.
func (e *extender) extend() bool {
	deadline := time.Now()
	if !e.last.IsZero() && deadline.Sub(e.last) < e.budget/4 {
		return true
	}
	e.last = deadline
	deadline = deadline.Add(e.budget)

	if err := e.rc.SetReadDeadline(deadline); err != nil {
		return !errors.Is(err, http.ErrNotSupported)
	}
	if err := e.rc.SetWriteDeadline(deadline); err != nil {
		return !errors.Is(err, http.ErrNotSupported)
	}
	return true
}

// idleReader extends the deadline as the request body is consumed, so a slow but
// progressing upload is not cut off.
type idleReader struct {
	io.ReadCloser
	ext *extender
}

func (r *idleReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		r.ext.extend()
	}
	return n, err
}

// idleWriter extends the deadline as the response is written, so a large download is not
// cut off part way through.
type idleWriter struct {
	http.ResponseWriter
	ext *extender
}

func (w *idleWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if n > 0 {
		w.ext.extend()
	}
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying connection through this
// wrapper, which is what makes Flush and the deadline calls work.
func (w *idleWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
