package handlers

import (
	"context"
	"strings"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/labstack/echo/v4"
)

// groupRequest is the body of POST /groups.
type groupRequest struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

// groupMembersRequest is the body of PUT /groups/{group-name}.
type groupMembersRequest struct {
	Members []string `json:"members"`
}

// groupResponse is a group and who is in it.
type groupResponse struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

// Groups serves the group endpoints.
type Groups struct{ deps Deps }

// NewGroups builds the group handlers.
func NewGroups(deps Deps) *Groups { return &Groups{deps: deps} }

// Get handles GET /groups/{group-name}.
func (g *Groups) Get(c echo.Context) error {
	ctx := c.Request().Context()

	_, scope, err := g.open(c)
	if err != nil {
		return err
	}
	defer scope.Close()

	name := c.Param("group-name")

	if err := g.requireGroupExists(ctx, scope, name); err != nil {
		return err
	}

	return g.respond(c, ctx, scope, name)
}

// Create handles POST /groups.
func (g *Groups) Create(c echo.Context) error {
	ctx := c.Request().Context()

	user, scope, err := g.open(c)
	if err != nil {
		return err
	}
	defer scope.Close()

	var body groupRequest
	if err := bindBody(c, &body); err != nil {
		return err
	}
	if strings.TrimSpace(body.Name) == "" {
		return schemaError("name must be a non-blank string")
	}

	if err := g.requireGroupAdmin(ctx, scope, user); err != nil {
		return err
	}

	exists, err := scope.GroupExists(ctx, body.Name)
	if err != nil {
		return err
	}
	if exists {
		return apierror.New(apierror.ErrExists).With("group", body.Name)
	}

	// Every member is checked before the group is made. A group created with members that
	// turned out not to exist would be half of what was asked for, with no way to tell
	// from the response which half.
	if err := g.requireAllUsersExist(ctx, scope, body.Members); err != nil {
		return err
	}

	if err := scope.CreateGroup(ctx, body.Name); err != nil {
		return err
	}
	for _, member := range body.Members {
		account, zone := splitAccount(member, g.deps.Layout.Zone)
		if err := scope.AddGroupMemberIn(ctx, body.Name, account, zone); err != nil {
			return err
		}
	}

	return g.respond(c, ctx, scope, body.Name)
}

// Update handles PUT /groups/{group-name}.
//
// The members given are the whole membership afterwards, not additions: anyone not named is
// removed. Members that do not exist are dropped rather than refused, so one departed account
// cannot stop a group being edited -- which is deliberate, and different from creating.
func (g *Groups) Update(c echo.Context) error {
	ctx := c.Request().Context()

	user, scope, err := g.open(c)
	if err != nil {
		return err
	}
	defer scope.Close()

	var body groupMembersRequest
	if err := bindBody(c, &body); err != nil {
		return err
	}

	name := c.Param("group-name")

	if err := g.requireGroupAdmin(ctx, scope, user); err != nil {
		return err
	}
	if err := g.requireGroupExists(ctx, scope, name); err != nil {
		return err
	}

	current, err := scope.GroupMembers(ctx, name)
	if err != nil {
		return err
	}

	desired := map[string]bool{}
	for _, member := range body.Members {
		known, err := scope.UserExists(ctx, member).Get(ctx)
		if err != nil {
			return err
		}
		if known {
			desired[qualify(member, g.deps.Layout.Zone)] = true
		}
	}

	held := map[string]bool{}
	for _, member := range current {
		held[qualify(member, g.deps.Layout.Zone)] = true
	}

	for member := range desired {
		if held[member] {
			continue
		}
		account, zone := splitAccount(member, g.deps.Layout.Zone)
		if err := scope.AddGroupMemberIn(ctx, name, account, zone); err != nil {
			return err
		}
	}
	for member := range held {
		if desired[member] {
			continue
		}
		account, zone := splitAccount(member, g.deps.Layout.Zone)
		if err := scope.RemoveGroupMemberIn(ctx, name, account, zone); err != nil {
			return err
		}
	}

	return g.respond(c, ctx, scope, name)
}

