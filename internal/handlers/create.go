package handlers

import (
	"context"
	"sort"
	"strings"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/labstack/echo/v4"
)

// Writes serves the endpoints that change the data store.
type Writes struct {
	deps Deps
}

// NewWrites builds the write handlers.
func NewWrites(deps Deps) *Writes { return &Writes{deps: deps} }

// CreateDirectories handles POST /data/directories.
//
// It creates each requested collection along with any missing parents, and makes the
// requesting user the owner of the shallowest collection it created in each chain -- not the
// deepest, and not all of them. That is what gives the user control of the new subtree: the
// collections below inherit from the one they now own.
func (h *Writes) CreateDirectories(c echo.Context) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return err
	}

	var body pathsRequest
	if err := bindBody(c, &body); err != nil {
		return err
	}
	if len(body.Paths) == 0 {
		return schemaError("paths must not be empty")
	}
	if err := h.deps.CheckPathCount(len(body.Paths)); err != nil {
		return err
	}

	scope, err := h.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	if err := requireKnownUser(ctx, scope, user, false); err != nil {
		return err
	}

	// Duplicates in the request collapse, as they do in the reference, which works from a
	// set of paths.
	requested, err := uniquePaths(body.Paths)
	if err != nil {
		return err
	}

	plans := make([]createPlan, 0, len(requested))
	for _, path := range requested {
		if !goodPathname(path, h.deps.BadChars) {
			return apierror.New(apierror.ErrBadOrMissingField).With("path", path)
		}
		// Before the existence walk, matching the reference: its first look at the path
		// goes through a jargon call that validates the name lengths, so an over-long one
		// is refused before anything is created.
		if err := checkPathLength(path); err != nil {
			return err
		}

		plan, err := h.planCreate(ctx, scope, path)
		if err != nil {
			return err
		}
		plans = append(plans, plan)
	}

	// Every collection the new ones will hang from has to be writable before anything is
	// created, so a request that cannot fully succeed does not half-succeed.
	for _, plan := range plans {
		// The zone root is not a collection any user may write to, and it is not something
		// the catalog will answer a stat for either, so it is refused directly.
		if plan.existingAncestor == "/" {
			return apierror.New(apierror.ErrNotWriteable).With("path", plan.existingAncestor)
		}

		stat, err := scope.Stat(ctx, plan.existingAncestor).Get(ctx)
		if err != nil {
			return err
		}
		if !permits(stat.Permission, rods.PermissionWrite) {
			// The path alone, with no user: that is what the jargon validator this route
			// uses attaches, unlike the clj-irods one used elsewhere.
			return apierror.New(apierror.ErrNotWriteable).With("path", plan.existingAncestor)
		}
	}

	created := map[string]bool{}
	for _, plan := range plans {
		for _, path := range plan.toCreate {
			created[path] = true
		}
	}

	for _, plan := range plans {
		// A path may already have been created by an earlier entry in this same request,
		// when one requested collection is an ancestor of another.
		stat, err := scope.Stat(ctx, plan.path).Get(ctx)
		if err != nil {
			return err
		}
		if stat.Exists {
			continue
		}
		if err := scope.MakeDir(ctx, plan.path, true); err != nil {
			return err
		}
	}

	for _, plan := range plans {
		if plan.shallowestCreated == "" {
			continue
		}
		if err := scope.SetOwner(ctx, plan.shallowestCreated, user, true); err != nil {
			return err
		}
	}

	out := make([]string, 0, len(created))
	for path := range created {
		out = append(out, path)
	}
	sort.Strings(out)

	return writeJSONOK(c, map[string]any{"paths": out})
}

// createPlan is what has to happen for one requested collection.
type createPlan struct {
	path string

	// existingAncestor is the deepest collection on the path that already exists, and so
	// the one whose permissions decide whether the request may proceed.
	existingAncestor string

	// toCreate are the collections that do not exist yet, deepest first.
	toCreate []string

	// shallowestCreated is the topmost collection this request will create, which is the
	// one the user is made owner of.
	shallowestCreated string
}

// planCreate walks up from a path to the first collection that exists.
func (h *Writes) planCreate(ctx context.Context, scope *rods.Scope, path string) (createPlan, error) {
	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return createPlan{}, err
	}
	if stat.Exists {
		return createPlan{}, apierror.New(apierror.ErrExists).With("path", path)
	}

	plan := createPlan{path: path}

	for current := path; ; current = paths.Dir(current) {
		stat, err := scope.Stat(ctx, current).Get(ctx)
		if err != nil {
			return createPlan{}, err
		}
		if stat.Exists {
			plan.existingAncestor = current
			break
		}

		plan.toCreate = append(plan.toCreate, current)
		plan.shallowestCreated = current

		// Reaching the zone root ends the walk. The root collection always exists, so it
		// becomes the existing ancestor and the permission check below is what refuses the
		// request -- nobody may create a collection there. Reporting the path as missing
		// instead would be the wrong answer for a request naming a zone that does not
		// exist, which is the common way to arrive here.
		parent := paths.Dir(current)
		if parent == current || parent == "/" {
			plan.existingAncestor = "/"
			return plan, nil
		}
	}

	return plan, nil
}

// uniquePaths canonicalises the requested paths and removes duplicates, preserving the order
// first seen.
//
// Cleaning matters for more than tidiness. go-irodsclient cleans a path before acting on it,
// so a request naming "/zone/home/me/../other/new" would be checked for writability against
// one collection and created under another -- the permission check would pass on the
// caller's own home while the collection appeared somewhere else. Cleaning first makes the
// path that is validated the path that is created.
//
// A blank entry is rejected rather than skipped. The reference declares these as non-blank
// strings and answers 400, and dropping one silently would report success for a request that
// created nothing.
func uniquePaths(in []string) ([]string, error) {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))

	for _, p := range in {
		if strings.TrimSpace(p) == "" {
			return nil, schemaError("paths must not contain blank strings")
		}

		cleaned := strings.TrimRight(paths.Clean(p), "/")
		if cleaned == "" {
			// The zone root, reached as "/" or as something that climbs out to it. There
			// is no collection to create and no parent to check.
			return nil, schemaError("paths must name a collection, not the root")
		}
		if seen[cleaned] {
			continue
		}

		seen[cleaned] = true
		out = append(out, cleaned)
	}
	return out, nil
}

// goodPathname reports whether a path avoids the characters the service refuses in new
// names.
//
// This is the setting's real use: it governs what may be created, not what may be listed.
func goodPathname(path, badChars string) bool {
	return !strings.ContainsAny(path, badChars)
}
