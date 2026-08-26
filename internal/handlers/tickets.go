package handlers

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// Ticket access modes. Read is the default, which is what the schema documents.
const (
	ticketModeRead  = "read"
	ticketModeWrite = "write"
)

// ticketsRequest is the body of POST /ticket-deleter.
type ticketsRequest struct {
	Tickets []string `json:"tickets"`
}

// ticketView is one ticket as the API reports it.
type ticketView struct {
	Path            string `json:"path"`
	TicketID        string `json:"ticket-id"`
	DownloadURL     string `json:"download-url"`
	DownloadPageURL string `json:"download-page-url"`
}

// Tickets serves the ticket endpoints.
type Tickets struct{ deps Deps }

// NewTickets builds the ticket handlers.
func NewTickets(deps Deps) *Tickets { return &Tickets{deps: deps} }

// Add handles POST /tickets.
//
// A ticket is a bearer token: anyone holding it gets the access it grants, without an account.
// That is why creating one requires more than read on the path -- and why a ticket created
// for an analysis, which only ever reads, is allowed on a path the caller can merely read.
func (t *Tickets) Add(c echo.Context) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return err
	}

	mode, err := ticketMode(c)
	if err != nil {
		return err
	}
	forJob, err := optionalBool(c, "for-job")
	if err != nil {
		return err
	}
	public, err := optionalBool(c, "public")
	if err != nil {
		return err
	}

	limits, err := ticketLimits(c)
	if err != nil {
		return err
	}

	var body pathsRequest
	if err := bindBody(c, &body); err != nil {
		return err
	}
	if err := t.deps.CheckPathCount(len(body.Paths)); err != nil {
		return err
	}

	requested := trimAll(body.Paths)

	scope, err := t.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	if err := requireKnownUser(ctx, scope, user, false); err != nil {
		return err
	}
	if err := requireAllExist(ctx, scope, requested); err != nil {
		return err
	}

	// A read-only ticket for an analysis needs only read: the DE creates those on inputs
	// the user was given but does not own. Everything else can be written through, so it
	// needs write.
	if forJob && mode == ticketModeRead {
		if err := requireAllReadable(ctx, scope, requested); err != nil {
			return err
		}
	} else if err := requireAllWriteable(ctx, scope, user, requested); err != nil {
		return err
	}

	// The tickets themselves are issued by the service's own account, not by the caller.
	// iRODS scopes a ticket listing to whoever holds the connection, and these endpoints
	// decide who may see a ticket from the permissions on its path -- so a ticket owned by
	// one user would be invisible to another who can write to the same path, and invisible to
	// the deletion cleanup, which also runs as the service. The Clojure service does the
	// same.
	proxy, err := t.deps.OpenProxyScope(ctx)
	if err != nil {
		return err
	}
	defer proxy.Close()

	views := make([]ticketView, 0, len(requested))
	for _, path := range requested {
		view, err := t.create(ctx, proxy, path, mode, public, limits)
		if err != nil {
			return err
		}
		views = append(views, view)
	}

	return writeJSONOK(c, map[string]any{"user": user, "tickets": views})
}

// create issues one ticket and applies what was asked for.
func (t *Tickets) create(
	ctx context.Context,
	scope *rods.Scope,
	path, mode string,
	public bool,
	limits rods.TicketLimits,
) (ticketView, error) {
	// Upper case, as the Clojure service generates them. The value ends up in a URL people
	// paste around, and the two services must agree on the shape.
	name := strings.ToUpper(uuid.NewString())

	if err := scope.CreateTicket(ctx, name, mode, path); err != nil {
		return ticketView{}, err
	}

	// Everything below happens after the ticket exists, so a failure leaves one that works
	// but grants more than was asked for. Removing it is the only honest outcome: reporting
	// success would hand back a token with the wrong limits.
	if err := scope.SetTicketLimits(ctx, name, limits); err != nil {
		t.removeAfterFailure(ctx, scope, name)
		return ticketView{}, err
	}
	if public {
		if err := scope.PublicizeTicket(ctx, name); err != nil {
			t.removeAfterFailure(ctx, scope, name)
			return ticketView{}, err
		}
	}

	return t.viewOf(name, path), nil
}

// removeAfterFailure takes back a ticket that could not be finished.
func (t *Tickets) removeAfterFailure(ctx context.Context, scope *rods.Scope, name string) {
	if err := scope.DeleteTicket(ctx, name); err != nil {
		t.deps.Logger().WithError(err).WithField("ticket", name).
			Error("could not remove a ticket that was created but not fully configured; " +
				"it grants more than was asked for and needs removing by hand")
	}
}

// List handles POST /ticket-lister.
func (t *Tickets) List(c echo.Context) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return err
	}

	var body pathsRequest
	if err := bindBody(c, &body); err != nil {
		return err
	}
	if err := t.deps.CheckPathCount(len(body.Paths)); err != nil {
		return err
	}

	requested := trimAll(body.Paths)

	scope, err := t.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	if err := requireKnownUser(ctx, scope, user, false); err != nil {
		return err
	}
	if err := requireAllExist(ctx, scope, requested); err != nil {
		return err
	}
	if err := requireAllReadable(ctx, scope, requested); err != nil {
		return err
	}

	// Listed as the service, for the same reason they are issued as it: which tickets a
	// caller may see is decided by the permissions on the path, not by who happens to own
	// the ticket.
	proxy, err := t.deps.OpenProxyScope(ctx)
	if err != nil {
		return err
	}
	defer proxy.Close()

	out := make(map[string][]ticketView, len(requested))
	for _, path := range requested {
		tickets, err := proxy.TicketsForPath(ctx, path)
		if err != nil {
			return err
		}

		views := make([]ticketView, 0, len(tickets))
		for _, ticket := range tickets {
			views = append(views, t.viewOf(ticket.Name, ticket.Path))
		}
		out[path] = views
	}

	return writeJSONOK(c, map[string]any{"tickets": out})
}

