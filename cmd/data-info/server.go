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
	"github.com/cyverse-de/data-info/internal/clients/metadata"
	"github.com/cyverse-de/data-info/internal/clients/notifications"
	"github.com/cyverse-de/data-info/internal/config"
	"github.com/cyverse-de/data-info/internal/handlers"
	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/irodsclient"
	dimw "github.com/cyverse-de/data-info/internal/middleware"
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/cyverse-de/data-info/internal/worker"
	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
)

// uploadRoutes are the routes that upload files to the data store. They get a longer idle
// timeout than other routes.
var uploadRoutes = map[string]bool{
	"POST /data":         true,
	"POST /data/":        true,
	"PUT /data/:data-id": true,
}

// isUploadRoute reports whether the matched route streams a whole file.
func isUploadRoute(c echo.Context) bool {
	return uploadRoutes[c.Request().Method+" "+c.Path()]
}

// Deps are the server's backends, injected so tests can exercise the real router without a
// network.
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
	Metadata  *metadata.Client
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

	// The logger wraps Recover so that a request that panicked still gets its line.
	e.Use(requestLogger(log))
	e.Use(recoverer(log))
	e.Use(dimw.IdleTimeout(cfg.Timeouts.Request, cfg.Timeouts.Upload, isUploadRoute))

	status := handlers.NewStatus(cfg, version, deps.IRODSProbe, deps.ICATProbe)

	e.GET("/", status.Info)
	e.GET("/healthz", status.Healthz)
	e.GET("/readyz", status.Readyz)
	e.GET("/admin/config", status.AdminConfig)

	registerDataRoutes(e, cfg, log, deps)

	return e
}

// registerDataRoutes mounts the data endpoints. Routes given ok are StyleOK, so a route's style
// changes its statuses: ERR_NOT_OWNER is 403 on /permissions-gatherer but 500 on /path-info.
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
		DataONE: handlers.DataONESettings{
			MemberNodeBase:       cfg.DataONE.MemberNodeBase,
			OREAttribute:         cfg.DataONE.OREAttribute,
			FormatIDAttribute:    cfg.DataONE.FormatIDAttribute,
			MetadataDirname:      cfg.DataONE.MetadataDirname,
			MetadataDirAttribute: cfg.DataONE.MetadataDirpathAttribute,
		},
		PathLists: handlers.PathListSettings{
			HTIdentifier:         cfg.PathLists.HT.FileIdentifier,
			HTInfoType:           cfg.PathLists.HT.InfoType,
			MultiInputIdentifier: cfg.PathLists.MultiInput.FileIdentifier,
			MultiInputInfoType:   cfg.PathLists.MultiInput.InfoType,
		},
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
	if deps.Metadata != nil {
		hd.Metadata = deps.Metadata
	}

	stats := handlers.NewStats(hd)
	reads := handlers.NewReads(hd)
	listings := handlers.NewListings(hd)
	writes := handlers.NewWrites(hd)

	ok := apierror.WithStyle(apierror.StyleOK)
	e.POST("/stat-gatherer", stats.GatherPlain, ok)
	e.POST("/path-info", stats.Gather, ok)
	e.POST("/stat-lister", stats.Listing, ok)
	e.POST("/existence-marker", reads.Existence, ok)
	e.POST("/creatability-marker", reads.Creatability, ok)
	e.GET("/navigation/root", listings.Root, ok)

	e.POST("/permissions-gatherer", reads.Permissions)
	e.POST("/data/directories", writes.CreateDirectories)
	e.GET("/users/:username/groups", reads.UserGroups)
	e.GET("/navigation/base-paths", reads.BasePaths)
	e.GET("/navigation/home", listings.Home)
	e.GET("/data/:data-id/permissions", reads.PermissionsByID)
	e.GET("/data/uuid", listings.UUIDForPath)
	e.HEAD("/data/:data-id", listings.Head)

	// The wildcard routes carry an iRODS path, which may contain characters echo would
	// otherwise treat as structure.
	e.GET("/navigation/path/:zone/*", listings.Navigation, ok)

	// The manifest and the two chunk readers, which together back file preview in the DE.
	// Each has a by-id route and a by-path one, and the by-path form carries the whole path
	// -- zone included -- in its wildcard.
	chunks := handlers.NewChunks(hd)
	e.GET("/data/:data-id/manifest", chunks.Manifest)
	e.GET("/data/:data-id/chunks", chunks.Chunk)
	e.GET("/data/:data-id/chunks-tabular", chunks.TabularChunk)
	e.GET("/data/by-path/manifest/*", chunks.ManifestByPath)
	e.GET("/data/by-path/chunks/*", chunks.ChunkByPath)
	e.GET("/data/by-path/chunks-tabular/*", chunks.TabularChunkByPath)

	// StyleTrap, unlike /navigation/path/:zone/*: a missing limit answers 400.
	e.GET("/data/path/:zone/*", listings.FolderListing)

	// Both spellings of the create route are part of the API.
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

	tickets := handlers.NewTickets(hd)
	e.POST("/tickets", tickets.Add, ok)
	e.POST("/ticket-lister", tickets.List, ok)
	e.POST("/ticket-deleter", tickets.Delete, ok)

	groups := handlers.NewGroups(hd)
	e.POST("/groups", groups.Create, ok)
	e.GET("/groups/:group-name", groups.Get, ok)
	e.PUT("/groups/:group-name", groups.Update, ok)
	e.DELETE("/groups/:group-name", groups.Delete, ok)

	// Metadata. These merge what the metadata service holds with what iRODS does, and the
	// administrative pair acts as the service's own account so that it sees and may write
	// the DE's own AVUs.
	avus := handlers.NewAVUs(hd)
	e.GET("/data/:data-id/metadata", avus.Get)
	e.PATCH("/data/:data-id/metadata", avus.Add)
	e.PUT("/data/:data-id/metadata", avus.Set)
	e.POST("/data/:data-id/metadata/copy", avus.Copy)
	e.GET("/admin/data/:data-id/metadata", avus.AdminGet)
	e.PATCH("/admin/data/:data-id/metadata", avus.AdminAdd)

	// Path lists: a file listing what a selection contains, which an analysis then runs
	// over.
	e.POST("/path-list-creator", handlers.NewPathLists(hd).Create)

	// The two endpoints that write metadata files into the data store. The ORE pair leaves
	// the DE entirely: DataONE harvests it.
	e.POST("/data/:data-id/metadata/save", avus.Save)
	e.POST("/data/:data-id/metadata/csv-parser", avus.ParseCSV)
	e.POST("/data/:data-id/ore/save", avus.SaveORE)
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

