package handlers

import (
	"context"
	"strings"

	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/cyverse-de/data-info/internal/service"
	"github.com/labstack/echo/v4"
)

// rootFields are the fields a root or home entry carries. Both endpoints report the same
// six, and no more: a root listing is drawn as a sidebar, and the counts and share counts a
// full stat would compute cost a catalog query apiece for something nothing renders.
const rootFields = "id,label,path,date-created,date-modified,permission"

// basePaths is where a user's own collections live. The keys are underscored where the rest
// of the API hyphenates, which is what the Clojure service emits.
type basePaths struct {
	UserHome  string `json:"user_home_path"`
	UserTrash string `json:"user_trash_path"`
	BaseTrash string `json:"base_trash_path"`
}

// Home handles GET /navigation/home.
//
// It creates the collection when it is missing, which is why a read-only-looking endpoint
// writes: it is what a client calls to find out where to put things, and answering with a
// path that does not exist yet would just move the failure.
func (h *Listings) Home(c echo.Context) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return err
	}

	scope, err := h.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	if err := requireKnownUser(ctx, scope, user, true); err != nil {
		return err
	}

	home := h.deps.Layout.UserHome(user)
	if err := h.ensureCollection(ctx, scope, home); err != nil {
		return err
	}

	if err := requireRodsExists(ctx, scope, home); err != nil {
		return err
	}

	stat, err := service.StatOf(ctx, scope, user, home, service.StatOptions{
		Fields: service.ParseFieldSet(rootFields, ""),
		Layout: h.deps.Layout,
	})
	if err != nil {
		return err
	}

	return writeJSONOK(c, stat)
}

// Root handles GET /navigation/root.
//
// It reports the four collections a client opens onto -- the user's home, community data,
// the collection other people's shares appear under, and the user's trash -- with the base
// paths alongside.
//
// Each is validated as it is built, in order, so a caller who cannot read community data gets
// that failure and not one naming every root at once. The Clojure service builds the vector
// element by element and the first to throw wins; reporting them together would change what
// the client is told.
func (h *Listings) Root(c echo.Context) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return err
	}

	scope, err := h.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	if err := requireKnownUser(ctx, scope, user, true); err != nil {
		return err
	}

	layout := h.deps.Layout
	home := layout.UserHome(user)
	trash := layout.UserTrash(user)
	community := strings.TrimRight(layout.CommunityData, "/")
	sharing := strings.TrimRight(layout.Home, "/")

	// One query for all four before any of them is examined, so the four roots cost one round
	// trip rather than four. The Clojure service warms the same set for the same reason.
	if _, err := scope.Stats(ctx, []string{home, community, sharing, trash}).Get(ctx); err != nil {
		return err
	}

	roots := make([]service.Stat, 0, 4)
	for _, path := range []string{home, community, sharing} {
		root, err := h.rootEntry(ctx, scope, user, path)
		if err != nil {
			return err
		}
		roots = append(roots, root)
	}

	// The trash is made rather than merely read: a user who has never deleted anything has
	// none, and the client needs somewhere to point at regardless.
	if err := h.ensureCollection(ctx, scope, trash); err != nil {
		return err
	}
	if err := h.ensureOwned(ctx, scope, user, trash); err != nil {
		return err
	}
	trashRoot, err := h.rootEntry(ctx, scope, user, trash)
	if err != nil {
		return err
	}
	roots = append(roots, trashRoot)

	return writeJSONOK(c, map[string]any{
		"roots": roots,
		"base-paths": basePaths{
			UserHome:  layout.UserHome(user),
			UserTrash: layout.UserTrash(user),
			BaseTrash: layout.TrashBase(),
		},
	})
}

// rootEntry describes one root, refusing to describe one the caller cannot read.
func (h *Listings) rootEntry(ctx context.Context, scope *rods.Scope, user, path string) (service.Stat, error) {
	// Readability rather than existence: a root that is not there reads as unreadable, which
	// is the answer the Clojure service gives for both. CORE-7638 -- without this check a
	// null permission reaches the response and the client mishandles it.
	if err := requireRodsReadable(ctx, scope, user, path); err != nil {
		return service.Stat{}, err
	}

	return service.StatOf(ctx, scope, user, path, service.StatOptions{
		Fields: service.ParseFieldSet(rootFields, ""),
		Layout: h.deps.Layout,
	})
}

// ensureCollection creates a collection if nothing is there yet.
//
// Existence is asked as the service's own account, and so is the creation: a user has no
// write access to the collection their home or trash sits in, so asking as them would fail
// on the one case this exists for. The caller's own view is then invalidated, because it may
// already have recorded that nothing was there.
func (h *Listings) ensureCollection(ctx context.Context, scope *rods.Scope, path string) error {
	proxy, err := h.deps.OpenProxyScope(ctx)
	if err != nil {
		return err
	}
	defer proxy.Close()

	stat, err := proxy.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}
	if stat.Exists {
		return nil
	}

	h.deps.Logger().WithField("path", path).Info("creating a missing root collection")
	if err := proxy.MakeDir(ctx, path, true); err != nil {
		return err
	}

	scope.Invalidate(path)
	return nil
}

// ensureOwned makes the user the owner of their own trash when they are not already.
//
// Granted by the service's own account, which is what created the collection. A user cannot
// grant themselves access to something they do not own, so this cannot be done as them.
func (h *Listings) ensureOwned(ctx context.Context, scope *rods.Scope, user, path string) error {
	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}
	if stat.Permission == rods.PermissionOwn {
		return nil
	}

	proxy, err := h.deps.OpenProxyScope(ctx)
	if err != nil {
		return err
	}
	defer proxy.Close()

	h.deps.Logger().WithFields(map[string]any{"path": path, "user": user}).
		Info("granting a user ownership of their own trash")
	if err := proxy.SetPermission(ctx, path, user, rods.PermissionOwn, false); err != nil {
		return err
	}

	scope.Invalidate(path)
	return nil
}
