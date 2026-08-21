package main

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/cyverse-de/data-info/internal/amqp"
	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/clients/asynctasks"
	"github.com/cyverse-de/data-info/internal/clients/notifications"
	"github.com/cyverse-de/data-info/internal/config"
	"github.com/cyverse-de/data-info/internal/handlers"
	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/irodsclient"
	dimw "github.com/cyverse-de/data-info/internal/middleware"
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/cyverse-de/data-info/internal/worker"
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
	IRODSProbe handlers.Prober
	ICATProbe  handlers.Prober

	// IRODS and ICAT are the clients the data endpoints read through. They are nil in
	// tests that only exercise the status endpoints.
	IRODS *irodsclient.Pool
	ICAT  icat.Store

	// Tasks and Worker record long-running work and run it, and Notifier and Publisher
	// report it. All are nil in tests that do not exercise a mutating endpoint.
	Tasks     *asynctasks.Client
	Worker    *worker.Runner
	Notifier  *notifications.Client
	Publisher *amqp.Publisher
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

	status := handlers.NewStatus(cfg, version, deps.IRODSProbe, deps.ICATProbe)

	e.GET("/", status.Info)
	e.GET("/healthz", status.Healthz)
	e.GET("/readyz", status.Readyz)
	e.GET("/admin/config", status.AdminConfig)

	registerDataRoutes(e, cfg, log, deps)

	return e
}