// ProbeTimeout bounds the ICAT ping behind GET /, so an unreachable catalog fails fast rather
// than holding the request open; DNS for a nonexistent host can take far longer than the dial.
const ProbeTimeout = 3 * time.Second

// networkDeps builds the production probes. iRODS is probed through the pool so
// authentication is exercised too.
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

// cachedProber reuses a probe result for ttl. Both k8s probes hit GET / on every pod, and an
// uncached catalog probe would compete with bulk queries for pool connections and flap under
// load.
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
func irodsPoolConfig(cfg *config.Config, log *logrus.Entry) irodsclient.Config {
	return irodsclient.Config{
		Host:          cfg.IRODS.Host,
		Port:          cfg.IRODS.Port,
		Zone:          cfg.IRODS.Zone,
		ProxyUser:     cfg.IRODS.User,
		ProxyPassword: cfg.IRODS.Password,
		Resource:      cfg.IRODS.Resource,
		AppName:       handlers.ServiceName,

		MaxSessions:          cfg.IRODS.MaxSessions,
		MaxTotalConnections:  cfg.IRODS.MaxTotalConnections,
		MaxConnections:       cfg.IRODS.MaxConnections,
		IdleTimeout:          cfg.IRODS.SessionIdleTimeout,
		OperationTimeout:     cfg.IRODS.OperationTimeout,
		LongOperationTimeout: cfg.IRODS.LongOperationTimeout,
		MaxRetries:           cfg.IRODS.MaxRetries,
		RetrySleep:           cfg.IRODS.RetrySleep,
		Log:                  log,
	}
}

// newHTTPServer leaves ReadTimeout and WriteTimeout unset because multi-gigabyte transfers
// outlive any whole-request deadline. dimw.IdleTimeout applies per-route idle limits
// instead.
func newHTTPServer(cfg *config.Config, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.Port),
		Handler:           h,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       cfg.Timeouts.Request,
	}
}