// Delete handles DELETE /groups/{group-name}.
//
// Deleting a group that is not there succeeds. A caller asking for it to be gone has got what
// they asked for, and reporting a failure would make retrying a delete an error.
func (g *Groups) Delete(c echo.Context) error {
	ctx := c.Request().Context()

	user, scope, err := g.open(c)
	if err != nil {
		return err
	}
	defer scope.Close()

	name := c.Param("group-name")

	if err := g.requireGroupAdmin(ctx, scope, user); err != nil {
		return err
	}

	exists, err := scope.GroupExists(ctx, name)
	if err != nil {
		return err
	}
	if exists {
		if err := scope.DeleteGroup(ctx, name); err != nil {
			return err
		}
	}

	return writeJSONOK(c, map[string]string{"name": name})
}

// open reads the caller and opens their view, which every group endpoint starts with.
func (g *Groups) open(c echo.Context) (string, *rods.Scope, error) {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return "", nil, err
	}

	scope, err := g.deps.OpenScope(ctx, user)
	if err != nil {
		return "", nil, err
	}

	if err := requireKnownUser(ctx, scope, user, false); err != nil {
		scope.Close()
		return "", nil, err
	}

	return user, scope, nil
}

// respond answers with a group and its current membership, read back rather than assumed.
func (g *Groups) respond(c echo.Context, ctx context.Context, scope *rods.Scope, name string) error {
	members, err := scope.GroupMembers(ctx, name)
	if err != nil {
		return err
	}
	if members == nil {
		members = []string{}
	}
	return writeJSONOK(c, groupResponse{Name: name, Members: members})
}

// requireGroupAdmin rejects a caller who may not administer groups.
func (g *Groups) requireGroupAdmin(ctx context.Context, scope *rods.Scope, user string) error {
	kind, err := scope.UserKind(ctx, user).Get(ctx)
	if err != nil {
		return err
	}
	if kind == icat.UserKindAdmin || kind == icat.UserKindGroupAdmin {
		return nil
	}
	// No status of its own. The routes document a 403, but they are written as (ok ...) in
	// the reference, so the thrown code reaches the default handler and answers 500 -- which
	// is what callers actually see. See docs/deferred-fixes.md.
	return apierror.New(apierror.ErrForbidden)
}

// requireGroupExists rejects a group that is not there.
func (g *Groups) requireGroupExists(ctx context.Context, scope *rods.Scope, name string) error {
	exists, err := scope.GroupExists(ctx, name)
	if err != nil {
		return err
	}
	if !exists {
		return apierror.New(apierror.ErrDoesNotExist).With("group", name)
	}
	return nil
}

// requireAllUsersExist rejects a request naming accounts iRODS has never heard of.
func (g *Groups) requireAllUsersExist(ctx context.Context, scope *rods.Scope, users []string) error {
	var missing []string
	for _, user := range users {
		known, err := scope.UserExists(ctx, user).Get(ctx)
		if err != nil {
			return err
		}
		if !known {
			missing = append(missing, user)
		}
	}
	if len(missing) > 0 {
		return apierror.New(apierror.ErrNotAUser).With("users", missing)
	}
	return nil
}

// qualify appends the zone to a bare account name, so that two spellings of the same account
// compare equal.
func qualify(name, zone string) string {
	if strings.Contains(name, "#") {
		return name
	}
	return name + "#" + zone
}

// splitAccount separates a possibly-qualified account name into its name and its zone.
//
// Both are needed: iRODS takes them as separate arguments, so passing "someone#otherzone"
// through as a name would have it looked up in the local zone under a name containing a hash.
func splitAccount(name, defaultZone string) (account, zone string) {
	if at := strings.LastIndex(name, "#"); at >= 0 {
		return name[:at], name[at+1:]
	}
	return name, defaultZone
}
