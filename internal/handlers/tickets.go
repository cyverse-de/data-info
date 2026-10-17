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

// Add handles POST /tickets. A ticket is a bearer token, so creating one needs write access,
// except a read-only ticket for an analysis.
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

	admin, err := t.deps.OpenProxyScope(ctx)
	if err != nil {
		return err
	}
	defer admin.Close()

	if err := requireAllExist(ctx, admin, requested); err != nil {
		return err
	}

	// The DE creates analysis tickets on inputs the user can read but doesn't own.
	if forJob && mode == ticketModeRead {
		if err := requireAllReadable(ctx, scope, requested); err != nil {
			return err
		}
	} else if err := requireAllWriteable(ctx, scope, user, requested); err != nil {
		return err
	}

	// Issued as the service account: iRODS scopes ticket listings to the connection's user,
	// and visibility here follows path permissions, so a caller-owned ticket would be hidden
	// from other writers and from deletion cleanup.
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
	// Upper case, matching the ticket ids already in circulation in shared URLs.
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

	admin, err := t.deps.OpenProxyScope(ctx)
	if err != nil {
		return err
	}
	defer admin.Close()

	if err := requireAllExist(ctx, admin, requested); err != nil {
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
		// Every missing ticket, under the plural key callers read.
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

// renderTemplate fills in a configured template, leaving unknown placeholders as-is so a typo
// yields a visibly wrong URL.
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
		// "path" holding a list is a known quirk, kept because callers parse it.
		return apierror.New(apierror.ErrNotReadable).With("path", refused)
	}
	return nil
}
