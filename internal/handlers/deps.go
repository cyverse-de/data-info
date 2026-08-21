package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/irodsclient"
	"github.com/cyverse-de/data-info/internal/jobs"
	"github.com/cyverse-de/data-info/internal/locks"
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/cyverse-de/data-info/internal/worker"
	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
)

// Deps are what the handlers need to serve a request.
type Deps struct {
	ICAT  icat.Store
	IRODS *irodsclient.Pool

	// Layout locates the zone's special collections.
	Layout paths.Layout

	// InfoTypeAttribute is the AVU attribute holding a data object's info type.
	InfoTypeAttribute string

	// MaxPathsInRequest bounds a bulk request.
	MaxPathsInRequest int

	// PermsFilter names accounts left out of permission listings and share counts.
	PermsFilter map[string]bool

	// BadChars are the characters a listing flags in a name by default when the caller
	// does not supply its own set.
	BadChars string

	// ProxyUser is the account the service authenticates as. It is not the caller: it is
	// used where the reference implementation asks whether something exists at all,
	// independently of whether the caller can see it.
	ProxyUser string

	// Tasks records work that outlives the request which asked for it, and Worker runs it.
	// The lock every mutating endpoint consults is derived from what Tasks holds, so an
	// endpoint that changes a path needs both.
	Tasks   locks.Reader
	Creator jobs.TaskCreator
	Worker  *worker.Runner

	// Notifier tells a user their operation finished, and Publisher tells other services
	// that something changed.
	Notifier  jobs.Notifier
	Publisher jobs.Publisher

	// AdminUsers are accounts whose access to a path is structural rather than shared, so
	// a permission repair leaves them alone.
	AdminUsers map[string]bool

	// AnonUser is the account anonymous access is granted to, and AnonBaseURL and
	// AnonMappings say where the service that serves it can be reached.
	AnonUser     string
	AnonBaseURL  string
	AnonMappings map[string]string

	// KifshareURL and KifshareTemplate say where a ticket can be redeemed. The template is
	// a deployment's to decide, because it addresses a service this one does not own.
	KifshareURL      string
	KifshareTemplate string

	// Log is where work that outlives a request reports itself. A request's own failures
	// travel back to the caller and are logged by the error handler; this is for the
	// background cleanup that has no caller left to tell.
	Log *logrus.Entry
}

// OpenProxyScope returns a view acting as the service's own account.
//
// Existence and visibility are separate questions, and the reference implementation asks
// them separately: it checks that a path exists using its own account, then quietly drops
// the ones the caller cannot see. Asking both as the caller would turn "this is not shared
// with you" into "this does not exist", which fails the request instead of omitting a row.
func (d Deps) OpenProxyScope(ctx context.Context) (*rods.Scope, error) {
	return d.OpenScope(ctx, d.ProxyUser)
}

// OpenScope returns a request-scoped view acting as user.
func (d Deps) OpenScope(ctx context.Context, user string) (*rods.Scope, error) {
	scope, err := rods.Open(ctx, rods.Deps{
		ICAT:              d.ICAT,
		IRODS:             d.IRODS,
		Zone:              d.Layout.Zone,
		InfoTypeAttribute: d.InfoTypeAttribute,
	}, rods.Options{User: user})
	if err != nil {
		return nil, fmt.Errorf("opening a data store view for %q: %w", user, err)
	}
	return scope, nil
}

// Logger returns where background work should report itself, never nil.
//
// Tests that only exercise request handling leave it unset, and a cleanup goroutine must
// not be the thing that panics because nobody was listening.
func (d Deps) Logger() *logrus.Entry {
	if d.Log != nil {
		return d.Log
	}
	discard := logrus.New()
	discard.SetOutput(io.Discard)
	return logrus.NewEntry(discard)
}

// RequireUnlocked rejects paths that unfinished work already holds.
//
// Every endpoint that changes a path calls this first. It answers nothing when no task
// client is configured, which is how the read-only test wiring stays usable -- a deployment
// always has one, because startup validates the URL.
func (d Deps) RequireUnlocked(ctx context.Context, paths ...string) error {
	if d.Tasks == nil {
		return nil
	}
	return locks.Validate(ctx, d.Tasks, paths)
}

// CheckPathCount rejects a bulk request carrying more paths than the service allows.
//
// The limit exists because these endpoints fan out into catalog queries, and the error code
// is the one the Clojure service used so callers recognise it.
func (d Deps) CheckPathCount(n int) error {
	if d.MaxPathsInRequest > 0 && n > d.MaxPathsInRequest {
		return apierror.New(apierror.ErrTooManyResults).
			With("count", n).
			With("limit", d.MaxPathsInRequest)
	}
	return nil
}

// requireUser reads the user query parameter, which is the only caller identity the service
// has.
//
// A missing one is a schema failure rather than a handler failure: in the Clojure service
// every endpoint declares user as a required non-blank parameter, so compojure-api rejects
// the request before the handler is reached, with ERR_ILLEGAL_ARGUMENT and a 400. Reporting
// ERR_MISSING_QUERY_PARAMETER here would be more descriptive and would not match.
func requireUser(c echo.Context) (string, error) {
	user := c.QueryParam("user")
	if strings.TrimSpace(user) == "" {
		return "", schemaError("user must be a non-blank string")
	}
	return user, nil
}

// requireKnownUser rejects a caller iRODS has never heard of.
//
// Every endpoint validates this before doing anything else, and the envelope differs by
// endpoint: the handlers written against clj-irods report a plural users list, and those
// written against the jargon validators report a singular user. Both are reproduced because
// callers read the key.
func requireKnownUser(ctx context.Context, scope *rods.Scope, user string, plural bool) error {
	known, err := scope.UserExists(ctx, user).Get(ctx)
	if err != nil {
		return err
	}
	if known {
		return nil
	}

	if plural {
		return apierror.New(apierror.ErrNotAUser).With("users", []string{user})
	}
	return apierror.New(apierror.ErrNotAUser).With("user", user)
}

// schemaError reports a request that fails the shape its endpoint declares.
//
// The Clojure stack answers these from compojure-api's request-validation handler, which
// always uses ERR_ILLEGAL_ARGUMENT and a 400 whatever the endpoint's own error style says.
// The reason differs -- theirs renders prismatic/schema's internal explanation -- and that
// text is diagnostic rather than contract.
func schemaError(reason string) error {
	return apierror.New(apierror.ErrIllegalArgument).
		WithStatus(http.StatusBadRequest).
		With("reason", reason)
}

// bindBody decodes a request body, reporting a malformed one the way the reference stack
// does.
//
// A body that will not parse is a schema failure, so it answers ERR_ILLEGAL_ARGUMENT with a
// 400 on every route whatever its error style. Reporting a code of our own would answer 500
// on the routes marked StyleOK, which is most of the bulk endpoints.
func bindBody(c echo.Context, into any) error {
	if err := c.Bind(into); err != nil {
		return schemaError("the request body could not be parsed")
	}
	return nil
}

// visiblePermissions drops the entries the service does not report: the requesting user's
// own, and the service and administrative accounts a deployment filters out. Reporting
// those would expose the proxy account's access on every path.
func (d Deps) visiblePermissions(acl []rods.ACLEntry, user string) []userPermission {
	out := make([]userPermission, 0, len(acl))
	for _, entry := range acl {
		if entry.User == user || d.PermsFilter[entry.User] {
			continue
		}
		if entry.Permission == rods.PermissionNone {
			continue
		}
		out = append(out, userPermission{User: entry.User, Permission: string(entry.Permission)})
	}
	return out
}