// registerDataRoutes mounts the data endpoints.
//
// The error style on each route is not decoration. The Clojure service wrote its handlers
// two ways, and they answer differently for the same error_code: a route wrapped in svc/trap
// uses the status table, while one returning (ok ...) lets the thrown map reach the default
// exception handler, which answers 500 whatever the code. So ERR_NOT_OWNER is 403 on
// /permissions-gatherer and 500 on /path-info. Marking the routes wrongly would change
// statuses that the port is meant to preserve exactly.
func registerDataRoutes(e *echo.Echo, cfg *config.Config, log *logrus.Entry, deps Deps) {
	if deps.ICAT == nil || deps.IRODS == nil {
		// Nothing to serve them with. The status endpoints still work, which is what a
		// readiness probe needs in order to report why.
		return
	}

	hd := handlers.Deps{
		ICAT:              deps.ICAT,
		IRODS:             deps.IRODS,
		Layout:            layoutOf(cfg),
		InfoTypeAttribute: cfg.TypeDetect.TypeAttribute,
		MaxPathsInRequest: cfg.MaxPathsInRequest,
		PermsFilter:       permsFilterOf(cfg),
		ProxyUser:         cfg.IRODS.User,
		BadChars:          cfg.BadChars,
		Log:               log,
		Worker:            deps.Worker,
		AdminUsers:        adminUsersOf(cfg),
		AnonUser:          cfg.AnonUser,
		AnonBaseURL:       cfg.AnonFiles.BaseURL,
		AnonMappings:      cfg.AnonFiles.Mappings,
		KifshareURL:       cfg.Kifshare.ExternalURL,
		KifshareTemplate:  cfg.Kifshare.DownloadTemplate,
	}
	// Assigned only when present. These fields are interfaces, and a nil concrete pointer
	// stored in one is not itself nil, so a guard on the field would never fire.
	if deps.Tasks != nil {
		hd.Tasks = deps.Tasks
		hd.Creator = deps.Tasks
	}
	if deps.Notifier != nil {
		hd.Notifier = deps.Notifier
	}
	if deps.Publisher != nil {
		hd.Publisher = deps.Publisher
	}

	stats := handlers.NewStats(hd)
	reads := handlers.NewReads(hd)
	listings := handlers.NewListings(hd)
	writes := handlers.NewWrites(hd)

	// (ok ...) routes in the Clojure service: every error_code answers 500.
	ok := apierror.WithStyle(apierror.StyleOK)
	e.POST("/stat-gatherer", stats.GatherPlain, ok)
	e.POST("/path-info", stats.Gather, ok)
	e.POST("/existence-marker", reads.Existence, ok)

	// svc/trap routes: the status table applies.
	e.POST("/permissions-gatherer", reads.Permissions)
	e.POST("/data/directories", writes.CreateDirectories)
	e.GET("/users/:username/groups", reads.UserGroups)
	e.GET("/navigation/base-paths", reads.BasePaths)
	e.GET("/data/uuid", listings.UUIDForPath)
	e.HEAD("/data/:data-id", listings.Head)

	// The wildcard routes carry an iRODS path, which may contain characters echo would
	// otherwise treat as structure.
	e.GET("/navigation/path/:zone/*", listings.Navigation, ok)

	// Trap-style, unlike its neighbour above: the data routes are wrapped in svc/trap in
	// the reference, so their codes map through the status table rather than all
	// answering 500. Verified against the running service, which answers a missing limit
	// with a 400.
	e.GET("/data/path/:zone/*", listings.FolderListing)

	// The upload routes are trap-wrapped in the reference, but every error they can raise
	// comes from the multipart middleware that stores the file, which sits outside the
	// trap -- so those errors reach the default handler and answer 500 whatever their code.
	// Verified against the running service, which answers a forbidden filename with a 500
	// where the status table says 400.
	//
	// Both spellings of the create route, because the Clojure route is a "/" inside a /data
	// context and compojure matches it with and without the trailing slash.
	e.POST("/data", writes.Upload, ok)
	e.POST("/data/", writes.Upload, ok)
	e.PUT("/data/:data-id", writes.Overwrite, ok)

	// Moves and renames. Each records a task before doing anything, which is what locks the
	// paths it will touch.
	e.POST("/mover", writes.Move)
	e.PUT("/data/:data-id/name", writes.RenameByID)
	e.PUT("/data/:data-id/dir", writes.MoveByID)
	e.PUT("/data/:data-id/children/dir", writes.MoveChildrenByID)

	// Trash and restore. A delete moves to the trash unless the thing is already there, in
	// which case there is nowhere further to move it.
	e.POST("/deleter", writes.Delete)
	e.DELETE("/data/:data-id", writes.DeleteByID)
	e.DELETE("/data/:data-id/children", writes.DeleteChildrenByID)
	e.DELETE("/trash", writes.EmptyTrash)
	e.POST("/restorer", writes.Restore)

	// Sharing. These report a failure per path inside a successful response, because the DE
	// runs them over a selection and one item nobody owns must not fail the rest.
	e.POST("/sharer", writes.Share)
	e.POST("/unsharer", writes.Unshare)
	e.POST("/anonymizer", writes.Anonymize)
	e.PUT("/data/:data-id/permissions/:share-with/:permission", writes.AddPermission)
	e.DELETE("/data/:data-id/permissions/:unshare-with", writes.RemovePermission)

	// Tickets. These are (ok ...) routes in the reference, so every error code answers 500.
	tickets := handlers.NewTickets(hd)
	e.POST("/tickets", tickets.Add, ok)
	e.POST("/ticket-lister", tickets.List, ok)
	e.POST("/ticket-deleter", tickets.Delete, ok)

	// Groups. These are (ok ...) routes too, except that the forbidden response pins its own
	// status because the route documents a 403 explicitly.
	groups := handlers.NewGroups(hd)
	e.POST("/groups", groups.Create, ok)
	e.GET("/groups/:group-name", groups.Get, ok)
	e.PUT("/groups/:group-name", groups.Update, ok)
	e.DELETE("/groups/:group-name", groups.Delete, ok)
}

// adminUsersOf names the accounts whose access to a path is structural.
//
// A permission repair after a move leaves these alone: their access is how the DE operates,
// not something a user shared.
func adminUsersOf(cfg *config.Config) map[string]bool {
	out := make(map[string]bool, len(cfg.IRODS.AdminUsers))
	for _, user := range cfg.IRODS.AdminUsers {
		out[user] = true
	}
	return out
}

// layoutOf describes the zone's namespace from the service configuration.
func layoutOf(cfg *config.Config) paths.Layout {
	return paths.Layout{
		Zone:          cfg.IRODS.Zone,
		Home:          cfg.IRODS.Home,
		CommunityData: cfg.CommunityData,
	}
}

