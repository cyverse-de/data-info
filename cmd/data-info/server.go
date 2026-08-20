package main

import (
	"net/http"
	"strconv"
	"time"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/config"
	"github.com/cyverse-de/data-info/internal/handlers"
	dimw "github.com/cyverse-de/data-info/internal/middleware"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/instrumentation/github.com/labstack/echo/otelecho"
)

// uploadRoutes are the endpoints that stream whole files and therefore get the long idle
// timeout rather than the ordinary one. The Clojure service singled out the same set in
// its Jetty customizer, matching #"/data/?" for POST and #"/data/[^/]+/?" for PUT.
//
// The routes themselves arrive with the write path; the budget is wired here so that they
// are never served under the ordinary timeout by omission.
var uploadRoutes = map[string]bool{
	"POST /data":         true,
	"POST /data/":        true,
	"PUT /data/:data-id": true,
}

// isUploadRoute reports whether the matched route streams a whole file.
func isUploadRoute(c echo.Context) bool {
	return uploadRoutes[c.Request().Method+" "+c.Path()]
}

// Deps are the backends the server talks to. They are passed in rather than constructed
// here so tests can exercise the real router without reaching the network, and so the
// iRODS and ICAT clients can replace the reachability probes when they land.
type Deps struct {
	IRODS handlers.Prober
	ICAT  handlers.Prober
}

// buildServer assembles the HTTP server. It is separate from main so tests can exercise
// the real router and middleware stack without binding a port.
func buildServer(cfg *config.Config, version string, log *logrus.Entry, deps Deps) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.HTTPErrorHandler = apierror.HTTPErrorHandler(errorLogger(log))

	// Pre runs before routing, which is required: this rewrites the query string.
	e.Pre(dimw.LowercaseQueryParams())

	e.Use(middleware.Recover())
	e.Use(otelecho.Middleware(handlers.ServiceName))
	e.Use(requestLogger(log))
	e.Use(dimw.IdleTimeout(cfg.Timeouts.Request, cfg.Timeouts.Upload, isUploadRoute))

	status := handlers.NewStatus(cfg, version, deps.IRODS, deps.ICAT)

	e.GET("/", status.Info)
	e.GET("/healthz", status.Healthz)
	e.GET("/readyz", status.Readyz)
	e.GET("/admin/config", status.AdminConfig)

	return e
}

// ProbeTimeout bounds a backend check. GET / probes iRODS on every call, so an
// unreachable backend has to fail fast rather than hold the request open; DNS for a
// nonexistent host can otherwise take far longer than the dial itself.
const ProbeTimeout = 3 * time.Second

// networkDeps builds the probes used in a running service.
func networkDeps(cfg *config.Config) Deps {
	return Deps{
		IRODS: handlers.TCPProber(cfg.IRODS.Host, cfg.IRODS.Port, ProbeTimeout),
		ICAT:  handlers.TCPProber(cfg.ICAT.Host, cfg.ICAT.Port, ProbeTimeout),
	}
}

// newHTTPServer wraps the router in a server whose timeouts suit streaming.
//
// ReadTimeout and WriteTimeout are deliberately zero. They are whole-request deadlines,
// and a multi-gigabyte upload or download legitimately outlives any value worth setting
// globally. Per-request deadlines are applied instead, long for the upload routes and
// short for everything else, reproducing the split the Clojure service configured on
// Jetty.
func newHTTPServer(cfg *config.Config, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.Port),
		Handler:           h,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       cfg.Timeouts.Request,
	}
}

// requestLogger records each request and its outcome, matching the Clojure req-logger's
// intent: enough to trace a call without logging bodies.
func requestLogger(log *logrus.Entry) echo.MiddlewareFunc {
	return middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogMethod:   true,
		LogURI:      true,
		LogStatus:   true,
		LogLatency:  true,
		LogError:    true,
		LogRemoteIP: true,
		HandleError: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			entry := log.WithFields(logrus.Fields{
				"method":  v.Method,
				"uri":     v.URI,
				"status":  v.Status,
				"latency": v.Latency.String(),
				"remote":  v.RemoteIP,
				// The caller's identity is a query parameter, not a header or a token.
				"user": c.QueryParam("user"),
			})
			// Errors are reported by the HTTP error handler, which has the error_code
			// and the resolved status. Repeating them here would double-log every
			// failure, so this only records that the request happened.
			entry.Info("request")
			return nil
		},
	})
}

// errorLogger reports errors the HTTP error handler renders, including any secondary
// failure that happened while answering.
func errorLogger(log *logrus.Entry) func(echo.Context, *apierror.Error, error) {
	return func(c echo.Context, apiErr *apierror.Error, err error) {
		status := apiErr.HTTPStatus()
		entry := log.WithFields(logrus.Fields{
			"method":     c.Request().Method,
			"uri":        c.Request().RequestURI,
			"error_code": string(apiErr.Code),
			"status":     status,
		}).WithError(err)

		// A 404 from a stale client or a scanner is not an operational problem. Only
		// server-side failures are worth an ERROR line.
		if status < http.StatusInternalServerError {
			entry.Warn("request error")
			return
		}
		entry.Error("request error")
	}
}
