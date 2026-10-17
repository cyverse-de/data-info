// Package handlers holds data-info's HTTP handlers.
package handlers

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/config"
	"github.com/labstack/echo/v4"
)

// ServiceName is the name this service reports and the value GET / expects.
const ServiceName = "data-info"

// ServiceDescription is reported verbatim by GET /.
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
// by GET /; irods is probed for GET / and icat for readiness.
func NewStatus(cfg *config.Config, version string, irods, icat Prober) *Status {
	return &Status{cfg: cfg, version: version, irods: irods, icat: icat}
}

// statusResponse is the body of GET /.
type statusResponse struct {
	Service     string `json:"service"`
	Description string `json:"description"`
	Version     string `json:"version"`
	DocsURL     string `json:"docs-url"`
	Expecting   string `json:"expecting"`
	IRODS       bool   `json:"iRODS"`
}

// Info handles GET /. Naming another service in ?expecting= answers 500 with the same body,
// which is how the DE detects a misrouted request.
func (s *Status) Info(c echo.Context) error {
	expecting := c.QueryParam("expecting")

	// A blank expecting= is a 400 schema failure rather than being treated as absent.
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

// docsURL always uses http:// and an explicit port, matching the Clojure service's docs-url.
func (s *Status) docsURL(c echo.Context) string {
	host, port, err := net.SplitHostPort(c.Request().Host)
	if err != nil {
		host, port = c.Request().Host, strconv.Itoa(s.cfg.Port)
	}
	return "http://" + net.JoinHostPort(host, port) + "/docs"
}

// Healthz handles GET /healthz. It touches no backend, so an iRODS outage can't make the
// liveness probe restart every pod.
func (s *Status) Healthz(c echo.Context) error {
	return writeJSONOK(c, map[string]string{"status": "ok"})
}

// Readyz handles GET /readyz, checking only the catalog. iRODS is skipped because probing it
// every 20s on every pod spends the Data Store's small connection budget; GET / still reports it.
func (s *Status) Readyz(c echo.Context) error {
	result := "ok"
	if s.icat == nil {
		result = "not configured"
	} else if err := s.icat.Probe(c.Request().Context()); err != nil {
		result = err.Error()
	}

	ready := result == "ok"
	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}
	return writeJSON(c, status, map[string]any{"ready": ready, "backends": map[string]string{"icat": result}})
}

// AdminConfig handles GET /admin/config with credentials masked. It has no authorization
// because data-info is reachable only behind terrain; see docs/deferred-fixes.md.
func (s *Status) AdminConfig(c echo.Context) error {
	return writeJSONOK(c, s.cfg.LegacyMap())
}