// permsFilterOf builds the set of accounts left out of permission listings and share counts.
//
// The service's own proxy account is always included: it holds access on everything, so
// reporting it would tell every user that every path is shared with an account they have
// never heard of.
func permsFilterOf(cfg *config.Config) map[string]bool {
	filter := make(map[string]bool, len(cfg.PermsFilter)+1)
	for _, name := range cfg.PermsFilter {
		filter[name] = true
	}
	filter[cfg.IRODS.User] = true
	return filter
}

// ProbeCacheTTL is how long a backend health result is reused, matching what the iRODS
// pool does internally.
const ProbeCacheTTL = 5 * time.Second

// ProbeTimeout bounds a backend check. GET / probes iRODS on every call, so an
// unreachable backend has to fail fast rather than hold the request open; DNS for a
// nonexistent host can otherwise take far longer than the dial itself.
const ProbeTimeout = 3 * time.Second

// networkDeps builds the probes used in a running service.
//
// iRODS is probed through the client, which exercises authentication as well as
// reachability -- the same ground the Clojure irods-running? check covered by opening a
// Jargon connection. ICAT is still a bare TCP dial until the catalog client lands.
func networkDeps(pool *irodsclient.Pool, store icat.Store) Deps {
	return Deps{
		IRODS: pool,
		ICAT:  store,
		IRODSProbe: handlers.ProberFunc(func(ctx context.Context) error {
			// The pool caches this internally and runs it on its own budget.
			return pool.Probe(ctx)
		}),
		ICATProbe: cachedProber(ProbeCacheTTL, func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
			defer cancel()
			return store.Ping(ctx)
		}),
	}
}

// cachedProber reuses a probe result for a while.
//
// GET / reports backend health on every call and is hit by both k8s probes on every pod,
// plus anything else watching the service. An uncached catalog probe would take a
// connection from the same modest pool the bulk queries depend on, every time -- so under
// the load where readiness matters most, the probe would queue behind real work, exceed its
// timeout, and flap. The iRODS probe is cached for the same reason.
func cachedProber(ttl time.Duration, probe func(context.Context) error) handlers.Prober {
	var (
		mu     sync.Mutex
		last   time.Time
		result error
	)

	return handlers.ProberFunc(func(ctx context.Context) error {
		mu.Lock()
		defer mu.Unlock()

		if !last.IsZero() && time.Since(last) < ttl {
			return result
		}

		result = probe(ctx)
		last = time.Now()
		return result
	})
}

// amqpConfig describes the broker and exchange from the service configuration.
func amqpConfig(cfg *config.Config) amqp.Config {
	return amqp.Config{
		URI:        cfg.AMQP.URI,
		Exchange:   cfg.AMQP.Exchange.Name,
		Durable:    cfg.AMQP.Exchange.Durable,
		AutoDelete: cfg.AMQP.Exchange.AutoDelete,
	}
}

// icatConfig derives the catalog connection from the service configuration.
func icatConfig(cfg *config.Config) icat.Config {
	return icat.Config{URI: cfg.ICATConnectionString()}
}

// irodsPoolConfig derives the pool's settings from the service configuration.
func irodsPoolConfig(cfg *config.Config) irodsclient.Config {
	return irodsclient.Config{
		Host:          cfg.IRODS.Host,
		Port:          cfg.IRODS.Port,
		Zone:          cfg.IRODS.Zone,
		ProxyUser:     cfg.IRODS.User,
		ProxyPassword: cfg.IRODS.Password,
		Resource:      cfg.IRODS.Resource,
		AppName:       handlers.ServiceName,

		MaxSessions:          cfg.IRODS.MaxSessions,
		MaxConnections:       cfg.IRODS.MaxConnections,
		IdleTimeout:          cfg.IRODS.SessionIdleTimeout,
		OperationTimeout:     cfg.IRODS.OperationTimeout,
		LongOperationTimeout: cfg.IRODS.LongOperationTimeout,
		MaxRetries:           cfg.IRODS.MaxRetries,
		RetrySleep:           cfg.IRODS.RetrySleep,
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