// Delete handles POST /ticket-deleter.
func (t *Tickets) Delete(c echo.Context) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return err
	}
	forJob, err := optionalBool(c, "for-job")
	if err != nil {
		return err
	}

	var body ticketsRequest
	if err := bindBody(c, &body); err != nil {
		return err
	}

	scope, err := t.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	if err := requireKnownUser(ctx, scope, user, false); err != nil {
		return err
	}

	// Which paths the tickets are on decides whether they may be deleted, so they are
	// resolved first and a ticket that names nothing fails the request.
	proxy, err := t.deps.OpenProxyScope(ctx)
	if err != nil {
		return err
	}
	defer proxy.Close()

	var covered, missing []string
	for _, name := range body.Tickets {
		ticket, err := proxy.GetTicket(ctx, name)
		if err != nil {
			// Not folded into the missing list. A connection failure reported as "this
			// ticket does not exist" would tell a caller retrying through a blip that
			// their tickets were already gone.
			return err
		}
		if ticket == nil {
			missing = append(missing, name)
			continue
		}
		covered = append(covered, ticket.Path)
	}
	if len(missing) > 0 {
		// The plural key, and every missing ticket rather than the first: that is what the
		// validator this route uses attaches.
		return apierror.New(apierror.ErrTicketDoesNotExist).With("ticket-ids", missing)
	}

	if forJob {
		if err := requireAllReadable(ctx, scope, covered); err != nil {
			return err
		}
	} else if err := requireAllWriteable(ctx, scope, user, covered); err != nil {
		return err
	}

	for _, name := range body.Tickets {
		if err := proxy.DeleteTicket(ctx, name); err != nil {
			return err
		}
	}

	return writeJSONOK(c, map[string]any{"user": user, "tickets": body.Tickets})
}

// viewOf renders a ticket the way the API reports it.
func (t *Tickets) viewOf(name, path string) ticketView {
	base := strings.TrimRight(t.deps.KifshareURL, "/")

	// The download URL comes from a configured template rather than being built here: it
	// points at a service this one does not own, whose URL shape is that deployment's to
	// decide.
	download := renderTemplate(t.deps.KifshareTemplate, map[string]string{
		"url":       base,
		"ticket-id": name,
		"filename":  paths.Base(path),
	})

	return ticketView{
		Path:            path,
		TicketID:        name,
		DownloadURL:     download,
		DownloadPageURL: base + "/" + name,
	}
}

// templatePlaceholder matches the {{name}} slots the download template uses.
var templatePlaceholder = regexp.MustCompile(`\{\{([^}]+)\}\}`)

// renderTemplate fills in a configured template.
//
// A placeholder naming something not supplied is left exactly as it was rather than becoming
// an empty string. That is what the Clojure service's renderer does, and it means a template
// with a typo in it produces a visibly wrong URL rather than a subtly wrong one.
func renderTemplate(template string, values map[string]string) string {
	return templatePlaceholder.ReplaceAllStringFunc(template, func(match string) string {
		key := strings.TrimSuffix(strings.TrimPrefix(match, "{{"), "}}")
		if value, ok := values[key]; ok {
			return value
		}
		return match
	})
}

// ticketMode reads the access a ticket should grant, defaulting to read.
func ticketMode(c echo.Context) (string, error) {
	mode := strings.TrimSpace(c.QueryParam("mode"))
	switch mode {
	case "":
		return ticketModeRead, nil
	case ticketModeRead, ticketModeWrite:
		return mode, nil
	default:
		return "", schemaError("mode must be read or write")
	}
}

// ticketLimits reads the optional caps.
func ticketLimits(c echo.Context) (rods.TicketLimits, error) {
	uses, err := optionalInt(c, "uses-limit")
	if err != nil {
		return rods.TicketLimits{}, err
	}
	fileWrite, err := optionalInt(c, "file-write-limit")
	if err != nil {
		return rods.TicketLimits{}, err
	}
	return rods.TicketLimits{Uses: uses, FileWrite: fileWrite}, nil
}

// optionalBool reads a boolean parameter that may be absent.
func optionalBool(c echo.Context, name string) (bool, error) {
	if _, present := c.QueryParams()[name]; !present {
		return false, nil
	}
	return boolParam(c, name)
}

// optionalInt reads a whole-number parameter that may be absent.
func optionalInt(c echo.Context, name string) (*int64, error) {
	raw := strings.TrimSpace(c.QueryParam(name))
	if raw == "" {
		return nil, nil
	}

	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, schemaError(name + " must be a whole number")
	}
	return &value, nil
}

// requireAllReadable rejects a request naming anything the caller cannot read.
func requireAllReadable(ctx context.Context, scope *rods.Scope, requested []string) error {
	stats, err := scope.Stats(ctx, requested).Get(ctx)
	if err != nil {
		return err
	}

	var refused []string
	for _, path := range requested {
		stat, ok := stats[path]
		if !ok || !rods.Permits(stat.Permission, rods.PermissionRead) {
			refused = append(refused, path)
		}
	}
	if len(refused) > 0 {
		// The singular key with a list in it, which is what this validator attaches.
		return apierror.New(apierror.ErrNotReadable).With("path", refused)
	}
	return nil
}
