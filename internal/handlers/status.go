// Package handlers holds data-info's HTTP handlers, one file per Clojure routes/*
// namespace.
package handlers

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/config"
	"github.com/labstack/echo/v4"
)

// ServiceName is the name this service reports and the value GET / expects.
const ServiceName = "data-info"

// ServiceDescription is reported verbatim by GET /; it matches data-info.util.config/svc-info.
const ServiceDescription = "DE service for data information logic and iRODS interactions."

// Prober reports whether a backend is reachable. Implementations live with their client.
type Prober interface {
	// Probe returns nil when the backend is usable.
	Probe(ctx context.Context) error
}

// ProberFunc adapts a function to Prober.
type ProberFunc func(context.Context) error

// Probe implements Prober.
func (f ProberFunc) Probe(ctx context.Context) error { return f(ctx) }

// Status serves the service-information endpoints.
type Status struct {
	cfg     *config.Config
	version string
	irods   Prober
	icat    Prober
}

// NewStatus builds the service-information handlers. version is the build version reported
// by GET /; irods and icat are probed for readiness.
func NewStatus(cfg *config.Config, version string, irods, icat Prober) *Status {
	return &Status{cfg: cfg, version: version, irods: irods, icat: icat}
}

// statusResponse mirrors clojure-commons' get-docs-status plus data-info's iRODS flag.
// Field order is the order the Clojure service serialized them in.
type statusResponse struct {
	Service     string `json:"service"`
	Description string `json:"description"`
	Version     string `json:"version"`
	DocsURL     string `json:"docs-url"`
	Expecting   string `json:"expecting"`
	IRODS       bool   `json:"iRODS"`
}

// Info handles GET /.
//
// It answers 500 when the caller names a different service in ?expecting=, which is how
// the DE detects a misrouted request; the body is the same either way.
func (s *Status) Info(c echo.Context) error {
	expecting := c.QueryParam("expecting")

	// The Clojure route types expecting as an optional NonBlankString, so supplying the
	// parameter with a blank value fails schema coercion rather than being treated as
	// absent. compojure-api reports that as ERR_ILLEGAL_ARGUMENT with a 400.
	if _, present := c.QueryParams()["expecting"]; present && strings.TrimSpace(expecting) == "" {
		return apierror.New(apierror.ErrIllegalArgument).
			WithStatus(http.StatusBadRequest).
			With("reason", "expecting must be a non-blank string")
	}

	body := statusResponse{
		Service:     ServiceName,
		Description: ServiceDescription,
		Version:     s.version,
		DocsURL:     s.docsURL(c),
		Expecting:   expecting,
		IRODS:       s.irods != nil && s.irods.Probe(c.Request().Context()) == nil,
	}

	status := http.StatusOK
	if expecting != "" && expecting != ServiceName {
		status = http.StatusInternalServerError
	}
	return writeJSON(c, status, body)
}

// docsURL reproduces clojure-commons' get-docs-status, which always builds an http:// URL
// and always includes the port.
func (s *Status) docsURL(c echo.Context) string {
	host, port, err := net.SplitHostPort(c.Request().Host)
	if err != nil {
		host, port = c.Request().Host, strconv.Itoa(s.cfg.Port)
	}
	return "http://" + net.JoinHostPort(host, port) + "/docs"
}

// Healthz handles GET /healthz: the process is up and serving. It touches no backend, so
// it is safe as a liveness and startup probe -- an iRODS outage must not restart every
// pod. This endpoint is new; the Clojure service had only GET /.
func (s *Status) Healthz(c echo.Context) error {
	return writeJSONOK(c, map[string]string{"status": "ok"})
}

// Readyz handles GET /readyz: the service can actually serve requests, which means both
// backends are reachable. This is the readiness probe target.
func (s *Status) Readyz(c echo.Context) error {
	ctx := c.Request().Context()

	backends := map[string]Prober{"irods": s.irods, "icat": s.icat}
	results := make(map[string]string, len(backends))
	ready := true

	for name, p := range backends {
		if p == nil {
			results[name] = "not configured"
			ready = false
			continue
		}
		if err := p.Probe(ctx); err != nil {
			results[name] = err.Error()
			ready = false
			continue
		}
		results[name] = "ok"
	}

	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}
	return writeJSON(c, status, map[string]any{"ready": ready, "backends": results})
}

// AdminConfig handles GET /admin/config, reporting the running configuration with
// credentials masked.
//
// Like the Clojure service, this endpoint has no authorization of its own. data-info sits
// on a private network behind terrain and is not internet-facing. Adding a gate here would
// require a coordinated change in every caller, so it is deliberately out of scope for the
// port; see docs/deferred-fixes.md.
func (s *Status) AdminConfig(c echo.Context) error {
	return writeJSONOK(c, s.cfg.LegacyMap())
}

// TCPProber reports whether a host:port accepts connections. It is a reachability check,
// not a protocol check: it cannot tell a healthy iRODS from one that would reject our
// credentials. The iRODS client replaces it with a real session probe.
func TCPProber(host string, port int, timeout time.Duration) Prober {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	return ProberFunc(func(ctx context.Context) error {
		// Bound the whole attempt, not just the dial: name resolution for an unreachable
		// host can outlast the dialer's own timeout.
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		d := net.Dialer{Timeout: timeout}
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return err
		}
		return conn.Close()
	})
}
