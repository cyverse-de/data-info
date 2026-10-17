package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/clients/metadata"
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

	// BadChars are the characters refused in the names of new files and folders.
	BadChars string

	// ProxyUser is the service's own account, used to ask whether something exists regardless
	// of whether the caller can see it.
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

	// Metadata holds the template AVUs the DE manages, which are separate from the ones
	// iRODS stores. Several endpoints report the two merged.
	Metadata MetadataClient

	// AdminUsers are accounts whose access to a path is structural rather than shared, so
	// a permission repair leaves them alone.
	AdminUsers map[string]bool

	// AnonUser is the account anonymous access is granted to, and AnonBaseURL and
	// AnonMappings say where the service that serves it can be reached.
	AnonUser     string
	AnonBaseURL  string
	AnonMappings map[string]string

	// PathLists names the file identifiers and info types generated path lists carry.
	PathLists PathListSettings

	// DataONE describes where harvested objects are served and how the files describing
	// them are marked.
	DataONE DataONESettings

	// KifshareURL and KifshareTemplate say where a ticket can be redeemed. The template is
	// a deployment's to decide, because it addresses a service this one does not own.
	KifshareURL      string
	KifshareTemplate string

	// Log is for background work that has no caller left to report to.
	Log *logrus.Entry
}

// OpenProxyScope returns a view acting as the service's own account, so existence can be
// checked separately from what the caller can see.
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

// PathListSettings describes the two kinds of path list this service writes. Both the
// identifier and the info type are configured, because whoever reads the file back keys on
// them.
type PathListSettings struct {
	HTIdentifier string
	HTInfoType   string

	MultiInputIdentifier string
	MultiInputInfoType   string
}

// DataONESettings describe the repository the exported metadata files are written for.
type DataONESettings struct {
	// MemberNodeBase is where the member node serves objects, which the resource map's
	// identifiers are built from.
	MemberNodeBase string

	// OREAttribute marks a file as a resource map, and FormatIDAttribute records its
	// format, both of which the harvester reads.
	OREAttribute      string
	FormatIDAttribute string

	// MetadataDirname is the directory metadata files go in, and MetadataDirAttribute
	// records where that directory is on the data set itself.
	MetadataDirname      string
	MetadataDirAttribute string
}

// MetadataClient is the part of the metadata service this service uses.
type MetadataClient interface {
	ListAVUs(ctx context.Context, user, targetType, targetID string) (map[string]any, error)
	UpdateAVUs(ctx context.Context, user, targetType, targetID string, body any) error
	SetAVUs(ctx context.Context, user, targetType, targetID string, body any) error
	CopyAVUs(ctx context.Context, user, targetType, targetID string, targets []metadata.CopyTarget) error
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

// CheckPathCount rejects a bulk request carrying more paths than the service allows, which
// bounds catalog fan-out; callers recognise its ERR_TOO_MANY_RESULTS.
func (d Deps) CheckPathCount(n int) error {
	if d.MaxPathsInRequest > 0 && n > d.MaxPathsInRequest {
		return apierror.New(apierror.ErrTooManyResults).
			With("count", n).
			With("limit", d.MaxPathsInRequest)
	}
	return nil
}

// requireUser reads the user query parameter, the caller's only identity. A missing one is a
// 400 ERR_ILLEGAL_ARGUMENT schema failure on every route, not ERR_MISSING_QUERY_PARAMETER.
func requireUser(c echo.Context) (string, error) {
	user := c.QueryParam("user")
	if strings.TrimSpace(user) == "" {
		return "", schemaError("user must be a non-blank string")
	}
	return user, nil
}

// requireKnownUser rejects a caller iRODS has never heard of. plural chooses the envelope, a
// "users" list or a singular "user"; each endpoint keeps the one its callers read.
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

// schemaError reports a request that doesn't match its endpoint's declared shape: always a
// 400 ERR_ILLEGAL_ARGUMENT, whatever the route's error style.
func schemaError(reason string) error {
	return apierror.New(apierror.ErrIllegalArgument).
		AsSchemaFailure().
		WithStatus(http.StatusBadRequest).
		With("reason", reason)
}

// bindBody reports an unparseable body as a schema failure, so it is a 400 even on StyleOK
// routes.
func bindBody(c echo.Context, into any) error {
	if err := c.Bind(into); err != nil {
		return schemaError("the request body could not be parsed")
	}
	return nil
}

// decodeBody reads a request body as JSON only. Use it rather than bindBody for map
// destinations: echo's binder merges path and query params into maps, and those extra keys
// would be forwarded to the metadata service, which rejects them.
func decodeBody(c echo.Context, into any) error {
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return schemaError("the request body could not be read")
	}

	// An absent body is not a malformed one. Several of these endpoints accept a request
	// that changes nothing.
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}

	if err := json.Unmarshal(body, into); err != nil {
		return schemaError("the request body could not be parsed")
	}
	return nil
}

// anonURL builds the address the anonymous file service serves a path at.
//
// The longest configured prefix wins, so that a more specific mapping is not shadowed by a
// more general one that happens to be checked first. A path no mapping covers gets an empty
// URL rather than a wrong one.
func (d Deps) anonURL(path string) string {
	var best string
	for prefix := range d.AnonMappings {
		if len(path) > len(prefix) && strings.HasPrefix(path, prefix) && len(prefix) > len(best) {
			best = prefix
		}
	}
	if best == "" {
		return ""
	}

	mapped := d.AnonMappings[best] + path[len(best):]

	// Each segment is escaped on its own so the separators survive, and a space becomes
	// %20 rather than a plus -- this ends up in a URL a person is given, not in a form.
	segments := strings.Split(mapped, "/")
	for i, segment := range segments {
		segments[i] = strings.ReplaceAll(url.QueryEscape(segment), "+", "%20")
	}

	return strings.TrimRight(d.AnonBaseURL, "/") + "/" + strings.Join(segments, "/")
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
